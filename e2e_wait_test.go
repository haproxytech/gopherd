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
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

var pidRe = regexp.MustCompile(`pid (\d+)`)

// runningPid returns the pid from a "running (pid N)" status line, or 0.
func runningPid(t *testing.T, td *testDaemon, svc string) int {
	t.Helper()
	resp := td.sendCommand("status " + svc)
	m := pidRe.FindStringSubmatch(resp)
	if m == nil {
		return 0
	}
	pid, _ := strconv.Atoi(m[1])
	return pid
}

// readStamps parses the nanosecond timestamps a stamp tool appended to file.
func readStamps(t *testing.T, file string) []int64 {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	var out []int64
	for line := range strings.FieldsSeq(string(data)) {
		n, err := strconv.ParseInt(line, 10, 64)
		if err != nil {
			t.Fatalf("bad stamp %q in %s", line, file)
		}
		out = append(out, n)
	}
	return out
}

func TestE2EControlStopWait(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: svc
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
`)
	defer td.kill()
	td.WaitRunning("svc", 5*time.Second)

	resp := td.sendCommand("stop svc --wait")
	if !strings.Contains(resp, "svc: stopped") {
		t.Fatalf("expected 'svc: stopped', got: %s", resp)
	}
	// No settling sleep: --wait already returned after the exit was reaped.
	if resp := td.sendCommand("status svc"); !strings.Contains(resp, "stopped") {
		t.Fatalf("expected stopped right after stop --wait, got: %s", resp)
	}
	if resp := td.sendCommand("stop svc --wait"); !strings.Contains(resp, "already stopped") {
		t.Fatalf("expected 'already stopped', got: %s", resp)
	}
	td.stop()
}

func TestE2EControlStopWaitTimeout(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: stubborn
    command: sh
    args: ["-c", "trap '' TERM; while :; do sleep 0.2; done"]
    kill-delay: 10s
    on-success: ignore
    on-failure: ignore
`)
	defer td.kill()
	td.WaitRunning("stubborn", 5*time.Second)

	resp := td.sendCommand("stop stubborn --wait --timeout 500ms")
	if !strings.Contains(resp, "error:") || !strings.Contains(resp, "still running") {
		t.Fatalf("expected timeout error, got: %s", resp)
	}
	// The stop itself was still issued: SIGKILL lands after kill-delay.
	if resp := td.sendCommand("status stubborn"); !strings.Contains(resp, "running") {
		t.Fatalf("expected still running within kill-delay, got: %s", resp)
	}
}

func TestE2EControlStartWaitSDNotify(t *testing.T) {
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: notifier
    command: %s
    startup: disabled
    sd-notify: true
    sd-notify-timeout: 5s
    on-success: ignore
    on-failure: ignore
  - name: silent
    command: sleep
    args: ["300"]
    startup: disabled
    sd-notify: true
    sd-notify-timeout: 500ms
    on-success: ignore
    on-failure: ignore
`, doctest.Tool(t, "sdnotifyready")))
	defer td.kill()

	resp := td.sendCommand("start notifier --wait")
	if !strings.Contains(resp, "notifier: ready") {
		t.Fatalf("expected 'notifier: ready', got: %s", resp)
	}

	// Never sending READY=1 fails the wait after sd-notify-timeout; the service stays running.
	resp = td.sendCommand("start silent --wait")
	if !strings.Contains(resp, "error:") || !strings.Contains(resp, "READY=1") {
		t.Fatalf("expected READY=1 timeout error, got: %s", resp)
	}
	if resp := td.sendCommand("status silent"); !strings.Contains(resp, "running") {
		t.Fatalf("expected silent still running, got: %s", resp)
	}
	td.stop()
}

// start --wait runs the boot-time ready-check gate before spawning.
func TestE2EControlStartWaitReadyCheck(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: gated
    command: sleep
    args: ["300"]
    startup: disabled
    ready-check: gate
    ready-timeout: 500ms
    on-success: ignore
    on-failure: ignore
checks:
  gate:
    exec:
      command: test
      args: ["-e", %q]
    period: 100ms
    timeout: 1s
    threshold: 1
`, gate))
	defer td.kill()

	resp := td.sendCommand("start gated --wait")
	if !strings.Contains(resp, "error:") || !strings.Contains(resp, "ready-check") {
		t.Fatalf("expected ready-check failure, got: %s", resp)
	}
	if runningPid(t, td, "gated") != 0 {
		t.Fatalf("gated must not have been spawned while its gate is closed")
	}

	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	resp = td.sendCommand("start gated --wait")
	if !strings.Contains(resp, "gated: started") {
		t.Fatalf("expected 'gated: started', got: %s", resp)
	}
	if runningPid(t, td, "gated") == 0 {
		t.Fatalf("expected gated running after gate opened")
	}
	td.stop()
}

func TestE2EControlRestartWait(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: svc
    command: sleep
    args: ["300"]
    kill-delay: 2s
`)
	defer td.kill()
	td.WaitRunning("svc", 5*time.Second)
	oldPid := runningPid(t, td, "svc")

	resp := td.sendCommand("restart svc --wait")
	if !strings.Contains(resp, "svc: restarted") {
		t.Fatalf("expected 'svc: restarted', got: %s", resp)
	}
	newPid := runningPid(t, td, "svc")
	if newPid == 0 || newPid == oldPid {
		t.Fatalf("expected a new running pid, old=%d new=%d", oldPid, newPid)
	}
	if resp := td.sendCommand("status"); !strings.Contains(resp, "restarts=1") || !strings.Contains(resp, "exits=0") {
		t.Errorf("expected restarts=1 exits=0, got: %s", resp)
	}
	td.stop()
}

// A client that gives up waiting must not strand the service.
func TestE2EControlRestartWaitTimeoutRecovers(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: slow
    command: sh
    args: ["-c", "trap 'sleep 1; exit 0' TERM; while :; do sleep 0.1; done"]
    kill-delay: 10s
    on-success: ignore
    on-failure: ignore
`)
	defer td.kill()
	td.WaitRunning("slow", 5*time.Second)
	oldPid := runningPid(t, td, "slow")

	resp := td.sendCommand("restart slow --wait --timeout 200ms")
	if !strings.Contains(resp, "error:") || !strings.Contains(resp, "still in progress") {
		t.Fatalf("expected timeout error, got: %s", resp)
	}
	deadline := time.Now().Add(5 * time.Second)
	newPid := 0
	for time.Now().Before(deadline) {
		if newPid = runningPid(t, td, "slow"); newPid != 0 && newPid != oldPid {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if newPid == 0 || newPid == oldPid {
		t.Fatalf("service never came back after the client timed out: old=%d new=%d (%s)", oldPid, newPid, td.sendCommand("status slow"))
	}
	if resp := td.sendCommand("status"); !strings.Contains(resp, "restarts=1") || !strings.Contains(resp, "exits=0") {
		t.Errorf("expected restarts=1 exits=0, got: %s", resp)
	}
	td.stop()
}

// statusLine returns the overview row for svc from a bare `status` reply.
func statusLine(t *testing.T, td *testDaemon, svc string) string {
	t.Helper()
	for line := range strings.SplitSeq(td.sendCommand("status"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), svc+" ") {
			return line
		}
	}
	t.Fatalf("no status row for %s", svc)
	return ""
}

// restart-with-dependents: requirers stop last-first, db restarts, requirers return in order; stopped ones stay stopped.
func TestE2EControlRestartWithDependents(t *testing.T) {
	dir := t.TempDir()
	stamp := doctest.Tool(t, "stamp")
	notify := doctest.Tool(t, "sdnotifyready")
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: db
    command: sh
    args: ["-c", "%[1]s %[2]s/db-start; exec %[3]s"]
    restart-with-dependents: true
    sd-notify: true
    sd-notify-timeout: 5s
    kill-delay: 2s
    on-success: ignore
    on-failure: ignore
  - name: web
    command: sh
    args: ["-c", "trap '%[1]s %[2]s/web-stop; exit 0' TERM; %[1]s %[2]s/web-start; while :; do sleep 0.1; done"]
    requires: [db]
    kill-delay: 2s
    on-success: ignore
    on-failure: ignore
  - name: idle
    command: sleep
    args: ["300"]
    requires: [db]
    on-success: ignore
    on-failure: ignore
`, stamp, dir, notify))
	defer td.kill()
	td.WaitRunning("web", 5*time.Second)
	td.WaitRunning("idle", 5*time.Second)
	if resp := td.sendCommand("stop idle --wait"); !strings.Contains(resp, "idle: stopped") {
		t.Fatalf("stop idle: %s", resp)
	}
	dbPid, webPid := runningPid(t, td, "db"), runningPid(t, td, "web")

	resp := td.sendCommand("restart db --wait")
	if !strings.Contains(resp, "db: restarted") || !strings.Contains(resp, "web") {
		t.Fatalf("expected db restarted with web listed, got: %s", resp)
	}
	if strings.Contains(resp, "idle") {
		t.Fatalf("stopped dependent must not be touched, got: %s", resp)
	}
	if p := runningPid(t, td, "db"); p == 0 || p == dbPid {
		t.Fatalf("db pid: old=%d new=%d", dbPid, p)
	}
	if p := runningPid(t, td, "web"); p == 0 || p == webPid {
		t.Fatalf("web pid: old=%d new=%d", webPid, p)
	}
	if resp := td.sendCommand("status idle"); !strings.Contains(resp, "stopped") {
		t.Fatalf("expected idle still stopped, got: %s", resp)
	}

	// The reply returns at fork time; the shells stamp a few ms later.
	var dbStarts, webStarts, webStops []int64
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		dbStarts = readStamps(t, filepath.Join(dir, "db-start"))
		webStarts = readStamps(t, filepath.Join(dir, "web-start"))
		webStops = readStamps(t, filepath.Join(dir, "web-stop"))
		if len(dbStarts) == 2 && len(webStarts) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(dbStarts) != 2 || len(webStarts) != 2 || len(webStops) != 1 {
		t.Fatalf("stamps: db-start=%d web-start=%d web-stop=%d", len(dbStarts), len(webStarts), len(webStops))
	}
	// db stamps before READY=1, so its stamp causally precedes web's respawn.
	if !(webStops[0] < dbStarts[1] && dbStarts[1] < webStarts[1]) {
		t.Fatalf("order violated: web-stop=%d db-restart=%d web-restart=%d", webStops[0], dbStarts[1], webStarts[1])
	}
	// The cascade is booked as a restart of web, not as a crash.
	if line := statusLine(t, td, "web"); !strings.Contains(line, "exits=0") || !strings.Contains(line, "restarts=1") {
		t.Errorf("web accounted as exit, want restarts=1 exits=0: %s", line)
	}
	td.stop()
}

// Without --wait the cascade runs in the background and the reply names it.
func TestE2EControlRestartWithDependentsAsync(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: db
    command: sleep
    args: ["300"]
    restart-with-dependents: true
    kill-delay: 2s
    on-success: ignore
    on-failure: ignore
  - name: web
    command: sleep
    args: ["300"]
    requires: [db]
    kill-delay: 2s
    on-success: ignore
    on-failure: ignore
`)
	defer td.kill()
	td.WaitRunning("web", 5*time.Second)
	dbPid, webPid := runningPid(t, td, "db"), runningPid(t, td, "web")

	resp := td.sendCommand("restart db")
	if !strings.Contains(resp, "restart scheduled") || !strings.Contains(resp, "web") {
		t.Fatalf("expected scheduled cascade naming web, got: %s", resp)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if p := runningPid(t, td, "web"); p != 0 && p != webPid && runningPid(t, td, "db") != dbPid {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if p := runningPid(t, td, "db"); p == 0 || p == dbPid {
		t.Fatalf("db pid: old=%d new=%d", dbPid, p)
	}
	if p := runningPid(t, td, "web"); p == 0 || p == webPid {
		t.Fatalf("web pid: old=%d new=%d", webPid, p)
	}
	td.stop()
}

// A reload that only flips restart-with-dependents takes effect without
// restarting the service.
func TestE2EControlRestartWithDependentsReload(t *testing.T) {
	base := `
processes:
  - name: db
    command: sleep
    args: ["300"]
    kill-delay: 2s
    on-success: ignore
    on-failure: ignore
%s
  - name: web
    command: sleep
    args: ["300"]
    requires: [db]
    kill-delay: 2s
    on-success: ignore
    on-failure: ignore
`
	td := startDaemon(t, fmt.Sprintf(base, ""))
	defer td.kill()
	td.WaitRunning("web", 5*time.Second)
	dbPid := runningPid(t, td, "db")

	td.updateConfig(fmt.Sprintf(base, "    restart-with-dependents: true"))
	if resp := td.sendCommand("reload"); strings.Contains(resp, "error") {
		t.Fatalf("reload: %s", resp)
	}
	if p := runningPid(t, td, "db"); p != dbPid {
		t.Fatalf("reload must not restart db: old=%d new=%d", dbPid, p)
	}
	resp := td.sendCommand("restart db --wait")
	if !strings.Contains(resp, "dependents restarted: web") {
		t.Fatalf("expected cascade after reload, got: %s", resp)
	}
	td.stop()
}
