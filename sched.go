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
	"log"
	"time"

	"github.com/haproxytech/gopherd/internal/cron"
	"github.com/haproxytech/gopherd/service"
)

// schedRunner drives one startup=scheduled service: it sleeps until the next
// cron tick and fires start(), skipping the tick when the previous run is
// still going (isRunning). It deliberately knows nothing about services or
// the daemon — the callbacks keep it testable in a synctest bubble.
type schedRunner struct {
	stopCh    chan struct{}
	doneCh    chan struct{}
	start     func()
	isRunning func() bool
	sched     *cron.Schedule
	name      string
}

func newSchedRunner(name string, sched *cron.Schedule, start func(), isRunning func() bool) *schedRunner {
	return &schedRunner{
		name:      name,
		sched:     sched,
		start:     start,
		isRunning: isRunning,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}
}

// loop runs until stop() or until the schedule has no future match. Run it in
// its own goroutine.
func (r *schedRunner) loop() {
	defer close(r.doneCh)
	for {
		now := time.Now()
		next := r.sched.Next(now)
		if next.IsZero() {
			log.Printf("scheduled %s: no future run matches schedule; scheduling stopped", r.name)
			return
		}
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-timer.C:
			if r.isRunning() {
				log.Printf("scheduled %s: skipping run: previous run still in progress", r.name)
				continue
			}
			r.start()
		case <-r.stopCh:
			timer.Stop()
			return
		}
	}
}

// stop signals the loop to exit without joining it: stop is called under
// d.mu (reload) while the loop may be blocked on d.mu inside start(), so
// joining could deadlock — the same rationale as check.Stop. A tick that
// slipped through is harmless: startService re-validates shutdown/replacement
// under d.mu. Called once per runner; reload replaces runners, never reuses.
func (r *schedRunner) stop() {
	close(r.stopCh)
}

// startSchedulers creates and starts one schedRunner per scheduled service.
// Called without d.mu during single-threaded startup and under d.mu from
// reload; appending to d.schedulers is safe in both.
func (d *daemon) startSchedulers() {
	for _, svc := range d.services {
		if !svc.Scheduled {
			continue
		}
		r := newSchedRunner(svc.Name, svc.Schedule, func() { d.startScheduledRun(svc) }, svc.IsRunning)
		d.schedulers = append(d.schedulers, r)
		go r.loop()
		if next := svc.Schedule.Next(time.Now()); !next.IsZero() {
			log.Printf("scheduled %s: next run at %s", svc.Name, next.Format(time.DateTime))
		}
	}
}

// stopSchedulers signals every runner to exit. Non-joining (see
// schedRunner.stop), so it is safe under d.mu.
func (d *daemon) stopSchedulers() {
	for _, r := range d.schedulers {
		r.stop()
	}
	d.schedulers = nil
}

// startScheduledRun fires one scheduled run and, when startup-timeout is set,
// arms a stop for a run that overstays it. The pid comparison keeps the
// timeout from killing a later run of the same service.
func (d *daemon) startScheduledRun(svc *service.Service) {
	pid, err := d.startService(svc)
	if err != nil {
		// Benign races (shutdown began, reload replaced the service, manual
		// start won the tick) are not failures worth logging as errors.
		if err != errShuttingDown && err != errServiceReplaced && err != errAlreadyRunning {
			log.Printf("scheduled %s: start failed: %v", svc.Name, err)
		}
		return
	}
	if svc.Proc.StartupTimeout == "" {
		return
	}
	// Pre-validated at config load; a parse failure here means a code bug,
	// and running unbounded is safer than crashing PID 1.
	dur, err := time.ParseDuration(svc.Proc.StartupTimeout)
	if err != nil || dur <= 0 {
		return
	}
	time.AfterFunc(dur, func() {
		if svc.IsRunning() && svc.Pid.Load() == int64(pid) {
			log.Printf("scheduled %s: run exceeded startup-timeout %s; stopping", svc.Name, svc.Proc.StartupTimeout)
			svc.Stop()
		}
	})
}
