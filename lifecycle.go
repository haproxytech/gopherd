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
	"slices"
	"strings"
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
		// Releases the probe transport; repeated starts would otherwise leak a checker each.
		defer c.Stop()
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

// observe reports work's result or a timeout; the work itself is never cancelled.
func (d *daemon) observe(opts control.ActionOptions, name, verb string, work func() error) error {
	result := make(chan error, 1)
	go func() { result <- work() }()
	ctx, cancel := waitContext(opts)
	defer cancel()
	select {
	case err := <-result:
		return err
	case <-d.shutdownCh:
		return errShuttingDown
	case <-ctx.Done():
		return fmt.Errorf("timeout: %s %s still in progress (continues in the background)", name, verb)
	}
}

// dependentClosure returns every service that transitively `requires` name.
func dependentClosure(services map[string]*service.Service, name string) map[string]bool {
	closure := map[string]bool{}
	frontier := []string{name}
	for len(frontier) > 0 {
		cur := frontier[0]
		frontier = frontier[1:]
		for _, other := range services {
			if other.Requires[cur] && !closure[other.Name] && other.Name != name {
				closure[other.Name] = true
				frontier = append(frontier, other.Name)
			}
		}
	}
	return closure
}

// runningDependentsLocked lists the running transitive requirers of name in
// start order. Callers hold d.mu.
func (d *daemon) runningDependentsLocked(name string) []*service.Service {
	closure := dependentClosure(d.services, name)
	var deps []*service.Service
	for _, n := range d.shutdownSeq {
		if closure[n] {
			if svc, ok := d.services[n]; ok && svc.IsRunning() {
				deps = append(deps, svc)
			}
		}
	}
	return deps
}

// stopForRestart books the exit as a restart; the caller owes one pendingRestarts decrement.
func (d *daemon) stopForRestart(svc *service.Service) (<-chan struct{}, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.shuttingDown.Load() {
		return nil, errShuttingDown
	}
	done := svc.Done()
	if svc.IsRunning() {
		svc.Stop()
		d.markRestartPending(svc.Name)
	}
	d.m.ServiceRestarted(svc.Name)
	// Keeps the reap loop treating ECHILD as transient until the fork lands.
	d.pendingRestarts.Add(1)
	return done, nil
}

// startForRestart restarts one leg; a reloaded instance is benign, other failures exit gopherd.
func (d *daemon) startForRestart(ctx context.Context, cfg *yml.Config, svc *service.Service) error {
	err := d.startGated(ctx, cfg, svc)
	switch err {
	case nil, errAlreadyRunning, errServiceReplaced:
		return nil
	case errConditionUnmet, errShuttingDown:
		return err
	}
	// Same as handleRestartReq: a service left down with nothing pending is an outage.
	d.initiateShutdown(1)
	return err
}

// runRestart stops dependents last-first, bounces svc through its gates, then restarts dependents in order.
func (d *daemon) runRestart(svc *service.Service, deps []*service.Service) error {
	// Unbounded on purpose: a client giving up must not strand a stopped service.
	ctx := context.Background()
	cfg := d.currentCfg()
	var stopped int32
	defer func() { d.pendingRestarts.Add(-stopped) }()

	for _, dep := range slices.Backward(deps) {
		done, err := d.stopForRestart(dep)
		if err != nil {
			return err
		}
		stopped++
		if err := d.waitExit(ctx, dep, done); err != nil {
			return err
		}
	}

	done, err := d.stopForRestart(svc)
	if err != nil {
		return err
	}
	stopped++
	if err := d.waitExit(ctx, svc, done); err != nil {
		return err
	}
	if err := d.startForRestart(ctx, cfg, svc); err != nil {
		if err == errConditionUnmet {
			return fmt.Errorf("%s skipped (%s)", svc.Name, svc.UnmetCondition())
		}
		return err
	}

	for _, dep := range deps {
		if err := d.startForRestart(ctx, cfg, dep); err != nil && err != errConditionUnmet {
			return fmt.Errorf("start %s: %w", dep.Name, err)
		}
	}
	return nil
}

// serviceNames joins names for a control reply.
func serviceNames(svcs []*service.Service) string {
	names := make([]string, 0, len(svcs))
	for _, s := range svcs {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}

// restartOrdered runs a sequenced restart (--wait, dependents, or both) to completion; --wait only observes.
func (d *daemon) restartOrdered(svc *service.Service, deps []*service.Service, opts control.ActionOptions) (string, error) {
	work := func() error {
		err := d.runRestart(svc, deps)
		if err != nil {
			log.Printf("restart %s: %v", svc.Name, err)
		}
		return err
	}
	if !opts.Wait {
		go func() { _ = work() }()
		return fmt.Sprintf("%s: restart scheduled (with dependents: %s)", svc.Name, serviceNames(deps)), nil
	}
	if err := d.observe(opts, svc.Name, "restart", work); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("%s: restarted (pid %d)", svc.Name, int(svc.Pid.Load()))
	if len(deps) > 0 {
		msg += "; dependents restarted: " + serviceNames(deps)
	}
	return msg, nil
}
