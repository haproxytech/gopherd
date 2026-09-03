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
	"time"

	"github.com/haproxytech/gopherd/check"
	"github.com/haproxytech/gopherd/control"
	"github.com/haproxytech/gopherd/internal/yml"
	"github.com/haproxytech/gopherd/service"
)

// waitContext bounds a `--wait` control command.
func waitContext(opts control.ActionOptions) (context.Context, context.CancelFunc) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = control.DefaultWaitTimeout
	}
	return context.WithTimeout(context.Background(), timeout)
}

// currentCfg snapshots the live config pointer (reload swaps it under d.mu).
func (d *daemon) currentCfg() *yml.Config {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cfg
}

// startGated runs the boot readiness sequence; errAlreadyRunning after the gates means success.
func (d *daemon) startGated(ctx context.Context, cfg *yml.Config, svc *service.Service) error {
	if svc.Proc.ReadyCheck != "" {
		checkCfg, ok := cfg.Checks[svc.Proc.ReadyCheck]
		if !ok {
			return fmt.Errorf("%s: ready-check %q not found in [checks]", svc.Name, svc.Proc.ReadyCheck)
		}
		c, err := check.New(svc.Proc.ReadyCheck, checkCfg, nil, nil)
		if err != nil {
			return fmt.Errorf("%s: ready check: %w", svc.Name, err)
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
				return fmt.Errorf("%s: invalid ready-timeout %q: %w", svc.Name, svc.Proc.ReadyTimeout, err)
			}
		}
		gateCtx, cancel := context.WithTimeout(ctx, readyTimeout)
		err = c.WaitReady(gateCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: ready-check %q did not pass within %s (ready-check runs before %s starts; it should poll a dependency already running, not the service itself)", svc.Name, svc.Proc.ReadyCheck, readyTimeout, svc.Name)
		}
		log.Printf("%s: ready (check %s passed)", svc.Name, svc.Proc.ReadyCheck)
	}

	_, startErr := d.startService(svc)
	if startErr != nil && startErr != errAlreadyRunning {
		return startErr
	}

	if svc.Proc.SDNotify {
		sdNotifyTimeout := 60 * time.Second
		if svc.Proc.SDNotifyTimeout != "" {
			sdNotifyTimeout, _ = time.ParseDuration(svc.Proc.SDNotifyTimeout)
		}
		gateCtx, cancel := context.WithTimeout(ctx, sdNotifyTimeout)
		err := svc.WaitSDNotifyReady(gateCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: READY=1 not received within %s: %w", svc.Name, sdNotifyTimeout, err)
		}
		log.Printf("%s: ready (READY=1 received)", svc.Name)
	}
	return startErr
}

// waitExit blocks until done closes, ctx expires, or shutdown begins.
func (d *daemon) waitExit(ctx context.Context, svc *service.Service, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-d.shutdownCh:
		return errShuttingDown
	case <-ctx.Done():
		return fmt.Errorf("timeout: %s still running", svc.Name)
	}
}

// runRestart bounces svc synchronously: stop, wait for the exit, then start
// through its readiness gates.
func (d *daemon) runRestart(ctx context.Context, svc *service.Service) error {
	cfg := d.currentCfg()
	d.mu.Lock()
	if d.shuttingDown.Load() {
		d.mu.Unlock()
		return errShuttingDown
	}
	done := svc.Done()
	if svc.IsRunning() {
		svc.Stop()
		d.markRestartPending(svc.Name)
	}
	d.m.ServiceRestarted(svc.Name)
	// Keeps the reap loop treating ECHILD as transient until the fork lands.
	d.pendingRestarts.Add(1)
	d.mu.Unlock()

	if err := d.waitExit(ctx, svc, done); err != nil {
		d.pendingRestarts.Add(-1)
		return err
	}
	err := d.startGated(ctx, cfg, svc)
	d.pendingRestarts.Add(-1)
	switch err {
	case nil, errAlreadyRunning:
		return nil
	case errConditionUnmet:
		return fmt.Errorf("%s skipped (%s)", svc.Name, svc.Proc.UnmetCondition())
	default:
		return err
	}
}

// restartOrdered serves a `restart --wait`: the reply comes back once the
// service has exited and started again.
func (d *daemon) restartOrdered(svc *service.Service, opts control.ActionOptions) (string, error) {
	ctx, cancel := waitContext(opts)
	defer cancel()
	if err := d.runRestart(ctx, svc); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s: restarted (pid %d)", svc.Name, int(svc.Pid.Load())), nil
}
