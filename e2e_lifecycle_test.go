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

	// Verify list shows the service.
	resp = td.sendCommand("list")
	if !strings.Contains(resp, "sleeper") {
		t.Fatalf("expected sleeper in list, got: %s", resp)
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

	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: second
    command: /bin/sh
    args: ["-c", "echo second >> %s && sleep 300"]
    after: [first]
    on-failure: shutdown

  - name: first
    command: /bin/sh
    args: ["-c", "echo first >> %s && sleep 300"]
    on-failure: shutdown
`, logFile, logFile))
	defer td.kill()

	time.Sleep(1 * time.Second)

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read order log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), string(data))
	}
	if lines[0] != "first" || lines[1] != "second" {
		t.Errorf("expected [first, second], got %v", lines)
	}

	td.stop()
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

	resp := td.sendCommand("stats")
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
