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
