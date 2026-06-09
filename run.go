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
	"context"
	"fmt"
	"log"
	"os"
	goSignal "os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/haproxytech/gopherd/check"
	"github.com/haproxytech/gopherd/control"
	"github.com/haproxytech/gopherd/internal/metrics"
	"github.com/haproxytech/gopherd/internal/order"
	"github.com/haproxytech/gopherd/internal/yml"
	"github.com/haproxytech/gopherd/service"
	"github.com/haproxytech/gopherd/version"
)

// run is the main daemon entry point. It returns the exit code.
func run(entrypointArgs []string) int {
	configPath := defaultConfigPath
	if v := os.Getenv("GOPHERD_CONFIG"); v != "" {
		configPath = v
	}

	data, err := readConfigFile(configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	cfg, err := yml.Unmarshal(data)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// GOPHERD_SOCKET overrides the configured control socket so a deployment
	// can relocate it (e.g. to a writable path when running rootless) without
	// editing the config. The client already honors this var.
	if v := os.Getenv("GOPHERD_SOCKET"); v != "" {
		cfg.Control.SocketPath = v
	}

	if !cfg.NoLogo {
		fmt.Print(version.Logo)
	}
	log.Printf("%s (built from %s)", version.Version, version.Repo)

	// When configured, mark ourselves as the child subreaper so orphaned
	// descendants are re-parented to us rather than to the real PID 1.
	// Intentionally non-fatal: a kernel/config that rejects the prctl (e.g.
	// unsupported platform, LSM denial) must not crash PID 1. Log and move
	// on; orphans will just be reaped by PID 1 instead, same as today.
	if cfg.Subreaper {
		if err := service.SetChildSubreaper(); err != nil {
			log.Printf("warning: subreaper: %v", err)
		} else {
			log.Printf("subreaper: enabled (orphans re-parent to gopherd)")
		}
	}

	// Check if another daemon is already running by probing the control socket.
	socketPath := cfg.Control.SocketPath
	if socketPath == "" {
		socketPath = control.DefaultSocketPath
	}
	if control.IsAlive(socketPath) {
		log.Printf("another gopherd instance is already running (socket %s is active)", socketPath)
		fmt.Fprintf(os.Stderr, "\navailable client commands:\n")
		fmt.Fprintf(os.Stderr, "  gopherd <service> <start|stop|restart|status>\n")
		fmt.Fprintf(os.Stderr, "  gopherd signal <service> <signal-name>\n")
		fmt.Fprintf(os.Stderr, "  gopherd logs <service> [-f]\n")
		fmt.Fprintf(os.Stderr, "  gopherd reload\n")
		fmt.Fprintf(os.Stderr, "  gopherd status                 # overview of all services and checks\n")
		fmt.Fprintf(os.Stderr, "  gopherd version\n")
		fmt.Fprintf(os.Stderr, "  gopherd tag\n")
		fmt.Fprintf(os.Stderr, "\ncurrent status:\n")
		os.Setenv("GOPHERD_SOCKET", socketPath)
		control.RunClient([]string{"status"})
		return 1
	}

	// Validate use-entrypoint-args: at most one service may set it.
	var entrypointCount int
	for _, p := range cfg.Processes {
		if p.UseEntrypointArgs {
			entrypointCount++
		}
	}
	if entrypointCount > 1 {
		log.Fatalf("only one process may set use-entrypoint-args: true")
	}
	// Warn if the user passed entrypoint args but no process is configured to
	// consume them — the args are otherwise silently discarded, which is a
	// common misconfiguration when migrating a container's CMD to gopherd.
	if entrypointCount == 0 && len(entrypointArgs) > 0 {
		log.Printf("warning: entrypoint args %q will be discarded because no process sets use-entrypoint-args: true", entrypointArgs)
	}

	d := &daemon{
		configPath:     configPath,
		cfg:            cfg,
		entrypointArgs: entrypointArgs,
		pidMap:         make(map[int]*service.Service),
		restartCh:      make(chan restartReq, 64),
		restartPending: make(map[string]bool),
		shutdownCh:     make(chan struct{}),
		childStarted:   make(chan struct{}, 1),
	}

	// Initialize stats tracking.
	d.m = metrics.New()

	// Compute start order from dependencies.
	startOrd, err := d.startOrder()
	if err != nil {
		log.Fatalf("dependencies: %v", err)
	}
	// Layers group independent services so oneshots in the same layer run in
	// parallel. Cycles already fail above; this can't surface a new error.
	startLayers, err := order.TopoLayers(buildOrderServices(cfg))
	if err != nil {
		log.Fatalf("dependencies: %v", err)
	}

	// Store start order; stopAll() derives the actual sequence from shutdownMode.
	d.shutdownSeq = startOrd
	d.shutdownMode = cfg.ShutdownOrder

	// Build log targets and services.
	d.buildLogTargets()
	if err := d.buildServices(); err != nil {
		log.Fatalf("config: %v", err)
	}

	// Start the control socket BEFORE bringing services up so that monitoring
	// tools and `gopherd status` can observe the daemon while services are
	// still being launched. Services not yet started by the layer loop appear
	// as "pending" in stats output.
	ctrlServer := d.setupControl()
	if err := ctrlServer.Start(); err != nil {
		log.Printf("warning: control socket: %v", err)
	} else {
		log.Printf("control socket: %s", ctrlServer.SocketPath)
	}

	d.startServiceLayers(cfg, startLayers)

	// Start health checks.
	d.startChecks()

	// Signals that trigger gopherd's graceful shutdown. Driven entirely
	// by `init-stop-signal` in config (defaults to {SIGTERM, SIGINT}).
	shutdownSet := cfg.ShutdownSignals()
	shutdownSigs := make([]syscall.Signal, 0, len(shutdownSet))
	for sig := range shutdownSet {
		shutdownSigs = append(shutdownSigs, sig)
	}
	if len(cfg.InitStopSignal) > 0 {
		log.Printf("init-stop-signal: %s", formatSignalSet(shutdownSigs))
	}

	// Forward signals to all children. SIGHUP triggers reload.
	sigs := make(chan os.Signal, 16)
	// Subscribe to the shutdown set plus the signals that always have a
	// special meaning inside gopherd (SIGHUP for reload) and those a
	// service may opt into forwarding (SIGUSR1, SIGUSR2).
	subscribed := make(map[syscall.Signal]bool)
	notify := func(sig syscall.Signal) {
		if subscribed[sig] {
			return
		}
		subscribed[sig] = true
		goSignal.Notify(sigs, sig)
	}
	for _, sig := range shutdownSigs {
		notify(sig)
	}
	notify(syscall.SIGHUP)
	notify(syscall.SIGUSR1)
	notify(syscall.SIGUSR2)
	go func() {
		for sig := range sigs {
			sysSig, ok := sig.(syscall.Signal)
			if !ok {
				continue
			}
			switch {
			case shutdownSet[sysSig]:
				// initiateShutdown is idempotent (CAS) and acquires d.mu itself.
				d.initiateShutdown(0)
			case sysSig == syscall.SIGHUP:
				msg, err := d.reload()
				if err != nil {
					log.Printf("reload failed: %v", err)
				} else {
					log.Printf("%s", msg)
				}
			default:
				// Signal forwarding is opt-in per service via the
				// signal-rewrite map. Services that do not list the
				// received signal are not forwarded to — this is the
				// documented behaviour change from the pre-3d model
				// that blasted every caught signal to every service.
				d.mu.Lock()
				for _, svc := range d.services {
					rewritten, ok := svc.RewriteSignal(sysSig)
					if !ok {
						continue
					}
					svc.Signal(rewritten)
				}
				d.mu.Unlock()
			}
		}
	}()

	// Handle restart requests from the reap loop.
	go func() {
		for req := range d.restartCh {
			d.handleRestartReq(req)
			// Decrement after the request is fully processed (started, skipped,
			// or failed) so the reap loop only treats ECHILD as transient while
			// a restart could still fork a new child.
			d.pendingRestarts.Add(-1)
		}
	}()

	// Subreaper is applied once at startup and never changes on reload; capture
	// it so the reap loop's idle path reads no shared (reloadable) config.
	subreaper := cfg.Subreaper

	// Single reap loop: handles managed children and orphaned zombies.
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			if err == syscall.ECHILD {
				// Shutting down with no children left: the daemon is done.
				if d.shuttingDown.Load() {
					break
				}
				// A restart is in flight: the handler is about to fork the
				// replacement child. Sleep briefly and retry so a single-service
				// daemon survives stop/restart.
				if d.pendingRestarts.Load() > 0 {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				// No children and not shutting down: idle as a live supervisor
				// rather than exiting. Stopping the last service (control-socket
				// stop, ignored exit) must not take gopherd down — it stays up so
				// the service can be started again. Block until a new child is
				// forked or shutdown begins. As subreaper, also wake periodically
				// so reparented orphans are reaped promptly (without subreaper,
				// orphans go to the real PID 1, so no poll is needed).
				var poll <-chan time.Time
				var timer *time.Timer
				if subreaper {
					timer = time.NewTimer(time.Second)
					poll = timer.C
				}
				select {
				case <-d.childStarted:
				case <-d.shutdownCh:
				case <-poll:
				}
				if timer != nil {
					timer.Stop()
				}
				continue
			}
			break
		}
		if pid <= 0 {
			continue
		}

		code := waitStatusCode(ws)

		d.mu.Lock()
		svc, isManaged := d.pidMap[pid]
		if isManaged {
			delete(d.pidMap, pid)
			// MarkExited invalidates svc.Pid and svc.running atomically
			// before acquiring svc.mu, so concurrent Stop/Signal/killTimer
			// callers see a stale-pid guard trip and do not issue
			// syscall.Kill against a pid the kernel has just freed.
			runDuration := svc.MarkExited()
			// Apply the user-configured exit-code-map BEFORE the WasStopped
			// fallback so an explicit mapping (e.g. 143 -> 42) wins over the
			// implicit "intentional-stop-becomes-0" heuristic.
			if mapped := svc.RemapExitCode(code); mapped != code {
				log.Printf("%s exited (status %d, remapped to %d)", svc.Name, code, mapped)
				code = mapped
			} else {
				log.Printf("%s exited (status %d)", svc.Name, code)
			}
			// Compute effective code before recording metrics: intentional stops
			// (WasStopped) should record exit code 0, not a crash/failure code.
			effectiveCode := code
			if svc.WasStopped() && code > 128 {
				effectiveCode = 0
			}
			// Restart-driven exits (control restart, check-failure restart,
			// crash + ActionRestart) count as a single `restarts +1` event, not
			// also as `exits +1` / ok / fail. Plain stops and one-off exits
			// still record ServiceExited as usual. The flag is cleared here so a
			// crash-then-restart sequence later (without an enqueued restart)
			// counts normally.
			restartConsumesExit := d.takeRestartPending(svc.Name)

			if d.shuttingDown.Load() {
				if !restartConsumesExit {
					d.m.ServiceExited(svc.Name, effectiveCode)
				}
				// Use pidMap rather than d.services: services removed during a
				// reload are deleted from d.services immediately but stay in
				// pidMap until they actually exit. Checking only d.services
				// would miss those processes and break out of the reap loop
				// prematurely while they are still in their kill-delay window.
				anyRunning := len(d.pidMap) > 0
				d.mu.Unlock()
				if !anyRunning {
					break
				}
				continue
			}

			success := effectiveCode == 0
			var action service.ExitAction

			// An intentional Stop() (control-socket stop/restart, check-failure
			// restart, dependency-cascade stop) must not trigger OnSuccess/
			// OnFailure: with the default OnSuccess=ActionShutdown a
			// signal-killed service would otherwise take the whole daemon down.
			// Pending restart requests already sit on restartCh, so the service
			// will come back if one was enqueued.
			switch {
			case svc.WasStopped():
				action = service.ActionIgnore
			case svc.Oneshot:
				// Oneshot services triggered via control socket after startup
				// should not take shutdown actions — just ignore the exit.
				if success {
					action = service.ActionIgnore
				} else {
					// svc.Proc.OnFailure was pre-validated by yml.Unmarshal
					// (ValidateExitAction) and again by service.New, so a parse
					// error here is impossible in practice. If one did occur we
					// fall back to Ignore rather than propagating the error:
					// fatalling from the reap loop would crash PID 1.
					parsed, err := service.ParseExitAction(svc.Proc.OnFailure, service.ActionIgnore)
					if err != nil {
						log.Printf("warning: %s: %v; ignoring exit", svc.Name, err)
						parsed = service.ActionIgnore
					}
					action = parsed
				}
			case success:
				action = svc.OnSuccess
			default:
				action = svc.OnFailure
				for _, other := range d.services {
					if other.Requires[svc.Name] && other.IsRunning() {
						log.Printf("stopping %s: required service %s failed", other.Name, svc.Name)
						other.Stop()
					}
				}
			}

			// Record the exit unless it is part of a restart cycle (either an
			// already-enqueued restart or the ActionRestart branch we are about
			// to take below).
			if !restartConsumesExit && action != service.ActionRestart {
				d.m.ServiceExited(svc.Name, effectiveCode)
			}

			switch action {
			case service.ActionRestart:
				if runDuration >= svc.Backoff.Limit {
					svc.Backoff.Reset()
				}
				delay := svc.Backoff.Next()
				log.Printf("restarting %s in %s", svc.Name, delay)
				d.m.ServiceRestarted(svc.Name)
				// Capture the done channel before unlocking; at this point
				// MarkExited has already closed it, so svc.Done() returns
				// the closed channel for this specific exit event.
				exitDone := svc.Done()
				d.mu.Unlock()
				// Increment before the send so the reap loop sees the pending
				// restart on its very next Wait4 ECHILD check.
				d.pendingRestarts.Add(1)
				d.senderWg.Go(func() {
					d.restartCh <- restartReq{svc: svc, done: exitDone, delay: delay}
				})
				continue

			case service.ActionShutdown, service.ActionFailureShutdown:
				d.mu.Unlock()
				// Pre-store exit code so it is visible even if the reap loop
				// breaks (ECHILD) before the goroutine runs.
				d.exitCode.CompareAndSwap(0, int32(effectiveCode))
				// Run in a goroutine so the reap loop remains free to call
				// Wait4 and close done channels; a synchronous call would
				// deadlock when stopAll waits on <-done for other services.
				go d.initiateShutdown(effectiveCode)
				continue

			case service.ActionSuccessShutdown:
				d.mu.Unlock()
				// Exit code 0 is the default — no pre-store needed.
				go d.initiateShutdown(0)
				continue

			case service.ActionIgnore:
				log.Printf("%s: ignoring exit", svc.Name)
				d.mu.Unlock()
				continue
			}
		}
		d.mu.Unlock()

		if d.shuttingDown.Load() {
			d.mu.Lock()
			anyRunning := len(d.pidMap) > 0
			d.mu.Unlock()
			if !anyRunning {
				break
			}
		}
	}

	ctrlServer.Stop()
	d.stopChecks()
	// Stop delivering signals before final teardown so a late SIGHUP cannot
	// trigger d.reload() concurrent with closeLogTargets() touching d.services.
	// Closing the channel lets the signal-forwarding goroutine exit cleanly.
	goSignal.Stop(sigs)
	close(sigs)
	// All sources that write to restartCh (reap loop, check callbacks, control
	// socket RestartFn) have now stopped. Wait for any in-flight sender
	// goroutines, then close the channel so the restart goroutine's range loop
	// terminates naturally.
	d.senderWg.Wait()
	close(d.restartCh)
	d.closeLogTargets()

	return int(d.exitCode.Load())
}

// runLayerOneshots starts every enabled oneshot in `layer` in parallel and
// blocks until they have all exited (or timed out). On the first non-ignored
// failure, it waits for the other in-flight oneshots to settle and then
// log.Fatalfs — matching the original sequential semantics where the first
// failure aborts gopherd, but without leaving stragglers running.
//
// Safety invariant: this is called from the startup loop, which completes
// entirely before the main reap loop (Wait4(-1,...)) starts. Multiple
// concurrent specific-PID Wait4 goroutines are safe; only Wait4(-1,...) would
// race with them, and it has not started yet.
// startServiceLayers brings up every enabled service in topological order.
// Oneshots within a layer run concurrently via runLayerOneshots and the next
// layer waits for all of them to exit cleanly. Non-oneshot services within a
// layer also start concurrently — by definition they have no ordering edge
// between each other in this layer, and any per-service gating (ready-check
// before spawn, sd_notify after spawn) runs inside its own goroutine so a
// slow gate on one service doesn't stall its siblings.
func (d *daemon) startServiceLayers(cfg *yml.Config, startLayers [][]string) {
	for _, layer := range startLayers {
		runLayerOneshots(d, layer)
		d.startLayerNonOneshots(cfg, layer)
	}
}

// startLayerNonOneshots starts every enabled non-oneshot service in the layer
// concurrently and waits for the slowest one (ready-check + spawn + sd_notify)
// before returning.
func (d *daemon) startLayerNonOneshots(cfg *yml.Config, layer []string) {
	var wg sync.WaitGroup
	for _, name := range layer {
		svc, ok := d.services[name]
		if !ok {
			log.Printf("warning: service %s in start order but not found, skipping", name)
			continue
		}
		if !svc.Enabled {
			log.Printf("skipping disabled service %s", svc.Name)
			continue
		}
		if svc.Oneshot {
			continue
		}
		// A client connected via the control socket during a previous layer
		// could have already started this one; don't double-fork.
		if svc.IsRunning() {
			continue
		}
		wg.Add(1)
		go func(svc *service.Service) {
			defer wg.Done()
			d.startNonOneshot(cfg, svc)
		}(svc)
	}
	wg.Wait()
}

// startNonOneshot performs the full per-service gating sequence for one
// non-oneshot, long-running service: optional ready-check, fork+exec, then
// optional sd_notify wait. Any gate failure is fatal — the daemon cannot
// safely proceed past a failed dependency gate.
func (d *daemon) startNonOneshot(cfg *yml.Config, svc *service.Service) {
	if svc.Proc.ReadyCheck != "" {
		checkCfg, ok := cfg.Checks[svc.Proc.ReadyCheck]
		if !ok {
			log.Fatalf("%s: ready-check %q not found in [checks]", svc.Name, svc.Proc.ReadyCheck)
		}
		c, err := check.New(svc.Proc.ReadyCheck, checkCfg, nil, nil)
		if err != nil {
			log.Fatalf("%s: ready check: %v", svc.Name, err)
		}
		if checkCfg.Exec != nil {
			cred, credErr := service.ResolveCredential(svc.Proc.User, svc.Proc.Group, svc.Proc.UserID, svc.Proc.GroupID)
			if credErr != nil {
				log.Printf("warning: %s: ready-check credential: %v", svc.Name, credErr)
			} else if cred != nil {
				c.SetCredential(cred)
			}
		}
		readyTimeout := 60 * time.Second
		if svc.Proc.ReadyTimeout != "" {
			readyTimeout, err = time.ParseDuration(svc.Proc.ReadyTimeout)
			if err != nil {
				log.Fatalf("%s: invalid ready-timeout %q: %v", svc.Name, svc.Proc.ReadyTimeout, err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), readyTimeout)
		err = c.WaitReady(ctx)
		cancel()
		if err != nil {
			log.Fatalf("%s: ready-check %q did not pass within %s (ready-check runs before %s starts; it should poll a dependency already running, not the service itself)", svc.Name, svc.Proc.ReadyCheck, readyTimeout, svc.Name)
		}
		log.Printf("%s: ready (check %s passed)", svc.Name, svc.Proc.ReadyCheck)
	}

	if _, err := d.startService(svc); err != nil {
		log.Fatalf("start %s: %v", svc.Name, err)
	}

	if svc.Proc.SDNotify {
		sdNotifyTimeout := 60 * time.Second
		if svc.Proc.SDNotifyTimeout != "" {
			sdNotifyTimeout, _ = time.ParseDuration(svc.Proc.SDNotifyTimeout)
		}
		ctx, cancel := context.WithTimeout(context.Background(), sdNotifyTimeout)
		err := svc.WaitSDNotifyReady(ctx)
		cancel()
		if err != nil {
			log.Fatalf("%s: sd_notify readiness did not arrive within %s: %v", svc.Name, sdNotifyTimeout, err)
		}
		log.Printf("%s: ready (READY=1 received)", svc.Name)
	}
}

func runLayerOneshots(d *daemon, layer []string) {
	type started struct {
		svc *service.Service
		pid int
	}
	var procs []started
	for _, name := range layer {
		svc, ok := d.services[name]
		if !ok || !svc.Enabled || !svc.Oneshot {
			continue
		}
		pid, err := svc.Start()
		if err != nil {
			for _, p := range procs {
				if p.pid > 0 {
					syscall.Kill(-p.pid, syscall.SIGKILL)
				}
			}
			log.Fatalf("oneshot %s: %v", svc.Name, err)
		}
		log.Printf("started oneshot %s (pid %d)", svc.Name, pid)
		d.m.ServiceStarted(svc.Name, pid)
		procs = append(procs, started{svc: svc, pid: pid})
	}
	if len(procs) == 0 {
		return
	}

	type result struct {
		err  error
		name string
		pid  int
		code int
	}
	results := make(chan result, len(procs))
	for _, p := range procs {
		go func(pp started) {
			code, err := waitOneshot(pp.pid, pp.svc.Proc.StartupTimeout)
			if err != nil && pp.pid > 0 {
				// Timeout (or wait4 error) — make sure the process is gone.
				syscall.Kill(-pp.pid, syscall.SIGKILL)
			}
			results <- result{name: pp.svc.Name, pid: pp.pid, code: code, err: err}
		}(p)
	}

	var fatalErr *result
	for i := 0; i < len(procs); i++ {
		r := <-results
		svc := d.services[r.name]
		if r.err != nil {
			// Wait failed (typically startup-timeout). Record a non-zero exit
			// so stats reflect that the oneshot didn't complete cleanly, even
			// though log.Fatalf below will terminate the daemon shortly.
			d.m.ServiceExited(r.name, 1)
			if fatalErr == nil {
				rc := r
				fatalErr = &rc
			}
			continue
		}
		svc.MarkExited()
		// Apply exit-code-map before success/failure evaluation, matching the
		// reap loop (RemapExitCode) so a startup oneshot honors the same
		// mapping a long-running service would.
		code := svc.RemapExitCode(r.code)
		if code != r.code {
			log.Printf("oneshot %s exited (status %d, remapped to %d)", r.name, r.code, code)
		}
		d.m.ServiceExited(r.name, code)
		if code != 0 {
			if svc.OnFailure == service.ActionIgnore {
				log.Printf("oneshot %s exited with status %d (ignored)", r.name, code)
				continue
			}
			if fatalErr == nil {
				rc := r
				rc.code = code
				fatalErr = &rc
			}
			continue
		}
		log.Printf("oneshot %s completed", r.name)
	}
	if fatalErr != nil {
		if fatalErr.err != nil {
			log.Fatalf("oneshot %s: %v", fatalErr.name, fatalErr.err)
		}
		log.Fatalf("oneshot %s failed (status %d)", fatalErr.name, fatalErr.code)
	}
}

// waitOneshot waits for a oneshot process to exit, with an optional timeout.
// If timeoutStr is empty, it waits indefinitely. Returns the exit code or an
// error if the timeout is exceeded.
//
// Safety invariant: called only during the startup loop, before the main reap
// loop (Wait4(-1,...)) starts. Concurrent specific-PID Wait4 calls (one per
// goroutine, distinct PIDs) are safe with each other.
func waitOneshot(pid int, timeoutStr string) (int, error) {
	type waitResult struct {
		err  error
		code int
	}
	ch := make(chan waitResult, 1)
	go func() {
		var ws syscall.WaitStatus
		for {
			_, err := syscall.Wait4(pid, &ws, 0, nil)
			if err == syscall.EINTR {
				continue
			}
			if err != nil {
				ch <- waitResult{err: fmt.Errorf("wait4: %w", err)}
				return
			}
			break
		}
		ch <- waitResult{code: waitStatusCode(ws)}
	}()

	if timeoutStr == "" {
		r := <-ch
		return r.code, r.err
	}

	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return 0, fmt.Errorf("invalid startup-timeout %q: %w", timeoutStr, err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.code, r.err
	case <-timer.C:
		// Drain the goroutine so it does not leak after the caller kills the process.
		go func() { <-ch }()
		return 0, fmt.Errorf("timed out after %s", timeout)
	}
}

// formatSignalSet renders a list of signals as a deterministic string
// ("SIGINT, SIGTERM") for log output. Sorted by numeric value so the
// output does not flap across runs.
func formatSignalSet(sigs []syscall.Signal) string {
	if len(sigs) == 0 {
		return "(none)"
	}
	sorted := make([]syscall.Signal, len(sigs))
	copy(sorted, sigs)
	slices.Sort(sorted)
	names := make([]string, len(sorted))
	for i, sig := range sorted {
		names[i] = service.SignalName(sig)
	}
	return strings.Join(names, ", ")
}

func waitStatusCode(ws syscall.WaitStatus) int {
	if ws.Exited() {
		return ws.ExitStatus()
	}
	if ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}
