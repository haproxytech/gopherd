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
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/haproxytech/gopherd/check"
	"github.com/haproxytech/gopherd/control"
	"github.com/haproxytech/gopherd/internal/logger"
	"github.com/haproxytech/gopherd/internal/metrics"
	"github.com/haproxytech/gopherd/internal/order"
	"github.com/haproxytech/gopherd/internal/yml"
	"github.com/haproxytech/gopherd/service"
)

// maxConfigFileSize caps readConfigFile's read so a misconfigured or swapped-out
// huge config file cannot OOM-kill PID 1. 4 MiB is generous for real configs.
const maxConfigFileSize = 4 << 20

// readConfigFile opens path, verifies it is not a symlink and passes permission
// checks, then returns its contents. All checks run on the open file descriptor
// to eliminate the TOCTOU window of a separate Lstat+Stat preceding ReadFile.
//
// Unlike dotenv/{{file}} refs, this guards only the leaf (O_NOFOLLOW) and does
// not walk ancestors: the operator-supplied config path (GOPHERD_CONFIG/default)
// often sits under a system symlink like /var/run that an ancestor walk would
// wrongly reject.
func readConfigFile(path string) ([]byte, error) {
	// O_NOFOLLOW fails with ELOOP if path is a symlink, making the symlink
	// check atomic with the open (no separate Lstat needed).
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if err == syscall.ELOOP {
			return nil, fmt.Errorf("config %s is a symlink; refusing to open", path)
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()

	// fstat(2) on the open fd: permission checks hit the same inode we read,
	// closing the TOCTOU window between check and read.
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// Reject non-regular files: a FIFO/pipe would make io.ReadAll block forever,
	// and a char/block device would return unexpected bytes.
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

	// Read cap+1 so reading the extra byte distinguishes "at the cap" from
	// "overflowed".
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

// errServiceReplaced is returned by startService when the service is no longer
// the current instance in d.services (replaced or removed by a reload). The
// restart goroutine must silently skip it, not treat it as fatal.
var errServiceReplaced = fmt.Errorf("service replaced or removed by reload")

// errAlreadyRunning is returned (with the running PID) when concurrent callers
// race past their pre-call IsRunning() check into startService; the loser bails
// rather than double-forking. Callers must not treat it as fatal.
var errAlreadyRunning = fmt.Errorf("service already running")

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

	// stopAllDone is closed by initiateShutdown after stopAll returns. The
	// reap loop waits on it before teardown so the per-service SIGKILL
	// escalation (WaitGroupExit) finishes before gopherd exits — otherwise
	// group members that outlive their leader would leak to the host init.
	stopAllDone chan struct{}

	// childStarted wakes the reap loop from its idle wait when a new child is
	// forked. Buffered so startService never blocks; a non-blocking send suffices
	// since the reap loop re-checks via Wait4 once woken.
	childStarted chan struct{}

	// restartPending marks services whose next observed exit is part of a restart
	// cycle, so the reap loop suppresses the ServiceExited metric (a restart
	// counts as restarts+1 only, not also exits+1). Guarded by d.mu.
	restartPending map[string]bool

	configPath string
	// controlSocket is the socket path bound at startup (config + GOPHERD_SOCKET
	// override, defaulted). Injected into children; stable across reloads since
	// the socket itself is bind-once.
	controlSocket  string
	shutdownMode   string // "reverse-dep" (default), "dep", "simultaneous"
	checkers       []*check.Checker
	logTargets     []*logger.Target
	entrypointArgs []string
	shutdownSeq    []string // start order; used to derive shutdown sequence

	// senderWg tracks fire-and-forget goroutines that write to restartCh.
	// It must reach zero before restartCh is closed.
	senderWg sync.WaitGroup

	mu sync.RWMutex
	// reloadMu serialises concurrent hot-reloads so two rapid SIGHUPs cannot race
	// on d.checkers, d.cfg, or d.services.
	//
	// Lock ordering: only reload() acquires reloadMu, and always BEFORE mu. All
	// other paths take only mu, so this one-way ordering makes deadlock impossible.
	reloadMu sync.Mutex

	// pendingRestarts counts restart requests in flight from enqueue until the
	// handler is done. On Wait4 ECHILD the reap loop checks this: a pending
	// restart means the empty-children state is transient (a fork is imminent),
	// so the loop must not exit.
	pendingRestarts atomic.Int32

	// Accessed from multiple goroutines; atomic so they can be read without mu.
	exitCode     atomic.Int32
	shuttingDown atomic.Bool

	// started gates reload(): the startup loop reads d.services without mu, so a
	// concurrent reload before this is set would panic PID 1.
	started atomic.Bool
}

type restartReq struct {
	svc *service.Service
	// done is svc.Done() captured at enqueue time. The handler waits on this
	// rather than calling req.svc.Done() later, which would race a concurrent
	// restart that has already re-created the done channel.
	done  <-chan struct{}
	delay time.Duration
}

// markRestartPending records that a restart was just enqueued for svc, so the
// reap loop suppresses the ServiceExited metric for the matching exit.
// Callers must hold d.mu.
func (d *daemon) markRestartPending(name string) {
	if d.restartPending == nil {
		d.restartPending = make(map[string]bool)
	}
	d.restartPending[name] = true
}

// takeRestartPending atomically reads and clears the restart-pending flag for
// svc. Returns true if the next ServiceExited call should be suppressed.
// Callers must hold d.mu.
func (d *daemon) takeRestartPending(name string) bool {
	if !d.restartPending[name] {
		return false
	}
	delete(d.restartPending, name)
	return true
}

// handleRestartReq processes one entry from restartCh: wait for the prior exit
// to be observed, optionally sleep the backoff delay, then re-start the service.
// The caller must decrement pendingRestarts after this returns so the reap
// loop's ECHILD bookkeeping stays accurate.
func (d *daemon) handleRestartReq(req restartReq) {
	// Wait for the specific exit that triggered this restart (see restartReq.done).
	<-req.done
	if req.svc.IsRunning() {
		return
	}
	// Wake early on shutdown so a long backoff cannot stall daemon exit.
	// NewTimer (not time.After) so the timer goroutine is reclaimed on early wake.
	if req.delay > 0 {
		timer := time.NewTimer(req.delay)
		select {
		case <-timer.C:
		case <-d.shutdownCh:
			timer.Stop()
		}
	}
	// errServiceReplaced (reload) and errAlreadyRunning (lost start race) are benign.
	if _, err := d.startService(req.svc); err != nil {
		if err != errShuttingDown && err != errServiceReplaced && err != errAlreadyRunning {
			log.Printf("restart %s failed: %v", req.svc.Name, err)
			d.initiateShutdown(1)
		}
	}
}

func (d *daemon) startService(svc *service.Service) (int, error) {
	// Prepare env/credentials/templates OFF d.mu: ResolveCredential can block on
	// NSS (LDAP/SSSD) and dotenv hits disk; under d.mu that would stall the reap
	// loop, control handlers, and shutdown. Lock-free is safe because a running
	// instance's svc.Proc is immutable (config changes fork a new instance).
	plan, err := svc.PrepareStart()
	if err != nil {
		return 0, err
	}
	// Hold mu across the shuttingDown check, fork/exec, and pidMap write: the
	// reap loop can't observe the exit before the PID is recorded, and a
	// concurrent initiateShutdown can't slip stopAll() in between (which would
	// start a process that never gets a stop signal).
	d.mu.Lock()
	if d.shuttingDown.Load() {
		d.mu.Unlock()
		return 0, errShuttingDown
	}
	// Reject stale restart requests: a reload may have replaced or removed this
	// service since enqueue, and starting the old instance would briefly run two
	// copies.
	if current, ok := d.services[svc.Name]; !ok || current != svc {
		d.mu.Unlock()
		return 0, errServiceReplaced
	}
	// Recheck under d.mu: callers test IsRunning() unlocked, so concurrent starts
	// can both reach here. The loser bails rather than double-forking (which would
	// orphan the first PID in pidMap and leak its done channel).
	if svc.IsRunning() {
		pid := int(svc.Pid.Load())
		d.mu.Unlock()
		return pid, errAlreadyRunning
	}
	pid, err := svc.FinishStart(plan)
	if err != nil {
		d.mu.Unlock()
		return 0, err
	}
	d.pidMap[pid] = svc
	// Record ServiceStarted under d.mu so a fast-exiting child cannot be reaped
	// (calling ServiceExited) before the start is recorded, which would
	// transiently show exits > starts in "stats".
	d.m.ServiceStarted(svc.Name, pid)
	d.mu.Unlock()
	// Wake the reap loop if it is idling with no children. Non-blocking: a full
	// buffer already means a wake is pending.
	select {
	case d.childStarted <- struct{}{}:
	default:
	}
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
// Callers must hold d.mu on entry. It releases d.mu while waiting for each exit
// so the reap loop can call MarkExited (which closes the done channel) without
// deadlocking.
func (d *daemon) stopSequential(seq []string) {
	stopped := make(map[string]bool)
	for _, name := range seq {
		svc, ok := d.services[name]
		if !ok || !svc.IsRunning() {
			continue
		}
		// Capture done before Stop so the close isn't missed if the process exits
		// before the receive below.
		done := svc.Done()
		svc.Stop()
		stopped[name] = true
		d.mu.Unlock()
		<-done
		// Leader is reaped; give the SIGKILL escalation time to catch group
		// members that survived the stop signal, so they don't leak past
		// gopherd's own exit.
		svc.WaitGroupExit()
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
		svc.WaitGroupExit()
		d.mu.Lock()
	}
}

// stopSimultaneous stops all services concurrently, then waits for every
// process to fully exit.
//
// Callers must hold d.mu on entry; like stopSequential it releases d.mu while
// waiting so the reap loop can call MarkExited without deadlocking.
func (d *daemon) stopSimultaneous() {
	// Collect done channels and signal stops under d.mu, then release before waiting.
	dones := make([]<-chan struct{}, 0, len(d.services))
	svcs := make([]*service.Service, 0, len(d.services))
	for _, svc := range d.services {
		if svc.IsRunning() {
			dones = append(dones, svc.Done())
			svcs = append(svcs, svc)
			svc.Stop()
		}
	}
	if len(dones) == 0 {
		return
	}
	d.mu.Unlock()
	for i, done := range dones {
		<-done
		// Leader is reaped; let the SIGKILL escalation catch surviving group
		// members before gopherd exits.
		svcs[i].WaitGroupExit()
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
	// Broadcast to select-waiting goroutines (e.g. the restart backoff sleeper)
	// so they exit without waiting out their delay.
	close(d.shutdownCh)
	d.exitCode.Store(int32(code))
	d.mu.Lock()
	d.stopAll()
	d.mu.Unlock()
	close(d.stopAllDone)
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
			// Enqueue a restart directly, bypassing the reap loop's exit-action
			// evaluation; otherwise restart would require on-success/on-failure:
			// restart too, which the user did not request.
			// Capture done before Stop so the handler waits on this specific exit
			// (see restartReq.done). Use senderWg.Go (tracked blocking send) so the
			// restart is never silently dropped: a drop plus default
			// OnSuccess=ActionShutdown would shut the daemon down when the stopped
			// service exits with effectiveCode=0.
			done := svc.Done()
			// Guard on IsRunning() (like RestartFn): marking a stopped service's
			// restart pending leaves the flag set with no matching exit, which then
			// suppresses ServiceExited on its next genuine crash.
			if svc.IsRunning() {
				svc.Stop()
				d.markRestartPending(svc.Name)
			}
			d.m.ServiceRestarted(svc.Name)
			d.pendingRestarts.Add(1)
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
	controlSocket := d.controlSocket
	if controlSocket == "" {
		controlSocket = control.DefaultSocketPath
	}
	for _, p := range d.cfg.Processes {
		if p.UseEntrypointArgs && len(d.entrypointArgs) > 0 {
			p.Args = append(p.Args, d.entrypointArgs...)
		}
		// Resolve the effective prefix into p.Prefix so processConfigChanged can
		// detect global prefix changes on reload, not just per-service ones.
		if p.Prefix == "" {
			p.Prefix = d.cfg.Prefix
		}
		svc, err := service.New(p, d.cfg.Prefix)
		if err != nil {
			return err
		}
		svc.ControlSocket = controlSocket
		d.services[svc.Name] = svc
		d.m.RegisterService(svc.Name, svc.Enabled)
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
	// Map each check to the service referencing it (via on-check-failure or
	// ready-check) so exec checks run as that service's user instead of root.
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
			cred, err := service.ResolveCredential(svc.Proc.User, svc.Proc.Group, svc.Proc.UserID, svc.Proc.GroupID, svc.Proc.StrictGroups)
			if err != nil {
				log.Printf("warning: check %s: resolve credential: %v", name, err)
			} else if cred != nil {
				c.SetCredential(cred)
			}
		}
		d.checkers = append(d.checkers, c)
		d.m.RegisterCheck(name)
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
	// Refuse until startup finishes; see the started field.
	if !d.started.Load() {
		return "", fmt.Errorf("reload blocked: daemon still starting up")
	}

	// Serialise concurrent reloads; see reloadMu.
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

	// Enforce use-entrypoint-args uniqueness, matching the startup check in run(),
	// so a hot-reload can't install a config that startup would have rejected.
	var entrypointCount int
	for _, p := range newCfg.Processes {
		if p.UseEntrypointArgs {
			entrypointCount++
		}
	}
	if entrypointCount > 1 {
		return "", fmt.Errorf("reload blocked: only one process may set use-entrypoint-args: true")
	}

	// Pre-validate the dependency graph before touching any state so a bad config
	// (e.g. a cycle) fails fast without leaving a half-reconciled daemon.
	startOrd, err := order.TopoSort(buildOrderServices(newCfg))
	if err != nil {
		return "", fmt.Errorf("reload dependencies: %w", err)
	}

	// Dry-run service.New (the authoritative validator) on every process before
	// mutating state, so buildServices below cannot fail mid-reload after
	// services have already been stopped and removed.
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

	newNames := make(map[string]bool)
	for _, p := range newCfg.Processes {
		name := p.Name
		if name == "" {
			name = p.Command
		}
		newNames[name] = true
	}

	// Snapshot before any mutation so the reconcile loop below still sees every
	// service running before this reload, including ones about to be removed.
	oldServices := maps.Clone(d.services)

	// Stop and remove services no longer in config. Set exit actions to
	// ActionIgnore first so the reap loop treats the exit as benign. OnSuccess/
	// OnFailure are plain fields read by the reap loop under d.mu, which is held
	// here.
	for name, svc := range d.services {
		if !newNames[name] {
			log.Printf("reload: removing service %s", name)
			svc.OnSuccess = service.ActionIgnore
			svc.OnFailure = service.ActionIgnore
			if svc.IsRunning() {
				svc.Stop()
			}
			// Disconnect from log targets before removal so the later ClearTargets
			// loop (which iterates d.services) doesn't miss this service.
			svc.Stdout.ClearTargets()
			svc.Stderr.ClearTargets()
			delete(d.services, name)
		}
	}

	d.stopChecks()

	// Reconcile log targets: disconnect current services, close old targets, then
	// rebuild from new config. buildServices() wires new wrappers automatically;
	// preserved services are re-wired explicitly below.
	for _, svc := range d.services {
		svc.Stdout.ClearTargets()
		svc.Stderr.ClearTargets()
	}
	for _, lt := range d.logTargets {
		lt.Close()
	}
	d.logTargets = nil

	// The shutdown signal set and control socket are bound once at startup and
	// can't be safely re-applied by a reload, so warn rather than silently ignore
	// a change. (shutdown-order and per-service policy ARE re-read below.)
	if !slices.Equal(d.cfg.InitStopSignal, newCfg.InitStopSignal) {
		log.Printf("reload: init-stop-signal change ignored (takes effect only on restart)")
	}
	if d.cfg.Control != newCfg.Control {
		log.Printf("reload: control socket change ignored (takes effect only on restart)")
	}

	// Snapshot old check names to unregister metrics for checks this reload drops;
	// startChecks re-registers the survivors below.
	oldCheckNames := make([]string, 0, len(d.cfg.Checks))
	for name := range d.cfg.Checks {
		oldCheckNames = append(oldCheckNames, name)
	}

	d.cfg = newCfg
	d.buildLogTargets()
	// Pre-validated above; on the off chance it still fails, unlock and abort
	// rather than crashing PID 1.
	if err := d.buildServices(); err != nil {
		d.mu.Unlock()
		return "", fmt.Errorf("reload: %w", err)
	}

	// Drop metrics for removed services so stale counters don't linger in stats;
	// surviving services were just re-registered by buildServices().
	for name := range oldServices {
		if _, exists := d.services[name]; !exists {
			d.m.UnregisterService(name)
		}
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
			// Config unchanged — keep the running instance; re-wire its writers
			// to the new log targets.
			for _, lt := range d.logTargets {
				if lt.AppliesTo(oldSvc.Name) {
					oldSvc.Stdout.AddTarget(lt.Writer)
					oldSvc.Stderr.AddTarget(lt.Writer)
				}
			}
			// Update policy fields in-place so a reload tuning them takes effect
			// without a restart. All are read by the reap loop / check callbacks
			// under d.mu, held here, so this is race-free.
			oldSvc.OnSuccess = newSvc.OnSuccess
			oldSvc.OnFailure = newSvc.OnFailure
			oldSvc.OnCheckFailure = newSvc.OnCheckFailure
			oldSvc.Requires = newSvc.Requires
			oldSvc.Proc.ExitCodeMap = newSvc.Proc.ExitCodeMap
			oldSvc.Proc.SignalRewrite = newSvc.Proc.SignalRewrite
			d.services[name] = oldSvc
		} else if oldSvc.IsRunning() {
			// Config changed — stop old instance, ignoring its exit so the reap
			// loop doesn't restart or shut down while we start the new one.
			log.Printf("reload: restarting changed service %s", name)
			oldSvc.OnSuccess = service.ActionIgnore
			oldSvc.OnFailure = service.ActionIgnore
			waitForOld = append(waitForOld, oldSvc.Done())
			oldSvc.Stop()
		}
	}

	d.shutdownSeq = startOrd
	d.shutdownMode = newCfg.ShutdownOrder

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
			continue // preserved from old config
		}
		toStart = append(toStart, svc)
	}

	// Release the lock before waiting/starting: startService acquires d.mu, and
	// the reap loop needs it to call MarkExited (closing the done channels below).
	d.mu.Unlock()

	// Wait for stopped old instances to exit before starting replacements, else
	// two instances could run simultaneously during the reload window.
	for _, done := range waitForOld {
		<-done
	}

	for _, svc := range toStart {
		if _, err := d.startService(svc); err != nil && err != errShuttingDown && err != errServiceReplaced && err != errAlreadyRunning {
			log.Printf("reload: start %s failed: %v", svc.Name, err)
		}
	}

	// Restart checks with new config under the lock to avoid racing on
	// d.checkers or d.cfg.
	d.mu.Lock()
	d.startChecks()
	// Drop metrics for checks this reload removed (survivors just re-registered).
	for _, name := range oldCheckNames {
		if _, ok := d.cfg.Checks[name]; !ok {
			d.m.UnregisterCheck(name)
		}
	}
	d.mu.Unlock()

	return "reload: ok", nil
}

// setupControl wires all control-socket handler callbacks and starts the server.
func (d *daemon) setupControl() *control.Server {
	ctrlServer := control.NewServer(d.cfg.Control)
	ctrlServer.StatsFn = func() string {
		return d.m.Format()
	}
	ctrlServer.StatsJSONFn = func() string {
		buf, err := json.Marshal(d.m.Snapshot())
		if err != nil {
			return fmt.Sprintf("error: marshal: %v", err)
		}
		return string(buf)
	}
	ctrlServer.StatusJSONFn = func(name string) (string, error) {
		snap, ok := d.m.ServiceSnapshot(name)
		if !ok {
			return "", fmt.Errorf("unknown service %q", name)
		}
		buf, err := json.Marshal(snap)
		if err != nil {
			return "", fmt.Errorf("marshal: %w", err)
		}
		return string(buf), nil
	}
	ctrlServer.StatusFn = func(name string) (string, error) {
		d.mu.RLock()
		svc, ok := d.services[name]
		d.mu.RUnlock()
		if !ok {
			return "", fmt.Errorf("unknown service %q", name)
		}
		switch {
		case svc.IsRunning():
			return fmt.Sprintf("%s: running (pid %d)", name, int(svc.Pid.Load())), nil
		case !svc.Enabled:
			return fmt.Sprintf("%s: disabled", name), nil
		case d.m.IsPending(name):
			return fmt.Sprintf("%s: pending", name), nil
		default:
			return fmt.Sprintf("%s: stopped", name), nil
		}
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
			// Lost a start race: report running, not an error.
			if err == errAlreadyRunning {
				return fmt.Sprintf("%s: already running (pid %d)", name, pid), nil
			}
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
		// Refuse during shutdown so we never stop a service with no plan to
		// bring it back up.
		if d.shuttingDown.Load() {
			d.mu.Unlock()
			return "", fmt.Errorf("daemon is shutting down")
		}
		// Capture done under d.mu. Use senderWg.Go for a tracked blocking send,
		// synchronised with the shutdown path (run.go waits for senderWg before
		// closing restartCh) so the send never panics on a closed channel and
		// the service is never left stopped with no pending restart.
		done := svc.Done()
		if svc.IsRunning() {
			svc.Stop()
			d.markRestartPending(svc.Name)
		}
		d.m.ServiceRestarted(svc.Name)
		d.pendingRestarts.Add(1)
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
		if !svc.LogCapture {
			return nil, nil, nil, fmt.Errorf("log capture disabled for %q (set log-capture: true)", name)
		}
		// Merge recent lines from both streams (stdout first).
		recent := append(svc.Stdout.Recent(), svc.Stderr.Recent()...)
		if !follow {
			return recent, nil, nil, nil
		}
		// Fan-in stdout and stderr subscriptions. The stop channel lets unsub()
		// unblock pipe goroutines on client disconnect, preventing leaks.
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

// processConfigChanged reports whether fields that affect the running process
// differ between old and new, i.e. whether the service must be restarted.
// Excluded (applied without a restart): on-success, on-failure, on-check-failure,
// requires (updated in-place by reload()), backoff-* (used at next restart), and
// startup-timeout (only during initial start sequencing).
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
	// strict-groups is applied at fork time, so a change needs a restart.
	if oldp.StrictGroups != newp.StrictGroups {
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
	// log-capture picks the FD wiring at fork time, so a change needs a restart.
	if boolPtrDiffers(oldp.LogCapture, newp.LogCapture) {
		return true
	}
	// export-socket is applied to the env at fork time.
	if boolPtrDiffers(oldp.ExportSocket, newp.ExportSocket) {
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
	if oldp.DotEnvFollow != newp.DotEnvFollow {
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
	// SDNotify changes the spawn env (NOTIFY_SOCKET) and whether dependents gate
	// on READY=1, so a change needs a restart.
	if oldp.SDNotify != newp.SDNotify {
		return true
	}
	if oldp.SDNotifyTimeout != newp.SDNotifyTimeout {
		return true
	}
	// Parent-death signal is applied via SysProcAttr at fork time, so a change
	// needs a restart.
	if oldp.ParentDeathSignal != newp.ParentDeathSignal {
		return true
	}
	// Exit-code-map and signal-rewrite are consulted at runtime, so reload()
	// updates them in-place without a restart — no check needed here.
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
