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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

func TestE2EOnSuccessShutdown(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: quick
    command: /bin/sh
    args: ["-c", "sleep 1 && exit 0"]
    on-success: shutdown
    on-failure: shutdown
`)
	defer td.kill()

	code := td.wait(10 * time.Second)
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
}

func TestE2EOnSuccessRestart(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: restarter
    command: /bin/sh
    args: ["-c", "exit 0"]
    on-success: restart
    on-failure: shutdown
    backoff-delay: 100ms
`)
	defer td.kill()

	// Wait for a few restarts to happen.
	time.Sleep(2 * time.Second)

	// Verify stats show restarts.
	resp := td.sendCommand("status")
	if !strings.Contains(resp, "restarter") {
		t.Fatalf("expected restarter in stats, got: %s", resp)
	}

	code := td.stop()
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
}

func TestE2EOnFailureRestart(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: flaky
    command: /bin/sh
    args: ["-c", "exit 1"]
    on-failure: restart
    on-success: ignore
    backoff-delay: 100ms
`)
	defer td.kill()

	// Wait for a few restarts.
	time.Sleep(2 * time.Second)

	resp := td.sendCommand("status")
	if !strings.Contains(resp, "flaky") {
		t.Fatalf("expected flaky in stats, got: %s", resp)
	}

	code := td.stop()
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
}

func TestE2EOnFailureIgnore(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: ignorer
    command: /bin/sh
    args: ["-c", "exit 1"]
    on-failure: ignore
`)
	defer td.kill()

	// Give time for ignorer to exit.
	time.Sleep(1 * time.Second)

	// keeper should still be running, daemon should not have shut down.
	resp := td.sendCommand("status keeper")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected keeper still running, got: %s", resp)
	}

	resp = td.sendCommand("status ignorer")
	if !strings.Contains(resp, "stopped") {
		t.Fatalf("expected ignorer stopped, got: %s", resp)
	}

	code := td.stop()
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
}

func TestE2ESuccessShutdownExitAction(t *testing.T) {
	// on-success: success-shutdown should exit with code 0 regardless.
	td := startDaemon(t, `
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "sleep 1 && exit 0"]
    on-success: success-shutdown
    on-failure: shutdown
`)
	defer td.kill()

	code := td.wait(10 * time.Second)
	if code != 0 {
		t.Errorf("expected exit code 0 from success-shutdown, got %d", code)
	}
}

func TestE2EFailureShutdownExitAction(t *testing.T) {
	// on-failure: failure-shutdown should exit with the process's exit code.
	td := startDaemon(t, `
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "sleep 1 && exit 7"]
    on-success: ignore
    on-failure: failure-shutdown
`)
	defer td.kill()

	code := td.wait(10 * time.Second)
	if code != 7 {
		t.Errorf("expected exit code 7 from failure-shutdown, got %d", code)
	}
}

func TestE2ERequiresCascade(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown

  - name: db
    command: /bin/sh
    args: ["-c", "sleep 2 && exit 1"]
    on-failure: ignore

  - name: app
    command: sleep
    args: ["300"]
    requires: [db]
    on-failure: ignore
    on-success: ignore
`)
	defer td.kill()

	// Wait for db to fail.
	time.Sleep(4 * time.Second)

	// app should have been stopped because it requires db.
	resp := td.sendCommand("status app")
	if !strings.Contains(resp, "stopped") {
		t.Fatalf("expected app stopped after db failure, got: %s", resp)
	}

	td.stop()
}

// TestE2ESignalForwardingIsOptIn asserts that a forwarded SIGUSR1 reaches only
// the services that declared it in signal-rewrite. Forwarding is opt-in because
// SIGUSR1/SIGUSR2 mean different things to different daemons — log reopen,
// config dump, stats flush — so a broadcast fires side effects in services the
// operator never wired up. Checking the opted-in service cannot catch that;
// the bystander has to be checked too.
func TestE2ESignalForwardingIsOptIn(t *testing.T) {
	dir := t.TempDir()
	optedLog := filepath.Join(dir, "opted.log")
	bystanderLog := filepath.Join(dir, "bystander.log")

	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: opted
    command: /bin/sh
    args: ["-c", "trap 'echo GOT >> %s' USR1; while true; do sleep 0.1; done"]
    signal-rewrite:
      SIGUSR1: SIGUSR1
    on-success: ignore
    on-failure: ignore

  - name: bystander
    command: /bin/sh
    args: ["-c", "trap 'echo GOT >> %s' USR1; while true; do sleep 0.1; done"]
    on-success: ignore
    on-failure: ignore
`, optedLog, bystanderLog))
	defer td.kill()

	td.WaitRunning("opted", 10*time.Second)
	td.WaitRunning("bystander", 10*time.Second)
	// The trap must be installed before the signal arrives.
	time.Sleep(500 * time.Millisecond)

	td.signal(syscall.SIGUSR1)

	// The opted-in service must receive it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(optedLog); err == nil && strings.Contains(string(data), "GOT") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("service with signal-rewrite never received the forwarded SIGUSR1")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// The service that did not opt in must receive nothing. Give any errant
	// delivery the same window to show up.
	time.Sleep(500 * time.Millisecond)
	if data, err := os.ReadFile(bystanderLog); err == nil && len(data) > 0 {
		t.Errorf("service without signal-rewrite received the forwarded signal "+
			"(log: %q); forwarding must be opt-in per service", data)
	}

	td.stop()
}

// TestE2EStopPreservesServiceExitCode pins that the stop-becomes-success rule
// is narrow: only *signal* death codes (>128, 143 for SIGTERM) are neutralised,
// so `gopherd stop` is not booked as a failure. A shutdown handler that exits
// non-zero has genuinely failed and must be recorded as one, intentional stop
// or not. Widening the rule reclassifies broken graceful shutdowns as clean.
func TestE2EStopPreservesServiceExitCode(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
  - name: grumpy
    command: /bin/sh
    args: ["-c", "trap 'exit 2' TERM; while true; do sleep 0.1; done"]
    kill-delay: 5s
    on-success: ignore
    on-failure: ignore
`)
	defer td.kill()

	td.WaitRunning("grumpy", 10*time.Second)
	time.Sleep(500 * time.Millisecond) // let the trap install

	if resp := td.sendCommand("stop grumpy"); strings.Contains(resp, "error") {
		t.Fatalf("stop failed: %s", resp)
	}
	// Poll until the exit has been booked.
	var line string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for l := range strings.SplitSeq(td.sendCommand("status"), "\n") {
			if strings.Contains(l, "grumpy") {
				line = l
			}
		}
		if strings.Contains(line, "exits=1") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Exit code 2 came from the service itself, not from the stop signal, so it
	// must land in fail=, not ok=.
	if !strings.Contains(line, "exits=1 restarts=0 ok=0 fail=1") {
		t.Errorf("after stopping a service whose handler exits 2: %q, want "+
			"exits=1 restarts=0 ok=0 fail=1 (only signal-death codes >128 may be "+
			"neutralised by the intentional-stop rule)", line)
	}

	td.stop()
}

func TestE2EBackoffIncreases(t *testing.T) {
	dir := t.TempDir()
	timestamps := filepath.Join(dir, "times.log")

	// The crasher records its own start time and exits non-zero, so the daemon
	// restarts it and the file accumulates one spawn time per attempt. A keeper
	// service holds the daemon open in between.
	//
	// The stamp helper, not `sh -c 'date +%%s.%%N'`: %%N is GNU-only, and busybox
	// leaves it unexpanded, so on Alpine the timestamps silently collapse to
	// whole seconds and every delay here reads as 0.
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: crasher
    command: %s
    args: ["%s", "1"]
    on-failure: restart
    on-success: ignore
    backoff-delay: 200ms
    backoff-factor: 2.0
    backoff-limit: 5s
`, doctest.Tool(t, "stamp"), timestamps))
	defer td.kill()

	// Delays are 200ms, 400ms, 800ms, 1600ms (±10%% jitter), so ~6s yields at
	// least five spawns.
	time.Sleep(6 * time.Second)

	data, err := os.ReadFile(timestamps)
	if err != nil {
		t.Fatalf("read timestamps: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected at least 4 restarts, got %d", len(lines))
	}

	times := make([]time.Time, 0, len(lines))
	for _, l := range lines {
		ns, err := strconv.ParseInt(strings.TrimSpace(l), 10, 64)
		if err != nil {
			t.Fatalf("unparseable timestamp %q: %v", l, err)
		}
		times = append(times, time.Unix(0, ns))
	}

	// A service that dies instantly must not have its counter reset — that is
	// what turns a crash loop into a fork storm in PID 1 — so the delays have
	// to grow geometrically. The 3rd against the 1st is a nominal 4x gap, well
	// outside the ±10%% jitter, where a disengaged backoff leaves them equal.
	d := delays(times)
	first, third := d[0], d[2]
	if first <= 0 {
		t.Fatalf("non-monotonic spawn times: %v", d)
	}
	if third < first*2 {
		t.Errorf("backoff is not growing: 1st delay %v, 3rd delay %v (want the "+
			"3rd to be >= 2x the 1st; nominal is 4x). A service that crashes "+
			"instantly must not reset its backoff. Delays: %v", first, third, d)
	}
	// Guard the other direction too: the cap must hold, so no delay may blow
	// past backoff-limit plus jitter.
	for i, delay := range d {
		if delay > 5500*time.Millisecond {
			t.Errorf("delay %d = %v exceeds backoff-limit 5s + jitter", i+1, delay)
		}
	}

	td.stop()
}

// delays converts spawn times into the intervals between them.
func delays(times []time.Time) []time.Duration {
	out := make([]time.Duration, 0, len(times))
	for i := 1; i < len(times); i++ {
		out = append(out, times[i].Sub(times[i-1]))
	}
	return out
}
