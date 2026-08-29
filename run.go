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
	"github.com/haproxytech/gopherd/internal/reaper"
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
	// can relocate it (e.g. writable path when rootless) without editing config.
	if v := os.Getenv("GOPHERD_SOCKET"); v != "" {
		cfg.Control.SocketPath = v
	}

	// GOPHERD_NO_LOGO lets test harnesses suppress the banner without a config flag.
	if !cfg.NoLogo && os.Getenv("GOPHERD_NO_LOGO") == "" {
		fmt.Print(version.Logo)
	}
	log.Printf("%s (built from %s)", version.Version, version.Repo)

	// Mark ourselves as child subreaper so orphaned descendants re-parent to us
	// instead of the real PID 1. Non-fatal: if the prctl is rejected (unsupported
	// platform, LSM denial) PID 1 must not crash; orphans fall back to PID 1.
	if cfg.Subreaper {
		if err := service.SetChildSubreaper(); err != nil {
			log.Printf("warning: subreaper: %v", err)
		} else {
			log.Printf("subreaper: enabled (orphans re-parent to gopherd)")
		}
	}

	// Refuse to start if another daemon already owns the control socket.
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
		_ = os.Setenv("GOPHERD_SOCKET", socketPath)
		control.RunClient([]string{"status"})
		return 1
	}

	// At most one service may set use-entrypoint-args.
	var entrypointCount int
	for _, p := range cfg.Processes {
		if p.UseEntrypointArgs {
			entrypointCount++
		}
	}
	if entrypointCount > 1 {
		log.Fatalf("only one process may set use-entrypoint-args: true")
	}
	// Warn when entrypoint args are passed but no process consumes them,
	// otherwise they are silently discarded — a common migration mistake.
	if entrypointCount == 0 && len(entrypointArgs) > 0 {
		log.Printf("warning: entrypoint args %q will be discarded because no process sets use-entrypoint-args: true", entrypointArgs)
	}

	d := &daemon{
		configPath:     configPath,
		cfg:            cfg,
		controlSocket:  socketPath,
		entrypointArgs: entrypointArgs,
		pidMap:         make(map[int]*service.Service),
		restartCh:      make(chan restartReq, 64),
		restartPending: make(map[string]bool),
		shutdownCh:     make(chan struct{}),
		stopAllDone:    make(chan struct{}),
		childStarted:   make(chan struct{}, 1),
	}
	// Probe forks must wake an idling reap loop just like service forks:
	// without it, a childless daemon would leave probe children unreaped.
	d.reaper = reaper.New(d.wakeReapLoop)

	d.m = metrics.New()

	startOrd, err := d.startOrder()
	if err != nil {
		log.Fatalf("dependencies: %v", err)
	}
	// Layers group independent services so same-layer oneshots run in parallel.
	// Cycles already failed above, so this can't surface a new error.
	startLayers, err := order.TopoLayers(buildOrderServices(cfg))
	if err != nil {
		log.Fatalf("dependencies: %v", err)
	}

	// stopAll() derives the actual shutdown sequence from shutdownMode.
	d.shutdownSeq = startOrd
	d.shutdownMode = cfg.ShutdownOrder

	d.buildLogTargets()
	if err := d.buildServices(); err != nil {
		log.Fatalf("config: %v", err)
	}

	// Start the control socket BEFORE bringing services up so monitoring tools
	// and `gopherd status` can observe the daemon mid-launch; not-yet-started
	// services appear as "pending".
	ctrlServer := d.setupControl()
	if err := ctrlServer.Start(); err != nil {
		log.Printf("warning: control socket: %v", err)
	} else {
		log.Printf("control socket: %s", ctrlServer.SocketPath)
	}

	d.startServiceLayers(cfg, startLayers)

	d.startChecks()
	d.startSchedulers()

	// Startup reads of d.services are done; reload() may now proceed.
	d.started.Store(true)

	// Graceful-shutdown signals, driven by `init-stop-signal` (default {SIGTERM, SIGINT}).
	shutdownSet := cfg.ShutdownSignals()
	shutdownSigs := make([]syscall.Signal, 0, len(shutdownSet))
	for sig := range shutdownSet {
		shutdownSigs = append(shutdownSigs, sig)
	}
	if len(cfg.InitStopSignal) > 0 {
		log.Printf("init-stop-signal: %s", formatSignalSet(shutdownSigs))
	}

	// Subscribe to the shutdown set plus signals with special meaning: SIGHUP
	// (reload) and SIGUSR1/SIGUSR2 (opt-in per-service forwarding).
	sigs := make(chan os.Signal, 16)
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
				// Forwarding is opt-in per service via the signal-rewrite
				// map; services not listing the signal are skipped.
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

	// One goroutine per restart request so a slow backoff can't head-of-line
	// block other services' restarts, and restartCh stays drained (senderWg.Go
	// senders don't pile up behind the 64-deep buffer). Concurrent restarts are
	// safe: startService rechecks IsRunning() under d.mu, and each handler waits
	// on its own captured done channel.
	go func() {
		for req := range d.restartCh {
			go func(req restartReq) {
				d.handleRestartReq(req)
				// Decrement only after the request settles, so the reap loop keeps
				// treating ECHILD as transient while a fork may still be pending.
				d.pendingRestarts.Add(-1)
			}(req)
		}
	}()

	// Subreaper is applied once at startup and never changes on reload; capture
	// it so the reap loop's idle path reads no shared (reloadable) config.
	subreaper := cfg.Subreaper

	// Single reap loop: handles managed children and orphaned zombies. From
	// here on it owns Wait4(-1), so exec probes must take exit statuses from
	// the registry instead of waiting themselves.
	d.reaper.Activate()
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			if err == syscall.ECHILD {
				if d.shuttingDown.Load() {
					break
				}
				// A restart is in flight: wait for the handler to fork (childStarted)
				// instead of busy-polling Wait4 for the whole backoff. The fallback
				// timer re-checks in case the fork failed (pendingRestarts hit 0 with
				// no signal).
				if d.pendingRestarts.Load() > 0 {
					timer := time.NewTimer(250 * time.Millisecond)
					select {
					case <-d.childStarted:
					case <-d.shutdownCh:
					case <-timer.C:
					}
					timer.Stop()
					continue
				}
				// No children, not shutting down: idle as a live supervisor.
				// Stopping the last service must not take gopherd down — it stays
				// up so the service can be restarted. Block until a new child is
				// forked or shutdown begins. As subreaper, also poll so reparented
				// orphans are reaped promptly (otherwise they go to PID 1).
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
			// MarkExited atomically invalidates svc.Pid/svc.running before taking
			// svc.mu, so concurrent Stop/Signal callers trip the stale-pid guard
			// and never Kill a pid the kernel just freed. The kill timer stays
			// armed: it probes group liveness itself before escalating.
			runDuration := svc.MarkExited()
			// Apply exit-code-map BEFORE the WasStopped fallback so an explicit
			// mapping (e.g. 143 -> 42) wins over the implicit stop-becomes-0 rule.
			if mapped := svc.RemapExitCode(code); mapped != code {
				log.Printf("%s exited (status %d, remapped to %d)", svc.Name, code, mapped)
				code = mapped
			} else {
				log.Printf("%s exited (status %d)", svc.Name, code)
			}
			// Intentional stops (WasStopped) record exit code 0, not a crash code.
			effectiveCode := code
			if svc.WasStopped() && code > 128 {
				effectiveCode = 0
			}
			// Restart-driven exits count as a single `restarts +1` event, not
			// also `exits +1`/ok/fail. Plain stops and one-off exits still record
			// ServiceExited. Clearing the flag here lets a later crash-then-restart
			// (without an enqueued restart) count normally.
			restartConsumesExit := d.takeRestartPending(svc.Name)

			if d.shuttingDown.Load() {
				if !restartConsumesExit {
					d.m.ServiceExited(svc.Name, effectiveCode)
				}
				// Use pidMap, not d.services: a reload deletes services from
				// d.services immediately but they linger in pidMap until they
				// actually exit. Checking d.services would break the reap loop
				// while those processes are still in their kill-delay window.
				anyRunning := len(d.pidMap) > 0
				d.mu.Unlock()
				if !anyRunning {
					break
				}
				continue
			}

			success := effectiveCode == 0
			var action service.ExitAction

			// An intentional Stop() must not trigger OnSuccess/OnFailure: with the
			// default OnSuccess=ActionShutdown a signal-killed service would
			// otherwise take the whole daemon down. Any enqueued restart already
			// sits on restartCh, so the service still comes back.
			switch {
			case svc.WasStopped():
				action = service.ActionIgnore
			case svc.Scheduled:
				// Scheduled runs are oneshot-style: log the outcome and never
				// take exit actions — the next cron tick is the retry.
				if !success {
					log.Printf("scheduled %s: run failed (status %d)", svc.Name, effectiveCode)
				}
				action = service.ActionIgnore
			case svc.Oneshot:
				// Oneshots triggered via control socket after startup must not
				// take shutdown actions — ignore a clean exit.
				if success {
					action = service.ActionIgnore
				} else {
					// OnFailure was pre-validated by yml.Unmarshal and service.New,
					// so a parse error is impossible in practice. Fall back to
					// Ignore rather than fatalling, which would crash PID 1.
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
				// A failed hard dependency stops its running dependents (systemd
				// Requires= semantics). These dependents are NOT automatically
				// brought back when the dependency recovers: like systemd, gopherd
				// does not track a "was stopped because of X" edge to re-enqueue
				// them. They stay down until manually restarted (or restarted by
				// their own on-failure policy on a later crash). This is
				// intentional; see the requires docs in README.
				for _, other := range d.services {
					if other.Requires[svc.Name] && other.IsRunning() {
						log.Printf("stopping %s: required service %s failed (will not auto-restart when %s recovers)", other.Name, svc.Name, svc.Name)
						other.Stop()
					}
				}
			}

			// Record the exit unless it is part of a restart cycle (already
			// enqueued, or the ActionRestart branch below).
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
				// Capture the done channel before unlocking; MarkExited has
				// already closed it for this specific exit event.
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
				// Pre-store exit code so it survives even if the reap loop breaks
				// (ECHILD) before the goroutine runs.
				d.exitCode.CompareAndSwap(0, int32(effectiveCode))
				// Run async so the reap loop stays free to Wait4 and close done
				// channels; a synchronous call would deadlock when stopAll waits
				// on <-done for other services.
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
		// Unmanaged child: possibly an exec probe whose checker awaits the
		// status; anything else was an orphaned zombie, dropped as before.
		d.reaper.Deliver(pid, code)

		if d.shuttingDown.Load() {
			d.mu.Lock()
			anyRunning := len(d.pidMap) > 0
			d.mu.Unlock()
			if !anyRunning {
				break
			}
		}
	}

	// The reap loop breaks as soon as the last child is reaped, but stopAll
	// (async goroutine) may still be waiting out SIGKILL escalations for
	// group members that outlived their leader. Exiting now would kill those
	// timers and leak the strays; stopAll is bounded (kill-delay + grace per
	// service), so this wait is too.
	if d.shuttingDown.Load() {
		<-d.stopAllDone
	}

	ctrlServer.Stop()
	d.stopChecks()
	d.stopSchedulers()
	// Stop delivering signals before teardown so a late SIGHUP cannot run
	// d.reload() concurrent with closeLogTargets() touching d.services. Closing
	// the channel lets the signal-forwarding goroutine exit.
	goSignal.Stop(sigs)
	close(sigs)
	// All restartCh writers have stopped; wait for in-flight senders, then close
	// so the restart goroutine's range loop terminates.
	d.senderWg.Wait()
	close(d.restartCh)
	d.closeLogTargets()

	return int(d.exitCode.Load())
}

// startServiceLayers brings up every enabled service in topological order.
// Within a layer, oneshots run concurrently (and the next layer waits for them
// to exit cleanly) and non-oneshots also start concurrently — they have no
// ordering edge here, and each one's gating (ready-check, sd_notify) runs in
// its own goroutine so a slow gate doesn't stall its siblings.
//
// The Wait4(-1) reap loop starts only after all layers finish, so a non-oneshot
// that crashes mid-startup lingers as a zombie and its on-failure action is
// deferred until startup completes. This is inherent to starting the reap loop
// last: a concurrent Wait4(-1) would race the per-PID Wait4s used here.
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
		// Scheduled services never run at boot; their runners fire them at
		// cron ticks after startup.
		if svc.Scheduled {
			continue
		}
		// A control-socket client may have started this in a previous layer;
		// don't double-fork.
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

// startNonOneshot runs the full gating sequence for one long-running service:
// optional ready-check, fork+exec, then optional sd_notify wait. Any gate
// failure is fatal — the daemon cannot safely proceed past a failed gate.
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
		c.SetReaper(d.reaper)
		if checkCfg.Exec != nil {
			cred, credErr := service.ResolveCredential(svc.Proc.User, svc.Proc.Group, svc.Proc.UserID, svc.Proc.GroupID, svc.Proc.StrictGroups)
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

	// errAlreadyRunning: a control client started this service between the
	// layer's IsRunning() check and here — not a startup failure.
	if _, err := d.startService(svc); err != nil {
		if err == errConditionUnmet {
			return // skipped, already logged; no sd_notify gate to wait on
		}
		if err != errAlreadyRunning {
			log.Fatalf("start %s: %v", svc.Name, err)
		}
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

// runLayerOneshots starts every enabled oneshot in layer concurrently and
// blocks until all exit (or time out). On the first non-ignored failure it lets
// the others settle, then log.Fatalfs — aborting gopherd without leaving
// stragglers running.
//
// Safety invariant: called from the startup loop, which finishes before the main
// reap loop (Wait4(-1,...)) starts. Concurrent specific-PID Wait4 goroutines are
// safe; only Wait4(-1,...) would race them, and it hasn't started yet.
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
		// Unmet condition skips the oneshot; the layer proceeds without it.
		if reason := svc.Proc.UnmetCondition(); reason != "" {
			log.Printf("oneshot %s skipped (%s)", svc.Name, reason)
			continue
		}
		pid, err := svc.Start()
		if err != nil {
			for _, p := range procs {
				if p.pid > 0 {
					_ = syscall.Kill(-p.pid, syscall.SIGKILL)
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
				// Timeout or wait4 error: make sure the process is gone.
				_ = syscall.Kill(-pp.pid, syscall.SIGKILL)
			}
			results <- result{name: pp.svc.Name, pid: pp.pid, code: code, err: err}
		}(p)
	}

	var fatalErr *result
	for i := 0; i < len(procs); i++ {
		r := <-results
		svc := d.services[r.name]
		if r.err != nil {
			// Wait failed (typically startup-timeout): record a non-zero exit so
			// stats reflect the unclean completion before log.Fatalf below.
			d.m.ServiceExited(r.name, 1)
			if fatalErr == nil {
				rc := r
				fatalErr = &rc
			}
			continue
		}
		svc.MarkExited()
		// Apply exit-code-map before success/failure evaluation, matching the
		// reap loop so a startup oneshot honors the same mapping.
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

// waitOneshot waits for a oneshot process to exit, with an optional timeout
// (empty timeoutStr waits indefinitely). Returns the exit code, or an error on
// timeout.
//
// Safety invariant: called only during the startup loop, before the main reap
// loop (Wait4(-1,...)) starts. Concurrent specific-PID Wait4 calls (distinct
// PIDs) are safe with each other.
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
		// Drain the goroutine so it doesn't leak once the caller kills the process.
		go func() { <-ch }()
		return 0, fmt.Errorf("timed out after %s", timeout)
	}
}

// formatSignalSet renders signals as a deterministic string ("SIGINT, SIGTERM")
// for log output, sorted by numeric value so output doesn't flap across runs.
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
