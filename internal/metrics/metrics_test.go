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
