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
	"fmt"
	"path/filepath"
	"strconv"
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
	resp := td.sendCommand("status")
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
	resp = td.sendCommand("status")
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

// pidOf reads a service's pid out of `status <name>`.
func pidOf(t *testing.T, td *testDaemon, name string) int {
	t.Helper()
	resp := td.sendCommand("status " + name)
	_, rest, ok := strings.Cut(resp, "(pid ")
	if !ok {
		t.Fatalf("status %s has no pid: %s", name, resp)
	}
	num, _, _ := strings.Cut(rest, ")")
	pid, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil || pid <= 0 {
		t.Fatalf("status %s: unparseable pid %q (%v)", name, num, err)
	}
	return pid
}

// TestE2EReloadPreservesUnchangedServices pins the point of hot reload: an
// unchanged service keeps running untouched, a changed one is replaced. Both
// names appear in `status` either way, so the identity that matters is the pid.
// Inverting the condition restarts everything that was fine and leaves
// everything that changed running stale — indistinguishable, from outside,
// from a successful reload.
func TestE2EReloadPreservesUnchangedServices(t *testing.T) {
	const before = `
processes:
  - name: steady
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
  - name: churn
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
`
	// Only churn's argv differs.
	const after = `
processes:
  - name: steady
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
  - name: churn
    command: sleep
    args: ["301"]
    on-success: ignore
    on-failure: ignore
`
	td := startDaemon(t, before)
	defer td.kill()

	td.WaitRunning("steady", 10*time.Second)
	td.WaitRunning("churn", 10*time.Second)
	steadyPid := pidOf(t, td, "steady")
	churnPid := pidOf(t, td, "churn")

	td.updateConfig(after)
	if resp := td.sendCommand("reload"); strings.Contains(resp, "error") {
		t.Fatalf("reload failed: %s", resp)
	}

	td.WaitRunning("steady", 10*time.Second)
	td.WaitRunning("churn", 10*time.Second)
	// Wait for churn's replacement to settle on a new pid.
	deadline := time.Now().Add(10 * time.Second)
	for pidOf(t, td, "churn") == churnPid && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}

	if got := pidOf(t, td, "steady"); got != steadyPid {
		t.Errorf("unchanged service was restarted: pid %d -> %d; a reload must "+
			"leave a service whose process config did not change alone",
			steadyPid, got)
	}
	if got := pidOf(t, td, "churn"); got == churnPid {
		t.Errorf("changed service kept running as pid %d; a reload must replace "+
			"a service whose argv changed so the new config takes effect", got)
	}

	td.stop()
}

// TestE2EReloadWaitsForOldInstance pins that a replaced service's old process
// is gone before its replacement starts. Without the wait the two overlap, and
// anything exclusive the service owns — a listening port, a lock file — is
// contended for the width of the window. "Both running afterwards" holds either
// way, so the overlap has to be caught while it happens: the service reports
// whether its predecessor still held the lock at exec time.
func TestE2EReloadWaitsForOldInstance(t *testing.T) {
	dir := t.TempDir()
	lock := filepath.Join(dir, "exclusive.lock")
	report := filepath.Join(dir, "report")

	// Each instance's first act is to report whether the lock was held. On stop
	// it lingers before releasing, as a service draining connections would,
	// which widens the overlap from microseconds to something observable.
	// kill-delay is generous so SIGTERM, not SIGKILL, ends the old instance.
	cfg := func(arg string) string {
		return fmt.Sprintf(`
processes:
  - name: exclusive
    command: /bin/sh
    args: ["-c", "if [ -e %[1]s ]; then echo OVERLAP >> %[2]s; else echo CLEAN >> %[2]s; fi; : > %[1]s; trap 'sleep 1; rm -f %[1]s; exit 0' TERM; while true; do sleep 0.1; done; echo %[3]s"]
    kill-delay: 10s
    on-success: ignore
    on-failure: ignore
`, lock, report, arg)
	}

	td := startDaemon(t, cfg("first"))
	defer td.kill()
	td.WaitRunning("exclusive", 10*time.Second)
	// The first instance must have found a clean slate.
	if lines := waitForLines(t, report, 1, 10*time.Second); lines[0] != "CLEAN" {
		t.Fatalf("first instance reported %q, want CLEAN", lines[0])
	}

	// Changing argv forces a replacement rather than an in-place update.
	td.updateConfig(cfg("second"))
	if resp := td.sendCommand("reload"); strings.Contains(resp, "error") {
		t.Fatalf("reload failed: %s", resp)
	}

	// Wait for the replacement to report. This is the assertion point: the new
	// instance has run its check, so whatever it saw is now on disk.
	lines := waitForLines(t, report, 2, 20*time.Second)
	if lines[1] != "CLEAN" {
		t.Errorf("the replacement started while the old instance still held the "+
			"lock (reported %q); a reload must wait for the old process to exit "+
			"before starting its replacement", lines[1])
	}

	td.stop()
}

// TestE2EReloadRefusedDuringStartup pins the reload gate. The control socket is
// up before services start, so a reload can arrive while the startup loop is
// still reading d.services without the mutex; opening the gate earlier lets
// reload() race that loop — a map race and a nil-map write panic in PID 1. The
// refusal is explicit so an operator, or a retrying orchestrator, can tell "too
// early" from "config broken".
func TestE2EReloadRefusedDuringStartup(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: slow-init
    command: /bin/sh
    args: ["-c", "sleep 3"]
    startup: oneshot

  - name: app
    command: sleep
    args: ["300"]
    after: [slow-init]
    on-success: ignore
    on-failure: ignore
`)
	defer td.kill()

	// startDaemon returns once the socket accepts connections, which happens
	// before the startup layers run, so we are inside the window here.
	var refused bool
	for range 10 {
		resp := td.sendCommand("reload")
		if strings.Contains(resp, "still starting up") {
			refused = true
			break
		}
		if !strings.Contains(resp, "error") {
			// Startup already finished; the window closed before we looked.
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !refused {
		t.Errorf("a reload during startup was not refused with " +
			"\"daemon still starting up\"; the reload gate must stay closed " +
			"until the startup loop has finished reading d.services")
	}

	// Once startup completes the gate must open.
	td.WaitRunning("app", 20*time.Second)
	if resp := td.sendCommand("reload"); strings.Contains(resp, "error") {
		t.Errorf("reload after startup should succeed, got: %s", resp)
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

	resp := td.sendCommand("status")
	if !strings.Contains(resp, "added") {
		t.Errorf("expected 'added' service after SIGHUP reload, got: %s", resp)
	}

	td.stop()
}

// TestE2EReloadValidatesBeforeMutating pins that a config which parses but is
// rejected by the service layer is refused *before* any state changes. Without
// the dry-run pass the reload stops and removes services first, then discovers
// the problem, leaving a half-reconciled daemon with no way back short of a
// restart. Reload has to be all-or-nothing.
func TestE2EReloadValidatesBeforeMutating(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
  - name: doomed
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
`)
	defer td.kill()
	td.WaitRunning("keeper", 10*time.Second)
	td.WaitRunning("doomed", 10*time.Second)
	keeperPid := pidOf(t, td, "keeper")
	doomedPid := pidOf(t, td, "doomed")

	// Parses fine; service.New rejects the negative kill-delay. `doomed` is
	// also dropped, so a reload that mutated first would have stopped it before
	// discovering the problem.
	td.updateConfig(`
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    kill-delay: -1s
    on-success: ignore
    on-failure: ignore
`)

	resp := td.sendCommand("reload")
	if !strings.Contains(resp, "error") {
		t.Fatalf("reload accepted a config service.New rejects: %s", resp)
	}

	// Nothing may have changed: same services, same pids, daemon still up.
	if !td.daemonAlive() {
		t.Fatal("daemon died on a rejected reload")
	}
	time.Sleep(500 * time.Millisecond)
	if got := pidOf(t, td, "keeper"); got != keeperPid {
		t.Errorf("keeper pid %d -> %d across a rejected reload", keeperPid, got)
	}
	if got := pidOf(t, td, "doomed"); got != doomedPid {
		t.Errorf("doomed pid %d -> %d across a rejected reload; a reload that "+
			"fails validation must not have touched anything", doomedPid, got)
	}

	td.stop()
}
