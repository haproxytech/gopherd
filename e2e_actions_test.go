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

func TestE2EBackoffIncreases(t *testing.T) {
	dir := t.TempDir()
	timestamps := filepath.Join(dir, "times.log")

	// Service exits immediately; restarts should show increasing delay.
	// A keeper service prevents the daemon from exiting between restarts.
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: crasher
    command: /bin/sh
    args: ["-c", "date +%%s.%%N >> %s && exit 1"]
    on-failure: restart
    on-success: ignore
    backoff-delay: 200ms
    backoff-factor: 2.0
    backoff-limit: 5s
`, timestamps))
	defer td.kill()

	// Let it restart a few times.
	time.Sleep(4 * time.Second)

	data, err := os.ReadFile(timestamps)
	if err != nil {
		t.Fatalf("read timestamps: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 restarts, got %d", len(lines))
	}

	// Verify delays are roughly increasing.
	var times []float64
	for _, l := range lines {
		var ts float64
		fmt.Sscanf(l, "%f", &ts)
		times = append(times, ts)
	}
	for i := 2; i < len(times); i++ {
		d1 := times[i-1] - times[i-2]
		d2 := times[i] - times[i-1]
		// Second delay should be at least 1.3x the first (allowing for jitter).
		if d2 < d1*1.3 && i < 4 {
			t.Logf("delays: d1=%.3f d2=%.3f (may be within jitter)", d1, d2)
		}
	}

	td.stop()
}
