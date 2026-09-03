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

package metrics

import (
	"slices"
	"strings"
	"testing"
)

func TestServiceStarted(t *testing.T) {
	t.Parallel()
	m := New()
	m.RegisterService("app", true)
	m.ServiceStarted("app", 1234)
	out := m.Format()
	if !strings.Contains(out, "app") || !strings.Contains(out, "pid=1234") {
		t.Errorf("expected app up with pid=1234, got:\n%s", out)
	}
}

func TestServiceExited(t *testing.T) {
	t.Parallel()
	m := New()
	m.RegisterService("app", true)
	m.ServiceStarted("app", 1234)
	m.ServiceExited("app", 1)
	out := m.Format()
	if !strings.Contains(out, "stopped") {
		t.Errorf("expected stopped, got:\n%s", out)
	}
	if !strings.Contains(out, "fail=1") {
		t.Errorf("expected fail=1, got:\n%s", out)
	}
}

func TestServiceExitedSuccess(t *testing.T) {
	t.Parallel()
	m := New()
	m.RegisterService("app", true)
	m.ServiceExited("app", 0)
	out := m.Format()
	if !strings.Contains(out, "ok=1") {
		t.Errorf("expected ok=1, got:\n%s", out)
	}
}

func TestServiceRestarted(t *testing.T) {
	t.Parallel()
	m := New()
	m.RegisterService("app", true)
	m.ServiceRestarted("app")
	m.ServiceRestarted("app")
	out := m.Format()
	if !strings.Contains(out, "restarts=2") {
		t.Errorf("expected restarts=2, got:\n%s", out)
	}
}

func TestServiceDisabled(t *testing.T) {
	t.Parallel()
	m := New()
	m.RegisterService("app", false)
	out := m.Format()
	if !strings.Contains(out, "disabled") {
		t.Errorf("expected disabled, got:\n%s", out)
	}
}

func TestUnregisterServiceSuppressesLateExit(t *testing.T) {
	t.Parallel()
	m := New()
	m.RegisterService("app", true)
	m.UnregisterService("app")
	m.ServiceExited("app", 0)
	out := m.Format()
	if strings.Contains(out, "app") {
		t.Errorf("unregistered service must not reappear after late exit, got:\n%s", out)
	}
}

func TestCheckResult(t *testing.T) {
	t.Parallel()
	m := New()
	m.RegisterCheck("health")
	m.CheckResult("health", true)
	out := m.Format()
	if !strings.Contains(out, "healthy") {
		t.Errorf("expected healthy, got:\n%s", out)
	}

	m.CheckResult("health", false)
	out = m.Format()
	if !strings.Contains(out, "unhealthy") {
		t.Errorf("expected unhealthy, got:\n%s", out)
	}
	if !strings.Contains(out, "failures=1") {
		t.Errorf("expected failures=1, got:\n%s", out)
	}
}

// TestCheckResultIgnoresUnregistered guards the no-resurrect fix: a late
// in-flight probe result for a check that was never registered (or was
// unregistered by a reload) must not recreate its metrics entry.
func TestCheckResultIgnoresUnregistered(t *testing.T) {
	t.Parallel()
	m := New()
	m.CheckResult("gone", false)
	if out := m.Format(); strings.Contains(out, "gone") {
		t.Errorf("unregistered check should not appear in stats, got:\n%s", out)
	}
}

// TestUnregisterCheck verifies a dropped check's stats do not linger after a
// reload removes it.
func TestUnregisterCheck(t *testing.T) {
	t.Parallel()
	m := New()
	m.RegisterCheck("health")
	m.CheckResult("health", false)
	m.UnregisterCheck("health")
	if out := m.Format(); strings.Contains(out, "health") {
		t.Errorf("unregistered check should not appear in stats, got:\n%s", out)
	}
}

func TestFormatEmpty(t *testing.T) {
	t.Parallel()
	m := New()
	out := m.Format()
	if out != "no stats" {
		t.Errorf("expected 'no stats', got: %q", out)
	}
}

// TestSnapshotAndStringAreOrdered pins that both `status` renderings list
// services and checks by name. They come from ranging maps, so without a sort
// the order changes between daemon starts, breaking golden comparisons and
// line-oriented consumers. One call proves nothing — unsorted output lands in
// sorted order often enough by chance — so the assertion is repeated.
func TestSnapshotAndStringAreOrdered(t *testing.T) {
	t.Parallel()
	names := []string{"zeta", "alpha", "mike", "bravo", "yankee", "delta", "kilo"}
	for range 25 {
		m := New()
		for _, n := range names {
			m.RegisterService(n, true)
			m.RegisterCheck(n + "-probe")
		}

		snap := m.Snapshot()
		got := make([]string, 0, len(snap.Services))
		for _, s := range snap.Services {
			got = append(got, s.Name)
		}
		if !slices.IsSorted(got) {
			t.Fatalf("Snapshot().Services is not sorted by name: %v", got)
		}
		chk := make([]string, 0, len(snap.Checks))
		for _, c := range snap.Checks {
			chk = append(chk, c.Name)
		}
		if !slices.IsSorted(chk) {
			t.Fatalf("Snapshot().Checks is not sorted by name: %v", chk)
		}

		// The human-readable form must agree with the structured one.
		var seen []string
		for line := range strings.SplitSeq(m.Format(), "\n") {
			f := strings.Fields(line)
			if len(f) > 0 && slices.Contains(names, f[0]) {
				seen = append(seen, f[0])
			}
		}
		if !slices.IsSorted(seen) {
			t.Fatalf("Format() lists services out of order: %v", seen)
		}
	}
}

// TestRegisterServiceDoesNotResurrectPending pins that re-registering a service,
// which every reload does, cannot put one that has already run back into
// "pending". Pending means "waiting for its first start", so showing it for a
// service that started and exited tells the operator the daemon never tried.
func TestRegisterServiceDoesNotResurrectPending(t *testing.T) {
	t.Parallel()

	// A service that ran and exited (crash-looping, say, and currently down).
	m := New()
	m.RegisterService("app", true)
	if !m.IsPending("app") {
		t.Fatal("a freshly registered service should be pending")
	}
	m.ServiceStarted("app", 42)
	m.ServiceExited("app", 1)
	if m.IsPending("app") {
		t.Fatal("a service that has exited must not be pending")
	}
	m.RegisterService("app", true) // reload
	if m.IsPending("app") {
		t.Error("a reload marked an already-exited service pending again; the " +
			"pending state means 'never started', and exits are the evidence " +
			"that it has")
	}
	if got := snapshotOf(t, m, "app").State; got != "stopped" {
		t.Errorf("state after reload = %q, want stopped", got)
	}

	// A service that started and is still up must not go pending either.
	m2 := New()
	m2.RegisterService("web", true)
	m2.ServiceStarted("web", 7)
	m2.RegisterService("web", true)
	if m2.IsPending("web") {
		t.Error("a reload marked a running service pending")
	}

	// "Never ran" rests on two independent signals — no recorded start *and* no
	// recorded exit — so each has to hold alone. An exit without a matching
	// start is what the reap loop books for a child it sees after a reload
	// replaced the service, and is then the only evidence it ever ran.
	m3 := New()
	m3.RegisterService("late", true)
	m3.ServiceExited("late", 0)
	m3.RegisterService("late", true) // reload
	if m3.IsPending("late") {
		t.Error("a service with a recorded exit but no recorded start was marked " +
			"pending on reload; a non-zero exit count alone proves it has run")
	}
}

// TestSnapshotStatePrecedence pins the order the state labels are chosen in.
// "up" must win over the rest: a service still running after a reload marked it
// startup:disabled is precisely what an operator needs to see, and reporting
// "disabled" with no pid hides a live process.
func TestSnapshotStatePrecedence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(*Metrics)
		state string
		pid   int
	}{
		{"never started", func(m *Metrics) { m.RegisterService("s", true) }, "pending", 0},
		{"disabled", func(m *Metrics) { m.RegisterService("s", false) }, "disabled", 0},
		{"running", func(m *Metrics) {
			m.RegisterService("s", true)
			m.ServiceStarted("s", 111)
		}, "up", 111},
		{"exited", func(m *Metrics) {
			m.RegisterService("s", true)
			m.ServiceStarted("s", 111)
			m.ServiceExited("s", 0)
		}, "stopped", 0},
		// Still running, but a reload disabled it: "up" must win, with the pid.
		{"running but disabled by reload", func(m *Metrics) {
			m.RegisterService("s", true)
			m.ServiceStarted("s", 222)
			m.RegisterService("s", false)
		}, "up", 222},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := New()
			tc.setup(m)
			snap := snapshotOf(t, m, "s")
			if snap.State != tc.state {
				t.Errorf("state = %q, want %q", snap.State, tc.state)
			}
			if snap.Pid != tc.pid {
				t.Errorf("pid = %d, want %d", snap.Pid, tc.pid)
			}
		})
	}
}

func snapshotOf(t *testing.T, m *Metrics, name string) ServiceSnapshot {
	t.Helper()
	s, ok := m.ServiceSnapshot(name)
	if !ok {
		t.Fatalf("no snapshot for %q", name)
	}
	return s
}
