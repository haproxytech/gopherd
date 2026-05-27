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
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestE2EHotReload(t *testing.T) {
	// Services removed during reload get Stop()'d. The reap loop sees the exit
	// and evaluates their exit action. Use on-success: ignore so the daemon
	// doesn't shut down when the removed service's stop-signal death is reaped.
	td := startDaemon(t, `
processes:
  - name: svc-a
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown

  - name: svc-b
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
`)
	defer td.kill()

	// Both should be running.
	resp := td.sendCommand("list")
	if !strings.Contains(resp, "svc-a") || !strings.Contains(resp, "svc-b") {
		t.Fatalf("expected both services in list, got: %s", resp)
	}

	// Reload with svc-b removed and svc-c added.
	td.updateConfig(`
processes:
  - name: svc-a
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown

  - name: svc-c
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`)

	resp = td.sendCommand("reload")
	if strings.Contains(resp, "error") {
		t.Fatalf("reload failed: %s", resp)
	}

	time.Sleep(1 * time.Second)

	// svc-a should still be running, svc-c should be running, svc-b should be gone.
	resp = td.sendCommand("list")
	if !strings.Contains(resp, "svc-a") {
		t.Errorf("expected svc-a in list after reload, got: %s", resp)
	}
	if !strings.Contains(resp, "svc-c") {
		t.Errorf("expected svc-c in list after reload, got: %s", resp)
	}
	if strings.Contains(resp, "svc-b") {
		t.Errorf("svc-b should be removed after reload, got: %s", resp)
	}

	td.stop()
}

// TestE2EHotReloadOnFailureInPlace verifies that a reload which only changes
// `on-failure` mutates the running service's policy in place — no restart —
// and that the new policy applies to the next crash. Pre-reload semantics
// would shut the daemon down on the crash; post-reload semantics ignore it.
func TestE2EHotReloadOnFailureInPlace(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown

  - name: crasher
    command: sleep
    args: ["300"]
    on-failure: shutdown
    on-success: ignore
`)
	defer td.kill()

	resp := td.sendCommand("status crasher")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected crasher running, got: %s", resp)
	}

	// Reload with on-failure flipped to ignore. Same command + args so no
	// restart is required; only policy fields mutate in place.
	td.updateConfig(`
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown

  - name: crasher
    command: sleep
    args: ["300"]
    on-failure: ignore
    on-success: ignore
`)
	resp = td.sendCommand("reload")
	if strings.Contains(resp, "error") {
		t.Fatalf("reload failed: %s", resp)
	}

	// Confirm crasher was NOT restarted by the reload (in-place policy update).
	resp = td.sendCommand("status crasher")
	if !strings.Contains(resp, "running") {
		t.Fatalf("crasher should still be running after policy-only reload, got: %s", resp)
	}

	// Crash the service via a real signal (not Stop()), so WasStopped() stays
	// false and the reap loop dispatches the OnFailure action. With the new
	// policy that action is Ignore; the daemon must stay alive.
	resp = td.sendCommand("signal crasher SIGKILL")
	if strings.Contains(resp, "error") {
		t.Fatalf("signal failed: %s", resp)
	}

	time.Sleep(500 * time.Millisecond)

	if !td.daemonAlive() {
		t.Fatalf("daemon exited unexpectedly after reloaded on-failure: ignore should have suppressed shutdown")
	}
	resp = td.sendCommand("status crasher")
	if !strings.Contains(resp, "stopped") {
		t.Errorf("expected crasher stopped, got: %s", resp)
	}

	td.stop()
}

func TestE2EHotReloadSIGHUP(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: original
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`)
	defer td.kill()

	// Add a new service to the config.
	td.updateConfig(`
processes:
  - name: original
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown

  - name: added
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`)

	// Trigger reload via SIGHUP.
	td.signal(syscall.SIGHUP)
	time.Sleep(2 * time.Second)

	resp := td.sendCommand("list")
	if !strings.Contains(resp, "added") {
		t.Errorf("expected 'added' service after SIGHUP reload, got: %s", resp)
	}

	td.stop()
}
