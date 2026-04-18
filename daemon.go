// Copyright 2026 HAProxy Technologies LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/haproxytech/gopherd/check"
	"github.com/haproxytech/gopherd/control"
	"github.com/haproxytech/gopherd/logger"
	"github.com/haproxytech/gopherd/metrics"
	"github.com/haproxytech/gopherd/order"
	"github.com/haproxytech/gopherd/service"
	"github.com/haproxytech/gopherd/yml"
)

// maxConfigFileSize caps how many bytes readConfigFile will consume from the
// gopherd config file. Real configs are typically a few KiB; 4 MiB is
// generous for comment-heavy or large examples while preventing an
// operator-supplied or swapped-out huge file from OOM-killing PID 1.
const maxConfigFileSize = 4 << 20

// readConfigFile opens path, verifies it is not a symlink and passes
// permission checks, then returns its contents. All checks are performed on
// the open file descriptor to eliminate the TOCTOU window that exists when
// a separate Lstat+Stat precedes an independent ReadFile call.
//
// O_NOFOLLOW causes the open to fail immediately if the final path component
// is a symlink, making the check atomic with the open.
//
// The read is capped at maxConfigFileSize so a misconfigured
// GOPHERD_CONFIG pointing at a huge file, or a config-management pipeline
// that accidentally replaced the file, cannot drive PID 1 into an
// unbounded allocation.
func readConfigFile(path string) ([]byte, error) {
	// syscall.Open with O_NOFOLLOW fails with ELOOP (Linux/macOS/FreeBSD)
	// if path is a symlink, so no separate Lstat is needed.
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if err == syscall.ELOOP {
			return nil, fmt.Errorf("config %s is a symlink; refusing to open", path)
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()

	// f.Stat() calls fstat(2) on the open fd, so permission checks are on the
	// same inode we will read — no TOCTOU between the check and the read.
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// Reject non-regular files. Without this check, a FIFO/pipe at the config
	// path would cause io.ReadAll to block forever (stalling startup or
	// reload), and a character/block device would return unexpected bytes.
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("config %s is not a regular file (mode %s); refusing to open", path, info.Mode())
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if ok {
		mode := info.Mode()
		if mode&0o002 != 0 {
			return nil, fmt.Errorf("config %s is world-writable (mode %04o, owner uid=%d); refusing to open", path, mode.Perm(), stat.Uid)
		}
		euid := uint32(os.Geteuid())
		if stat.Uid != 0 && stat.Uid != euid {
			return nil, fmt.Errorf("config %s is owned by uid %d (expected root or uid %d); refusing to open", path, stat.Uid, euid)
		}
		if mode&0o020 != 0 {
			log.Printf("warning: config %s is group-writable (mode %04o, owner uid=%d)", path, mode.Perm(), stat.Uid)
		}
	}

	// Read at most maxConfigFileSize+1 so we can distinguish "exactly at the
	// cap" from "overflowed". If we read maxConfigFileSize+1 bytes, the
	// file is too large.
	data, err := io.ReadAll(io.LimitReader(f, maxConfigFileSize+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxConfigFileSize {
		return nil, fmt.Errorf("config %s exceeds %d-byte size cap", path, maxConfigFileSize)
	}
	return data, nil
}

// errShuttingDown is returned by startService when the daemon has already
// started its shutdown sequence. Callers must not treat this as a fatal error.
var errShuttingDown = fmt.Errorf("daemon is shutting down")

// errServiceReplaced is returned by startService when the service being started
// is no longer the current instance in d.services (replaced or removed by a
// reload). The restart goroutine must silently skip this rather than treating
// it as a fatal error that triggers shutdown.
var errServiceReplaced = fmt.Errorf("service replaced or removed by reload")

// daemon holds all mutable daemon state so reload can update it.
type daemon struct {
	cfg       *yml.Config
	services  map[string]*service.Service
	m         *metrics.Metrics
	pidMap    map[int]*service.Service
	restartCh chan restartReq

	// shutdownCh is closed exactly once by initiateShutdown. Goroutines that
	// need to wake early on shutdown (e.g. the restart backoff sleeper) select
	// on this channel. A closed channel broadcasts to all receivers at once.
	shutdownCh chan struct{}

	configPath     string
	shutdownMode   string // "reverse-dep" (default), "dep", "simultaneous"
	checkers       []*check.Checker
	logTargets     []*logger.Target
	entrypointArgs []string
	shutdownSeq    []string // start order; used to derive shutdown sequence

	// senderWg tracks fire-and-forget goroutines that write to restartCh.
	// It must reach zero before restartCh is closed.
	senderWg sync.WaitGroup

	mu sync.RWMutex
	// reloadMu serialises concurrent hot-reloads so that two rapid SIGHUPs
	// cannot race on d.checkers, d.cfg, or d.services.
	//
	// Lock ordering: reloadMu is only ever acquired by reload(), and reload()
	// must take it BEFORE mu. All other code paths (control-socket handlers,
	// signal handlers, reap loop) take only mu. This one-way ordering makes
	// deadlock impossible: no caller ever needs to acquire reloadMu while
	// holding mu.
	reloadMu sync.Mutex

	// exitCode and shuttingDown are accessed from multiple goroutines.
	// Use atomic types so they can be read without holding mu.
	exitCode     atomic.Int32
	shuttingDown atomic.Bool
}

type restartReq struct {
	svc *service.Service
	// done is the service's Done() channel captured at the moment the restart
	// was enqueued. The restart handler waits on this channel rather than
	// calling req.svc.Done() at processing time, which would race against a
	// concurrent restart that has already re-created the done channel.
	done  <-chan struct{}
	delay time.Duration
}

func (d *daemon) startService(svc *service.Service) (int, error) {
	// Register the service in pidMap while holding mu so the reap loop cannot
	// observe the process exit before we have recorded the PID. Checking
	// shuttingDown under the same lock closes the race where a concurrent
	// initiateShutdown completes stopAll() between the caller's pre-check and
	// this function's fork/exec, which would start a process that never gets
	// a stop signal.
	d.mu.Lock()
	if d.shuttingDown.Load() {
		d.mu.Unlock()
		return 0, errShuttingDown
	}
	// Reject stale restart requests: if a reload replaced or removed this
	// service between the restart goroutine enqueuing the request and now,
	// starting the old instance would briefly run two copies of the service.
	if current, ok := d.services[svc.Name]; !ok || current != svc {
		d.mu.Unlock()
		return 0, errServiceReplaced
	}
	pid, err := svc.Start()
	if err != nil {
		d.mu.Unlock()
		return 0, err
	}
	d.pidMap[pid] = svc
	// Record ServiceStarted under d.mu so a fast-exiting child cannot be
	// reaped (which calls ServiceExited) before we have recorded the start.
	// With the call outside the lock the metrics could show exits > starts
	// transiently, which is misleading in "stats" output.
	d.m.ServiceStarted(svc.Name)
	d.mu.Unlock()
	log.Printf("started %s (pid %d)", svc.Name, pid)
	return pid, nil
}

// stopAll stops all services according to the configured shutdown mode.
//
// Modes:
//   - "reverse-dep" (default): reverse dependency order (dependents first).
//   - "dep": dependency order (dependencies first, then dependents).
//   - "simultaneous": all services stopped concurrently.
//
// Services not in the shutdown sequence are stopped last (sequential modes)
// or together with everyone (simultaneous).
func (d *daemon) stopAll() {
	mode := d.shutdownMode
	if mode == "" || mode == yml.ShutdownReverseDep {
		d.stopSequential(d.reverseSeq())
		return
	}
	if mode == yml.ShutdownDep {
		d.stopSequential(d.shutdownSeq)
		return
	}
	// simultaneous
	d.stopSimultaneous()
}

func (d *daemon) reverseSeq() []string {
	n := len(d.shutdownSeq)
	rev := make([]string, n)
	for i, name := range d.shutdownSeq {
		rev[n-1-i] = name
	}
	return rev
}

// stopSequential stops services in the given order, waiting for each to fully
// exit before stopping the next.
//
// Callers must hold d.mu on entry. This function temporarily releases d.mu
// while waiting for each process to exit so the reap loop can call MarkExited
// (which closes the done channel) without deadlocking.
func (d *daemon) stopSequential(seq []string) {
	stopped := make(map[string]bool)
	for _, name := range seq {
		svc, ok := d.services[name]
		if !ok || !svc.IsRunning() {
			continue
		}
		// Capture done before Stop so we never miss the close even if the
		// process exits between Stop() and the channel receive below.
		done := svc.Done()
		svc.Stop()
		stopped[name] = true
		// Release d.mu between stops so the reap loop can call MarkExited
		// (which closes done) without deadlocking on d.mu.
		d.mu.Unlock()
		<-done
		d.mu.Lock()
	}
	// Stop any services not covered by the ordered sequence.
	for name, svc := range d.services {
		if stopped[name] || !svc.IsRunning() {
			continue
		}
		done := svc.Done()
		svc.Stop()
		d.mu.Unlock()
		<-done
		d.mu.Lock()
	}
}

// stopSimultaneous stops all services concurrently, then waits for every
// process to fully exit.
//
// Callers must hold d.mu on entry. Like stopSequential, this function
// temporarily releases d.mu while waiting so the reap loop can call
// MarkExited without deadlocking.
func (d *daemon) stopSimultaneous() {
	// Collect done channels and send stop signals while holding d.mu,
	// then release the lock so the reap loop can call MarkExited.
	dones := make([]<-chan struct{}, 0, len(d.services))
	for _, svc := range d.services {
		if svc.IsRunning() {
			dones = append(dones, svc.Done())
			svc.Stop()
		}
	}
	if len(dones) == 0 {
		return
	}
	d.mu.Unlock()
	for _, done := range dones {
		<-done
	}
	d.mu.Lock()
}

// initiateShutdown is safe to call from any goroutine without holding d.mu.
// CompareAndSwap ensures stopAll is called exactly once even if multiple
// goroutines race to initiate shutdown simultaneously.
func (d *daemon) initiateShutdown(code int) {
	if !d.shuttingDown.CompareAndSwap(false, true) {
		return
	}
	// Closing shutdownCh broadcasts to all select-waiting goroutines (e.g. the
	// restart backoff sleeper) so they can exit without waiting out their delay.
	close(d.shutdownCh)
	d.exitCode.Store(int32(code))
	d.mu.Lock()
	d.stopAll()
	d.mu.Unlock()
}

func (d *daemon) handleCheckFailure(checkName string) {
	d.mu.Lock()
	if d.shuttingDown.Load() {
		d.mu.Unlock()
		return
	}
	for _, svc := range d.services {
		action, ok := svc.OnCheckFailure[checkName]
		if !ok {
			continue
		}
		log.Printf("check %s failed: %s %s", checkName, action, svc.Name)
		switch action {
		case service.ActionRestart:
			// Stop the service and enqueue a restart directly, bypassing the
			// reap loop's exit-action evaluation. Without this, restart would
			// only happen if the service also has on-success/on-failure: restart,
			// which is counterintuitive and not what the user requested.
			// Capture done before Stop so the restart handler waits on this
			// specific exit, not a future done channel created by a concurrent
			// restart that may have already re-created svc.done.
			// Use senderWg.Go (blocking send in a tracked goroutine) instead of a
			// non-blocking select so the restart is never silently dropped. A
			// dropped restart combined with the default OnSuccess=ActionShutdown
			// would cause an unexpected daemon shutdown when the stopped service
			// exits with effectiveCode=0 (WasStopped+signal-death path).
			done := svc.Done()
			svc.Stop()
			d.senderWg.Go(func() {
				d.restartCh <- restartReq{svc: svc, done: done, delay: 0}
			})
			continue
		case service.ActionShutdown:
			d.mu.Unlock()
			d.initiateShutdown(1)
			return
		case service.ActionSuccessShutdown:
			d.mu.Unlock()
			d.initiateShutdown(0)
			return
		case service.ActionFailureShutdown:
			d.mu.Unlock()
			d.initiateShutdown(1)
			return
		case service.ActionIgnore:
			// do nothing
		}
	}
	d.mu.Unlock()
}

func (d *daemon) buildLogTargets() {
	for name, ltCfg := range d.cfg.LogTargets {
		lt, err := logger.NewTarget(name, ltCfg)
		if err != nil {
			log.Printf("warning: log-target %s: %v", name, err)
			continue
		}
		d.logTargets = append(d.logTargets, lt)
		log.Printf("configured log-target %s (%s)", name, ltCfg.Type)
	}
}

// buildServices constructs runtime Service wrappers for every process in the
// current config. Returns an error if any process config is malformed (e.g.
// invalid stop-signal or duration), so callers can abort startup or reload
// before applying partial state.
func (d *daemon) buildServices() error {
	d.services = make(map[string]*service.Service)
	for _, p := range d.cfg.Processes {
		// Inject entrypoint args into the designated service.
		if p.UseEntrypointArgs && len(d.entrypointArgs) > 0 {
			p.Args = append(p.Args, d.entrypointArgs...)
		}
		// Resolve the effective prefix into p.Prefix so processConfigChanged
		// can detect global prefix changes on reload (not just per-service ones).
		if p.Prefix == "" {
			p.Prefix = d.cfg.Prefix
		}
		svc, err := service.New(p, d.cfg.Prefix)
		if err != nil {
			return err
		}
		d.services[svc.Name] = svc
		for _, lt := range d.logTargets {
			if lt.AppliesTo(svc.Name) {
				svc.Stdout.AddTarget(lt.Writer)
				svc.Stderr.AddTarget(lt.Writer)
			}
		}
	}
	return nil
}

// buildOrderServices converts a Config's process list into the format needed
// for topological sorting. Extracted so reload() can validate the new config's
// dependency graph before mutating any daemon state.
func buildOrderServices(cfg *yml.Config) []order.Service {
	result := make([]order.Service, len(cfg.Processes))
	for i, p := range cfg.Processes {
		name := p.Name
		if name == "" {
			name = p.Command
		}
		result[i] = order.Service{
			Name:     name,
			After:    p.After,
			Before:   p.Before,
			Requires: p.Requires,
		}
	}
	return result
}

func (d *daemon) startOrder() ([]string, error) {
	return order.TopoSort(buildOrderServices(d.cfg))
}

func (d *daemon) startChecks() {
	// Build a map from check name to the credential of the service that
	// references it (via on-check-failure or ready-check). Exec checks
	// then run as that service's user instead of as root.
	checkOwner := make(map[string]*service.Service)
	for _, svc := range d.services {
		if svc.Proc.ReadyCheck != "" {
			checkOwner[svc.Proc.ReadyCheck] = svc
		}
		for checkName := range svc.OnCheckFailure {
			if _, exists := checkOwner[checkName]; !exists {
				checkOwner[checkName] = svc
			}
		}
	}

	for name, checkCfg := range d.cfg.Checks {
		c, err := check.New(name, checkCfg, d.handleCheckFailure, d.m.CheckResult)
		if err != nil {
			log.Printf("warning: check %s: %v", name, err)
			continue
		}
		if svc, ok := checkOwner[name]; ok && checkCfg.Exec != nil {
			cred, err := service.ResolveCredential(svc.Proc.User, svc.Proc.Group, svc.Proc.UserID, svc.Proc.GroupID)
			if err != nil {
				log.Printf("warning: check %s: resolve credential: %v", name, err)
			} else if cred != nil {
				c.SetCredential(cred)
			}
		}
		d.checkers = append(d.checkers, c)
		c.Run()
		log.Printf("started check %s", name)
	}
}

func (d *daemon) stopChecks() {
	for _, c := range d.checkers {
		c.Stop()
	}
	d.checkers = nil
}

func (d *daemon) closeLogTargets() {
	for _, svc := range d.services {
		svc.Stdout.Flush()
		svc.Stderr.Flush()
	}
	for _, lt := range d.logTargets {
		lt.Close()
	}
	d.logTargets = nil
}

// reload re-reads the config and reconciles services, checks, and log targets.
func (d *daemon) reload() (string, error) {
	// Serialize concurrent reloads (e.g., two rapid SIGHUPs) so they cannot
	// race on d.checkers, d.cfg, or d.services.
	d.reloadMu.Lock()
	defer d.reloadMu.Unlock()

	data, err := readConfigFile(d.configPath)
	if err != nil {
		return "", fmt.Errorf("reload blocked: %w", err)
	}
	newCfg, err := yml.Unmarshal(data)
	if err != nil {
		return "", fmt.Errorf("reload config: %w", err)
	}

	// Enforce use-entrypoint-args uniqueness on reload, matching the startup check
	// in run(). Without this a hot-reload could install a config that would have
	// been rejected at startup.
	var entrypointCount int
	for _, p := range newCfg.Processes {
		if p.UseEntrypointArgs {
			entrypointCount++
		}
	}
	if entrypointCount > 1 {
		return "", fmt.Errorf("reload blocked: only one process may set use-entrypoint-args: true")
	}

	// Pre-validate the dependency graph before acquiring d.mu or mutating any
	// state. This ensures a bad config (e.g., cycle) fails fast with no
	// side-effects, rather than leaving services in a half-reconciled state.
	startOrd, err := order.TopoSort(buildOrderServices(newCfg))
	if err != nil {
		return "", fmt.Errorf("reload dependencies: %w", err)
	}

	// Pre-validate every process config (stop-signal, kill-delay, backoff-*,
	// exit actions) before mutating state. service.New is the authoritative
	// validator, so a dry-run here guarantees buildServices below cannot fail
	// mid-reload and leave the daemon in a half-applied state. Without this a
	// typo like kill-delay: "bogus" in a SIGHUP-reloaded config would abort
	// reload after services had already been stopped and removed.
	for _, p := range newCfg.Processes {
		if _, err := service.New(p, newCfg.Prefix); err != nil {
			return "", fmt.Errorf("reload: %w", err)
		}
	}

	d.mu.Lock()

	if d.shuttingDown.Load() {
		d.mu.Unlock()
		return "", fmt.Errorf("shutting down, reload not possible")
	}

	// Build new service set from config.
	newNames := make(map[string]bool)
	for _, p := range newCfg.Processes {
		name := p.Name
		if name == "" {
			name = p.Command
		}
		newNames[name] = true
	}

	// Snapshot the current service map before any mutations so the reconcile
	// loop below sees every service that was running before this reload,
	// including ones we are about to remove.
	oldServices := maps.Clone(d.services)

	// Stop and remove services that are no longer in config.
	// Set exit actions to ActionIgnore before removing so the reap loop
	// treats the eventual exit as benign rather than triggering a shutdown.
	// Invariant: OnSuccess and OnFailure are plain fields that must only be
	// read or written while holding d.mu. The reap loop reads them under
	// d.mu; both mutation sites here are inside d.mu.Lock().
	for name, svc := range d.services {
		if !newNames[name] {
			log.Printf("reload: removing service %s", name)
			svc.OnSuccess = service.ActionIgnore
			svc.OnFailure = service.ActionIgnore
			if svc.IsRunning() {
				svc.Stop()
			}
			// Disconnect from log targets before removal so the subsequent
			// ClearTargets loop (which iterates d.services) does not miss this
			// service. Old targets are closed immediately after that loop.
			svc.Stdout.ClearTargets()
			svc.Stderr.ClearTargets()
			delete(d.services, name)
		}
	}

	// Stop old checks.
	d.stopChecks()

	// Reconcile log targets: disconnect all current services from old target
	// writers, close old targets, then rebuild from new config. New service
	// wrappers created by buildServices() will be wired to the new targets
	// automatically. Preserved services (kept running below) are re-wired
	// explicitly after the reconcile loop.
	for _, svc := range d.services {
		svc.Stdout.ClearTargets()
		svc.Stderr.ClearTargets()
	}
	for _, lt := range d.logTargets {
		lt.Close()
	}
	d.logTargets = nil

	d.cfg = newCfg
	d.buildLogTargets()
	// Config was already pre-validated above, so buildServices cannot fail here
	// in practice. Defence-in-depth: if it does, abort the reload with d.mu
	// still held so the partial state is internally consistent (already-stopped
	// services stay stopped, caller sees the error).
	if err := d.buildServices(); err != nil {
		d.mu.Unlock()
		return "", fmt.Errorf("reload: %w", err)
	}

	// Preserve running state: if a service was running and its process config
	// is unchanged, keep the existing service wrapper. If any field that
	// affects the running process changed, stop the old instance so the new
	// config takes effect on the next start.
	var waitForOld []<-chan struct{}
	for name, oldSvc := range oldServices {
		newSvc, exists := d.services[name]
		if !exists {
			continue
		}
		if oldSvc.IsRunning() && !processConfigChanged(oldSvc.Proc, newSvc.Proc) {
			// Config unchanged — keep the old running instance.
			// Re-wire its PrefixWriters to the newly built log targets.
			for _, lt := range d.logTargets {
				if lt.AppliesTo(oldSvc.Name) {
					oldSvc.Stdout.AddTarget(lt.Writer)
					oldSvc.Stderr.AddTarget(lt.Writer)
				}
			}
			// Update policy fields in-place. These do not require a process
			// restart, but must be applied immediately so that a reload
			// changing on-success, on-failure, on-check-failure, or requires
			// takes effect for the current run rather than being silently lost.
			// All four fields are read by the reap loop or check callbacks
			// under d.mu, which is held here, so the update is race-free.
			oldSvc.OnSuccess = newSvc.OnSuccess
			oldSvc.OnFailure = newSvc.OnFailure
			oldSvc.OnCheckFailure = newSvc.OnCheckFailure
			oldSvc.Requires = newSvc.Requires
			// exit-code-map and signal-rewrite live on Proc and are read by
			// the reap loop / signal-forward branch. Copy them so a reload
			// that only tunes these maps takes effect without a restart.
			oldSvc.Proc.ExitCodeMap = newSvc.Proc.ExitCodeMap
			oldSvc.Proc.SignalRewrite = newSvc.Proc.SignalRewrite
			d.services[name] = oldSvc
		} else if oldSvc.IsRunning() {
			// Config changed — stop old instance. Set exit actions to ignore
			// so the reap loop treats the upcoming exit as benign and does not
			// trigger a restart or shutdown while we start the new instance.
			log.Printf("reload: restarting changed service %s", name)
			oldSvc.OnSuccess = service.ActionIgnore
			oldSvc.OnFailure = service.ActionIgnore
			waitForOld = append(waitForOld, oldSvc.Done())
			oldSvc.Stop()
		}
	}

	// Update shutdown sequence and mode from new config.
	d.shutdownSeq = startOrd
	d.shutdownMode = newCfg.ShutdownOrder

	// Collect services that need starting.
	var toStart []*service.Service
	for _, name := range startOrd {
		svc, ok := d.services[name]
		if !ok {
			continue
		}
		if !svc.Enabled || svc.Oneshot {
			continue
		}
		if svc.IsRunning() {
			continue // already running (preserved from old config)
		}
		toStart = append(toStart, svc)
	}

	// Release the lock before waiting and starting services, since startService
	// acquires d.mu internally, and the reap loop needs d.mu to call MarkExited
	// (which closes the done channels we are about to wait on).
	d.mu.Unlock()

	// Wait for stopped old instances to fully exit before starting their
	// replacements. Without this wait, two instances of the same service could
	// run simultaneously during the reload window.
	for _, done := range waitForOld {
		<-done
	}

	for _, svc := range toStart {
		if _, err := d.startService(svc); err != nil && err != errShuttingDown && err != errServiceReplaced {
			log.Printf("reload: start %s failed: %v", svc.Name, err)
		}
	}

	// Restart checks with new config. Hold the lock so a concurrent reload
	// (e.g., two rapid SIGHUPs) cannot race on d.checkers or d.cfg.
	d.mu.Lock()
	d.startChecks()
	d.mu.Unlock()

	return "reload: ok", nil
}

// setupControl wires all control-socket handler callbacks and starts the server.
func (d *daemon) setupControl() *control.Server {
	ctrlServer := control.NewServer(d.cfg.Control)
	ctrlServer.StatsFn = func() string {
		return d.m.Format()
	}
	ctrlServer.ListFn = func() string {
		d.mu.RLock()
		defer d.mu.RUnlock()
		var lines []string
		for _, svc := range d.services {
			state := "stopped"
			if svc.IsRunning() {
				state = fmt.Sprintf("running (pid %d)", int(svc.Pid.Load()))
			}
			lines = append(lines, fmt.Sprintf("%-20s %s", svc.Name, state))
		}
		if len(lines) == 0 {
			return "no services"
		}
		slices.Sort(lines)
		return strings.Join(lines, "\n")
	}
	ctrlServer.StatusFn = func(name string) (string, error) {
		d.mu.RLock()
		svc, ok := d.services[name]
		d.mu.RUnlock()
		if !ok {
			return "", fmt.Errorf("unknown service %q", name)
		}
		if svc.IsRunning() {
			return fmt.Sprintf("%s: running (pid %d)", name, int(svc.Pid.Load())), nil
		}
		return fmt.Sprintf("%s: stopped", name), nil
	}
	ctrlServer.StartFn = func(name string) (string, error) {
		d.mu.RLock()
		svc, ok := d.services[name]
		d.mu.RUnlock()
		if !ok {
			return "", fmt.Errorf("unknown service %q", name)
		}
		if svc.IsRunning() {
			return fmt.Sprintf("%s: already running (pid %d)", name, int(svc.Pid.Load())), nil
		}
		pid, err := d.startService(svc)
		if err != nil {
			return "", fmt.Errorf("start %s: %w", name, err)
		}
		return fmt.Sprintf("%s: started (pid %d)", name, pid), nil
	}
	ctrlServer.StopFn = func(name string) (string, error) {
		d.mu.RLock()
		svc, ok := d.services[name]
		d.mu.RUnlock()
		if !ok {
			return "", fmt.Errorf("unknown service %q", name)
		}
		if !svc.IsRunning() {
			return fmt.Sprintf("%s: already stopped", name), nil
		}
		svc.Stop()
		return fmt.Sprintf("%s: stop signal sent", name), nil
	}
	ctrlServer.SignalFn = func(name, sigName string) (string, error) {
		d.mu.RLock()
		svc, ok := d.services[name]
		d.mu.RUnlock()
		if !ok {
			return "", fmt.Errorf("unknown service %q", name)
		}
		if !svc.IsRunning() {
			return "", fmt.Errorf("%s is not running", name)
		}
		sig, err := service.ParseSignal(sigName)
		if err != nil {
			return "", err
		}
		svc.Signal(sig)
		return fmt.Sprintf("%s: sent %s", name, sigName), nil
	}
	ctrlServer.RestartFn = func(name string) (string, error) {
		d.mu.Lock()
		svc, ok := d.services[name]
		if !ok {
			d.mu.Unlock()
			return "", fmt.Errorf("unknown service %q", name)
		}
		// Refuse the restart early if the daemon has started shutting down, so
		// we never stop a service with no plan to bring it back up.
		if d.shuttingDown.Load() {
			d.mu.Unlock()
			return "", fmt.Errorf("daemon is shutting down")
		}
		// Capture done under d.mu. Before calling Stop(), verify that restartCh
		// has capacity for the enqueue: otherwise we would stop the service and
		// leave it stopped with no pending restart, which is inconsistent from
		// the caller's perspective ("restart queue full" but the service is
		// actually down). A non-blocking send outside the lock would hit the
		// same race; use senderWg.Go for a tracked blocking send that is
		// synchronised with the shutdown path (senderWg.Wait before close).
		done := svc.Done()
		if svc.IsRunning() {
			svc.Stop()
		}
		// Use senderWg.Go so the sender is tracked by the shutdown path just
		// like the reap-loop and check-failure restart senders. This allows a
		// blocking send without risk of panicking on a closed channel: run.go
		// waits for senderWg to drain before closing restartCh.
		d.senderWg.Go(func() {
			d.restartCh <- restartReq{svc: svc, done: done, delay: 0}
		})
		d.mu.Unlock()
		return fmt.Sprintf("%s: restart scheduled", name), nil
	}
	ctrlServer.ReloadFn = func() (string, error) {
		return d.reload()
	}
	ctrlServer.LogsFn = func(name string, follow bool) ([][]byte, <-chan []byte, func(), error) {
		d.mu.RLock()
		svc, ok := d.services[name]
		d.mu.RUnlock()
		if !ok {
			return nil, nil, nil, fmt.Errorf("unknown service %q", name)
		}
		// Merge recent lines from both stdout and stderr (stdout first).
		recent := append(svc.Stdout.Recent(), svc.Stderr.Recent()...)
		if !follow {
			return recent, nil, nil, nil
		}
		// Fan-in stdout and stderr subscription channels so the caller
		// receives lines from both streams. A stop channel lets unsub()
		// unblock pipe goroutines that are waiting to send to merged,
		// preventing goroutine leaks when the client disconnects.
		outCh, outUnsub := svc.Stdout.Subscribe()
		errCh, errUnsub := svc.Stderr.Subscribe()
		merged := make(chan []byte, 256)
		stop := make(chan struct{})
		go func() {
			defer close(merged)
			var wg sync.WaitGroup
			wg.Add(2)
			pipe := func(src <-chan []byte) {
				defer wg.Done()
				for {
					select {
					case b, ok := <-src:
						if !ok {
							return
						}
						select {
						case merged <- b:
						case <-stop:
							return
						}
					case <-stop:
						return
					}
				}
			}
			go pipe(outCh)
			go pipe(errCh)
			wg.Wait()
		}()
		unsub := func() {
			close(stop)
			outUnsub()
			errUnsub()
		}
		return recent, merged, unsub, nil
	}
	return ctrlServer
}

// processConfigChanged reports whether the fields of p that affect the running
// process differ between old and new. Fields that only affect restart policy or
// metadata are intentionally excluded since they do not require a process restart:
//   - on-success, on-failure, on-check-failure, requires: updated in-place on
//     the preserved service wrapper by reload() under d.mu; no restart needed
//   - backoff-*: applied at next restart, no restart needed
//   - startup-timeout: only relevant during initial start sequencing
//
// ReadyCheck, ReadyTimeout, and KillDelay ARE included: they affect how the
// process is started (readiness gating) and stopped (kill delay), so a config
// change should cause the service to restart with the new values.
// Startup IS included: changing "oneshot" vs normal vs "disabled" changes the
// process lifecycle model and must take effect immediately.
func processConfigChanged(oldp, newp service.Process) bool {
	if oldp.Prefix != newp.Prefix {
		return true
	}
	if oldp.Startup != newp.Startup {
		return true
	}
	if oldp.Command != newp.Command {
		return true
	}
	if !slices.Equal(oldp.Args, newp.Args) {
		return true
	}
	if oldp.User != newp.User || oldp.Group != newp.Group {
		return true
	}
	if intPtrDiffers(oldp.UserID, newp.UserID) || intPtrDiffers(oldp.GroupID, newp.GroupID) {
		return true
	}
	if oldp.WorkingDir != newp.WorkingDir {
		return true
	}
	if oldp.StopSignal != newp.StopSignal {
		return true
	}
	if boolPtrDiffers(oldp.PassEnv, newp.PassEnv) {
		return true
	}
	if !maps.Equal(oldp.Environment, newp.Environment) {
		return true
	}
	if !slices.Equal(oldp.RemoveEnv, newp.RemoveEnv) {
		return true
	}
	if oldp.DotEnv != newp.DotEnv {
		return true
	}
	if oldp.ReadyCheck != newp.ReadyCheck {
		return true
	}
	if oldp.ReadyTimeout != newp.ReadyTimeout {
		return true
	}
	if oldp.KillDelay != newp.KillDelay {
		return true
	}
	// Flipping SDNotify changes the spawn env (adds/removes NOTIFY_SOCKET)
	// and changes whether dependent startup gates on READY=1; both require
	// the child to be restarted with the new behaviour.
	if oldp.SDNotify != newp.SDNotify {
		return true
	}
	if oldp.SDNotifyTimeout != newp.SDNotifyTimeout {
		return true
	}
	// Parent-death signal is applied via SysProcAttr at fork time, so a
	// change only takes effect on the next spawn — require a restart.
	if oldp.ParentDeathSignal != newp.ParentDeathSignal {
		return true
	}
	// Exit-code-map and signal-rewrite are consulted at runtime against the
	// live Process, so a change does NOT require a restart — updating the
	// struct in-place is enough. These checks exist to surface the fact
	// that the reload handler updates those fields on the preserved
	// service wrapper.
	return false
}

func intPtrDiffers(a, b *int) bool {
	if (a == nil) != (b == nil) {
		return true
	}
	return a != nil && *a != *b
}

func boolPtrDiffers(a, b *bool) bool {
	if (a == nil) != (b == nil) {
		return true
	}
	return a != nil && *a != *b
}
