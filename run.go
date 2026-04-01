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

	cfg, err := yml.Load(configPath)
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

	// Store reverse order for graceful shutdown (dependents stop first).
	d.shutdownOrder = make([]string, len(startOrd))
	for i, name := range startOrd {
		d.shutdownOrder[len(startOrd)-1-i] = name
	}

	// Build log targets and services.
	d.buildLogTargets()
	d.buildServices()

	// Start enabled services in dependency order.
	for _, name := range startOrd {
		svc := d.services[name]
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

		if err := d.startService(svc); err != nil {
			log.Fatalf("start %s: %v", svc.Name, err)
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
				log.Fatalf("%s: ready-check %q did not pass within %s", svc.Name, svc.Proc.ReadyCheck, readyTimeout)
			}
			log.Printf("%s: ready (check %s passed)", svc.Name, svc.Proc.ReadyCheck)
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

	// Forward signals to all children. SIGHUP triggers reload.
	sigs := make(chan os.Signal, 16)
	goSignal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR1, syscall.SIGUSR2)
	go func() {
		for sig := range sigs {
			d.mu.Lock()
			sysSig := sig.(syscall.Signal)
			switch {
			case sysSig == syscall.SIGTERM || sysSig == syscall.SIGINT:
				if !d.shuttingDown {
					d.initiateShutdown(0)
				}
			case sysSig == syscall.SIGHUP:
				d.mu.Unlock()
				msg, err := d.reload()
				if err != nil {
					log.Printf("reload failed: %v", err)
				} else {
					log.Printf("%s", msg)
				}
				continue
			default:
				for _, svc := range d.services {
					svc.Signal(sig)
				}
			}
			d.mu.Unlock()
		}
	}()

	// Handle restart requests from the reap loop.
	go func() {
		for req := range d.restartCh {
			// Wait for the service to actually stop before restarting.
			for req.svc.IsRunning() {
				time.Sleep(50 * time.Millisecond)
			}
			time.Sleep(req.delay)
			d.mu.Lock()
			if d.shuttingDown {
				d.mu.Unlock()
				continue
			}
			d.mu.Unlock()
			if err := d.startService(req.svc); err != nil {
				log.Printf("restart %s failed: %v", req.svc.Name, err)
				d.mu.Lock()
				if !d.shuttingDown {
					d.initiateShutdown(1)
				}
				d.mu.Unlock()
			}
		}
	}()

	// Single reap loop: handles managed children and orphaned zombies.
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err != nil {
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

			if d.shuttingDown {
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
				d.mu.Unlock()
				go func() { d.restartCh <- restartReq{svc: svc, delay: delay} }()
				continue

			case service.ActionShutdown:
				d.initiateShutdown(effectiveCode)

			case service.ActionSuccessShutdown:
				d.initiateShutdown(0)

			case service.ActionFailureShutdown:
				d.initiateShutdown(effectiveCode)

			case service.ActionIgnore:
				log.Printf("%s: ignoring exit", svc.Name)
				d.mu.Unlock()
				continue
			}
		}
		d.mu.Unlock()

		if d.shuttingDown {
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
	d.closeLogTargets()

	return d.exitCode
}

// waitOneshot waits for a oneshot process to exit, with an optional timeout.
// If timeoutStr is empty, it waits indefinitely. Returns the exit code or an
// error if the timeout is exceeded.
//
// Uses a blocking Wait4 in a goroutine rather than WNOHANG polling to avoid
// race conditions with the main reap loop or PID reuse.
func waitOneshot(pid int, timeoutStr string) (int, error) {
	type waitResult struct {
		code int
		err  error
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

	select {
	case r := <-ch:
		return r.code, r.err
	case <-time.After(timeout):
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
