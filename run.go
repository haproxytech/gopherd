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
	"syscall"
	"time"

	"github.com/haproxytech/gopherd/check"
	"github.com/haproxytech/gopherd/control"
	"github.com/haproxytech/gopherd/metrics"
	"github.com/haproxytech/gopherd/service"
	"github.com/haproxytech/gopherd/version"
	"github.com/haproxytech/gopherd/yml"
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

	if !cfg.NoLogo {
		fmt.Print(version.Logo)
	}
	log.Printf("%s (built from %s)", version.Version, version.Repo)

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
		fmt.Fprintf(os.Stderr, "  gopherd stats\n")
		fmt.Fprintf(os.Stderr, "  gopherd list\n")
		fmt.Fprintf(os.Stderr, "\ncurrent stats:\n")
		os.Setenv("GOPHERD_SOCKET", socketPath)
		control.RunClient([]string{"stats"})
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

	d := &daemon{
		configPath:     configPath,
		cfg:            cfg,
		entrypointArgs: entrypointArgs,
		pidMap:         make(map[int]*service.Service),
		restartCh:      make(chan restartReq, 64),
	}

	// Initialize stats tracking.
	d.m = metrics.New()

	// Compute start order from dependencies.
	startOrd, err := d.startOrder()
	if err != nil {
		log.Fatalf("dependencies: %v", err)
	}

	// Store start order; stopAll() derives the actual sequence from shutdownMode.
	d.shutdownSeq = startOrd
	d.shutdownMode = cfg.ShutdownOrder

	// Build log targets and services.
	d.buildLogTargets()
	d.buildServices()

	// Start enabled services in dependency order.
	for _, name := range startOrd {
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
			pid, err := svc.Start()
			if err != nil {
				log.Fatalf("oneshot %s: %v", svc.Name, err)
			}
			log.Printf("started oneshot %s (pid %d)", svc.Name, pid)

			code, err := waitOneshot(pid, svc.Proc.StartupTimeout)
			svc.MarkExited()
			if err != nil {
				// Timeout — kill the process and fail.
				syscall.Kill(-pid, syscall.SIGKILL)
				log.Fatalf("oneshot %s: %v", svc.Name, err)
			}
			if code != 0 {
				if svc.OnFailure == service.ActionIgnore {
					log.Printf("oneshot %s exited with status %d (ignored)", svc.Name, code)
					continue
				}
				log.Fatalf("oneshot %s failed (status %d)", svc.Name, code)
			}
			log.Printf("oneshot %s completed", svc.Name)
			continue
		}

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
	}

	// Start health checks.
	d.startChecks()

	// Start the control socket server.
	ctrlServer := d.setupControl()
	if err := ctrlServer.Start(); err != nil {
		log.Printf("warning: control socket: %v", err)
	} else {
		log.Printf("control socket: %s", ctrlServer.SocketPath)
	}

	// Determine which signal triggers graceful shutdown.
	// Default: SIGTERM. Override via config stop-signal or GOPHERD_STOP_SIGNAL env.
	// This allows matching Docker's STOPSIGNAL directive so gopherd shuts down
	// gracefully regardless of which signal the container runtime sends.
	stopSignal := resolveStopSignal(cfg.StopSignal)
	if stopSignal != syscall.SIGTERM {
		log.Printf("stop-signal: %s", stopSignal)
	}

	// Forward signals to all children. SIGHUP triggers reload.
	sigs := make(chan os.Signal, 16)
	goSignal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)
	if stopSignal != syscall.SIGTERM && stopSignal != syscall.SIGINT &&
		stopSignal != syscall.SIGHUP && stopSignal != syscall.SIGUSR1 && stopSignal != syscall.SIGUSR2 {
		goSignal.Notify(sigs, stopSignal)
	}
	go func() {
		for sig := range sigs {
			sysSig, ok := sig.(syscall.Signal)
			if !ok {
				continue
			}
			switch {
			case sysSig == stopSignal || sysSig == syscall.SIGTERM || sysSig == syscall.SIGINT:
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
				d.mu.Lock()
				for _, svc := range d.services {
					svc.Signal(sig)
				}
				d.mu.Unlock()
			}
		}
	}()

	// Handle restart requests from the reap loop.
	go func() {
		for req := range d.restartCh {
			// Wait for the specific exit event that triggered this restart.
			// Using req.done (captured at enqueue time) rather than calling
			// req.svc.Done() here avoids blocking on a newer done channel if
			// the service was already restarted by a concurrent path.
			<-req.done
			// Skip if a concurrent restart already brought the service back up.
			if req.svc.IsRunning() {
				continue
			}
			time.Sleep(req.delay)
			// startService checks shuttingDown atomically with the fork/exec
			// under d.mu, so no separate pre-check is needed here.
			if _, err := d.startService(req.svc); err != nil {
				if err != errShuttingDown {
					log.Printf("restart %s failed: %v", req.svc.Name, err)
					d.initiateShutdown(1)
				}
			}
		}
	}()

	// Single reap loop: handles managed children and orphaned zombies.
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err != nil {
			if err == syscall.EINTR {
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
			runDuration := svc.MarkExited()
			log.Printf("%s exited (status %d)", svc.Name, code)
			d.m.ServiceExited(svc.Name, code)

			if d.shuttingDown.Load() {
				anyRunning := false
				for _, s := range d.services {
					if s.IsRunning() {
						anyRunning = true
						break
					}
				}
				d.mu.Unlock()
				if !anyRunning {
					break
				}
				continue
			}

			// If we intentionally stopped the service (via Stop()), treat
			// the signal-death exit code as 0 for action evaluation.
			// This prevents stop-signal deaths from looking like crashes.
			effectiveCode := code
			if svc.WasStopped() && code > 128 {
				effectiveCode = 0
			}

			success := effectiveCode == 0
			var action service.ExitAction

			// Oneshot services triggered via control socket after startup
			// should not take shutdown actions — just ignore the exit.
			if svc.Oneshot {
				if success {
					action = service.ActionIgnore
				} else {
					action = service.ParseExitAction(svc.Proc.OnFailure, service.ActionIgnore)
				}
			} else if success {
				action = svc.OnSuccess
			} else {
				action = svc.OnFailure
				for _, other := range d.services {
					if other.Requires[svc.Name] && other.IsRunning() {
						log.Printf("stopping %s: required service %s failed", other.Name, svc.Name)
						other.Stop()
					}
				}
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
				d.senderWg.Go(func() {
					d.restartCh <- restartReq{svc: svc, done: exitDone, delay: delay}
				})
				continue

			case service.ActionShutdown:
				d.mu.Unlock()
				d.initiateShutdown(effectiveCode)
				continue

			case service.ActionSuccessShutdown:
				d.mu.Unlock()
				d.initiateShutdown(0)
				continue

			case service.ActionFailureShutdown:
				d.mu.Unlock()
				d.initiateShutdown(effectiveCode)
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
			anyRunning := false
			for _, s := range d.services {
				if s.IsRunning() {
					anyRunning = true
					break
				}
			}
			d.mu.Unlock()
			if !anyRunning {
				break
			}
		}
	}

	ctrlServer.Stop()
	d.stopChecks()
	// All sources that write to restartCh (reap loop, check callbacks, control
	// socket RestartFn) have now stopped. Wait for any in-flight sender
	// goroutines, then close the channel so the restart goroutine's range loop
	// terminates naturally.
	d.senderWg.Wait()
	close(d.restartCh)
	d.closeLogTargets()

	return int(d.exitCode.Load())
}

// waitOneshot waits for a oneshot process to exit, with an optional timeout.
// If timeoutStr is empty, it waits indefinitely. Returns the exit code or an
// error if the timeout is exceeded.
//
// Safety invariant: this is called during the startup loop, which completes
// entirely before the main reap loop (Wait4(-1,...)) starts. There is therefore
// no concurrent Wait4(-1,...) racing with the specific-PID Wait4 goroutine here.
func waitOneshot(pid int, timeoutStr string) (int, error) {
	type waitResult struct {
		err  error
		code int
	}
	ch := make(chan waitResult, 1)
	go func() {
		var ws syscall.WaitStatus
		_, err := syscall.Wait4(pid, &ws, 0, nil)
		if err != nil {
			ch <- waitResult{err: fmt.Errorf("wait4: %w", err)}
			return
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

func waitStatusCode(ws syscall.WaitStatus) int {
	if ws.Exited() {
		return ws.ExitStatus()
	}
	if ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}
