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

func TestE2ESingleServiceStartStop(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: sleeper
    command: sleep
    args: ["300"]
    on-failure: shutdown
`)
	defer td.kill()

	// Verify service is running.
	resp := td.sendCommand("status sleeper")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected sleeper running, got: %s", resp)
	}

	// Verify stats shows the service.
	resp = td.sendCommand("status")
	if !strings.Contains(resp, "sleeper") {
		t.Fatalf("expected sleeper in stats, got: %s", resp)
	}

	// SIGTERM should shut down cleanly.
	code := td.stop()
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
}

func TestE2EDisabledService(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: app
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: debug
    command: sleep
    args: ["300"]
    startup: disabled
`)
	defer td.kill()

	time.Sleep(500 * time.Millisecond)

	// debug should be disabled (not auto-started).
	resp := td.sendCommand("status debug")
	if !strings.Contains(resp, "disabled") {
		t.Fatalf("expected debug disabled, got: %s", resp)
	}

	// app should be running.
	resp = td.sendCommand("status app")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected app running, got: %s", resp)
	}

	td.stop()
}

func TestE2EMultipleServicesOrdering(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "order.log")

	// `first` is a oneshot: the startup sequencer waits for it to exit cleanly
	// before starting the next layer, so `second` (after: [first]) cannot run
	// its echo until `first`'s echo has completed. With two long-running
	// services, `after` only sequences the supervisor's exec calls — the two
	// shells then race to write, so the ordering would not be guaranteed.
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: first
    command: /bin/sh
    args: ["-c", "echo first >> %s"]
    startup: oneshot

  - name: second
    command: /bin/sh
    args: ["-c", "echo second >> %s && sleep 300"]
    after: [first]
    on-failure: shutdown
`, logFile, logFile))
	defer td.kill()

	// Poll for both lines rather than guessing a fixed delay: the shell writes
	// happen asynchronously after the supervisor forks each child.
	lines := waitForLines(t, logFile, 2, 5*time.Second)
	if lines[0] != "first" || lines[1] != "second" {
		t.Errorf("expected [first, second], got %v", lines)
	}

	td.stop()
}

// waitForLines polls path until it contains at least n non-empty lines or the
// timeout elapses, then returns the lines. Fails the test on timeout.
func waitForLines(t *testing.T, path string, n int, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			trimmed := strings.TrimSpace(string(data))
			if trimmed != "" {
				lines := strings.Split(trimmed, "\n")
				if len(lines) >= n {
					return lines
				}
			}
		} else if !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", path, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d lines in %s; have: %q", n, path, string(data))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestE2EMultipleServicesStats(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: web
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: worker
    command: sleep
    args: ["300"]
    on-failure: shutdown
`)
	defer td.kill()

	resp := td.sendCommand("status")
	if !strings.Contains(resp, "web") {
		t.Errorf("expected 'web' in stats, got: %s", resp)
	}
	if !strings.Contains(resp, "worker") {
		t.Errorf("expected 'worker' in stats, got: %s", resp)
	}

	td.stop()
}

func TestE2ECustomStopSignal(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "signal.log")

	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: trapper
    command: /bin/sh
    args: ["-c", "trap 'echo GOT_USR1 > %s; exit 0' USR1; while true; do sleep 0.1; done"]
    stop-signal: SIGUSR1
    kill-delay: 5s
    on-success: shutdown
    on-failure: shutdown
`, marker))
	defer td.kill()

	time.Sleep(1 * time.Second)

	// SIGTERM to daemon triggers stop with SIGUSR1 to the service.
	code := td.stop()
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read signal log: %v", err)
	}
	if !strings.Contains(string(data), "GOT_USR1") {
		t.Errorf("expected GOT_USR1 in signal log, got: %s", data)
	}
}
