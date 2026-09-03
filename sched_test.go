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
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/haproxytech/gopherd/internal/cron"
	"github.com/haproxytech/gopherd/service"
)

func mustCron(t *testing.T, expr string) *cron.Schedule {
	t.Helper()
	s, err := cron.Parse(expr)
	if err != nil {
		t.Fatalf("cron.Parse(%q): %v", expr, err)
	}
	return s
}

// Inside a synctest bubble the fake clock starts at 2000-01-01 00:00:00 UTC,
// so an every-minute schedule first fires at 00:01:00.

func TestSchedRunnerFiresAtTicks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var starts atomic.Int32
		r := newSchedRunner("job", mustCron(t, "* * * * *"),
			func() { starts.Add(1) },
			func() bool { return false })
		go r.loop()

		time.Sleep(3*time.Minute + time.Second)
		synctest.Wait()
		if got := starts.Load(); got != 3 {
			t.Errorf("starts = %d, want 3", got)
		}
		r.stop()
	})
}

func TestSchedRunnerHonorsCronTimes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var starts atomic.Int32
		// Daily at 03:00: from the bubble epoch the first fire is in 3h.
		r := newSchedRunner("job", mustCron(t, "0 3 * * *"),
			func() { starts.Add(1) },
			func() bool { return false })
		go r.loop()

		time.Sleep(2*time.Hour + 59*time.Minute)
		synctest.Wait()
		if got := starts.Load(); got != 0 {
			t.Fatalf("starts before 03:00 = %d, want 0", got)
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if got := starts.Load(); got != 1 {
			t.Errorf("starts after 03:00 = %d, want 1", got)
		}
		// Next fire is tomorrow 03:00.
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		if got := starts.Load(); got != 2 {
			t.Errorf("starts after next day = %d, want 2", got)
		}
		r.stop()
	})
}

func TestSchedRunnerSkipsWhileRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var starts atomic.Int32
		running := atomic.Bool{}
		running.Store(true)
		r := newSchedRunner("job", mustCron(t, "* * * * *"),
			func() { starts.Add(1) },
			running.Load)
		go r.loop()

		time.Sleep(2*time.Minute + time.Second)
		synctest.Wait()
		if got := starts.Load(); got != 0 {
			t.Fatalf("starts while running = %d, want 0", got)
		}
		// Once the previous run finishes, the next tick fires normally.
		running.Store(false)
		time.Sleep(time.Minute)
		synctest.Wait()
		if got := starts.Load(); got != 1 {
			t.Errorf("starts after run finished = %d, want 1", got)
		}
		r.stop()
	})
}

func TestSchedRunnerStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var starts atomic.Int32
		r := newSchedRunner("job", mustCron(t, "* * * * *"),
			func() { starts.Add(1) },
			func() bool { return false })
		go r.loop()

		r.stop() // must not hang; loop exits even with a pending timer
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if got := starts.Load(); got != 0 {
			t.Errorf("starts after stop = %d, want 0", got)
		}
	})
}

func TestSchedRunnerUnsatisfiableExits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newSchedRunner("job", mustCron(t, "0 0 30 2 *"), // Feb 30 never exists
			func() { t.Error("start must never fire") },
			func() bool { return false })
		go r.loop()
		synctest.Wait()
		// stop() must return immediately because the loop already exited.
		r.stop()
	})
}

// TestStartSchedulersBindsEachService pins that every runner drives its own
// service. startSchedulers builds one closure per scheduled service in a range
// loop, and sharing one variable across iterations — the pre-Go-1.22 capture
// bug, still an easy refactor — makes every runner fire whichever service the
// loop ended on: one job runs on everyone's schedule, the rest never run. The
// runner names stay correct either way, so the closures have to be invoked and
// the starts observed.
func TestStartSchedulersBindsEachService(t *testing.T) {
	d := newTestDaemon([]service.Process{
		// Schedules far enough out that the loops cannot fire during the test.
		{
			Name: "job-a", Command: "/bin/sleep", Args: []string{"30"},
			Startup: "scheduled", Schedule: "0 3 * * *",
		},
		{
			Name: "job-b", Command: "/bin/sleep", Args: []string{"30"},
			Startup: "scheduled", Schedule: "0 4 * * *",
		},
	})
	t.Cleanup(func() { killAllChildren(d) })

	d.startSchedulers()
	if len(d.schedulers) != 2 {
		t.Fatalf("len(d.schedulers) = %d, want 2 (one per scheduled service)", len(d.schedulers))
	}
	// Stop the loops so only the explicit invocations below start anything.
	for _, r := range d.schedulers {
		r.stop()
	}

	for _, r := range d.schedulers {
		r.start()
	}

	started := make(map[string]bool)
	d.mu.Lock()
	for _, svc := range d.pidMap {
		started[svc.Name] = true
	}
	d.mu.Unlock()

	for _, name := range []string{"job-a", "job-b"} {
		if !started[name] {
			t.Errorf("%s was never started; each scheduler must be bound to its "+
				"own service (started: %v)", name, started)
		}
	}
}

// TestStopSchedulersClearsRunners pins that stopSchedulers empties the slice.
// startSchedulers appends, so leaving it populated grows it by the scheduled
// service count on every reload — re-signalling runners that already exited,
// and leaking without bound over a long-lived daemon.
func TestStopSchedulersClearsRunners(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "job", Command: "/bin/true", Startup: "scheduled", Schedule: "0 3 * * *"},
	})

	for round := 1; round <= 3; round++ {
		d.startSchedulers()
		if got := len(d.schedulers); got != 1 {
			t.Fatalf("round %d: len(d.schedulers) = %d, want 1 — it must equal the "+
				"scheduled service count, not accumulate across reloads", round, got)
		}
		d.stopSchedulers()
		if got := len(d.schedulers); got != 0 {
			t.Fatalf("round %d: len(d.schedulers) = %d after stopSchedulers, want 0",
				round, got)
		}
	}
}

// TestStartScheduledRunTimeoutTargetsItsOwnRun pins that a run's
// startup-timeout can only stop the run that armed it. The timer may fire long
// after its own run finished, so without the pid comparison it stops whatever
// is running then — killing a healthy later run because an earlier one was
// slow.
func TestStartScheduledRunTimeoutTargetsItsOwnRun(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{
			Name: "job", Command: "/bin/sleep", Args: []string{"30"},
			Startup: "scheduled", Schedule: "0 3 * * *", StartupTimeout: "1s",
		},
	})
	svc := d.services["job"]
	t.Cleanup(func() { killAllChildren(d) })

	// Run 1 arms a timer that will fire at ~t+1s.
	d.startScheduledRun(svc)
	first := int(svc.Pid.Load())
	if first <= 0 {
		t.Fatal("run 1 did not start")
	}

	// End run 1 well before its timer fires, mimicking what the reap loop does.
	svc.Stop()
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(first, &ws, 0, nil); err != nil {
		t.Skipf("cannot reap run 1 (pid %d): %v", first, err)
	}
	svc.MarkExited()

	// Start run 2 late enough that run 1's timer fires first, but early enough
	// that run 2's own timer has not.
	time.Sleep(700 * time.Millisecond)
	d.startScheduledRun(svc)
	second := int(svc.Pid.Load())
	if second <= 0 || second == first {
		t.Fatalf("run 2 did not start a new process (pid %d, run 1 was %d)", second, first)
	}

	// Run 1's timer fires at ~1s; run 2's at ~1.7s. Look in between.
	//
	// WasStopped is the observable, not IsRunning: Stop() sends the signal and
	// records the intent, but `running` stays set until MarkExited, and no reap
	// loop runs here.
	time.Sleep(500 * time.Millisecond)
	if svc.WasStopped() {
		t.Errorf("run 2 (pid %d) was stopped by run 1's startup-timeout timer; the "+
			"timer must only act on the pid it was armed for", second)
	}
	if err := syscall.Kill(second, 0); err != nil {
		t.Errorf("run 2 (pid %d) is gone (%v); run 1's timeout must not reach it",
			second, err)
	}
}
