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
	"testing"
	"testing/synctest"
	"time"

	"github.com/haproxytech/gopherd/internal/cron"
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
