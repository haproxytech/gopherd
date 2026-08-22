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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2EScheduledFiresAtTick proves the real wiring end to end: a scheduled
// service is not started at boot, the daemon stays alive with no running
// children, and the run fires at the next cron minute. Cron's granularity is
// one minute, so this test waits up to ~65s of wall clock (the timing logic
// itself is covered deterministically by the synctest scheduler tests).
func TestE2EScheduledFiresAtTick(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for a real cron minute boundary")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")

	bootedAt := time.Now()
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: job
    command: /bin/sh
    args: ["-c", "date >> %s"]
    startup: scheduled
    schedule: "* * * * *"
`, marker))
	defer td.kill()

	// The job must not run at boot. Only checkable when boot wasn't within a
	// breath of a minute boundary (the first legitimate tick).
	if bootedAt.Second() < 55 {
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("scheduled job ran at boot; it must only run at cron ticks")
		}
	}

	// Status must identify the service as scheduled, not stopped/pending.
	resp := td.sendCommand("status job")
	if !strings.Contains(resp, "scheduled") {
		t.Fatalf("status = %q, want it to mention scheduled", resp)
	}

	// The run fires at the next minute boundary: within 65s of boot.
	deadline := bootedAt.Add(65 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduled job did not run within 65s")
		}
		time.Sleep(200 * time.Millisecond)
	}

	// The daemon must still be alive after the oneshot-style run exits
	// cleanly (exit 0 must not trigger any shutdown action).
	time.Sleep(500 * time.Millisecond)
	if !td.daemonAlive() {
		t.Fatal("daemon exited after scheduled run completed")
	}
	resp = td.sendCommand("status job")
	if !strings.Contains(resp, "scheduled") {
		t.Fatalf("status after run = %q, want it to mention scheduled", resp)
	}
}

// TestE2EScheduledReload verifies a reload can add and remove scheduled
// services: an added one is registered (status shows the next run) without
// being started, and a removed one disappears. Tick firing after reload is
// covered by the synctest runner tests; waiting out a real minute here would
// double the suite's wall clock for no extra signal.
func TestE2EScheduledReload(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: app
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`)
	defer td.kill()

	td.updateConfig(`
processes:
  - name: app
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown

  - name: job
    command: /bin/true
    startup: scheduled
    schedule: "0 3 * * *"
`)
	resp := td.sendCommand("reload")
	if strings.Contains(resp, "error") {
		t.Fatalf("reload failed: %s", resp)
	}

	resp = td.sendCommand("status job")
	if !strings.Contains(resp, "scheduled (next run") {
		t.Fatalf("status after add = %q, want scheduled with next run", resp)
	}

	// Reload must not have started the scheduled service.
	if strings.Contains(resp, "running") {
		t.Fatalf("scheduled job running right after reload: %s", resp)
	}

	// Remove it again; the daemon must survive and forget the service.
	td.updateConfig(`
processes:
  - name: app
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`)
	resp = td.sendCommand("reload")
	if strings.Contains(resp, "error") {
		t.Fatalf("second reload failed: %s", resp)
	}
	resp = td.sendCommand("status job")
	if !strings.Contains(resp, "unknown") {
		t.Fatalf("status after remove = %q, want unknown service", resp)
	}
	if !td.daemonAlive() {
		t.Fatal("daemon died across scheduled reloads")
	}

	// Stop gracefully: app (sleep 300) inherits the daemon's stdout pipe, and
	// the deferred kill()'s cmd.Wait would block on that pipe until the orphan
	// exits. SIGTERM lets the daemon stop its children first.
	td.stop()
}
