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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Regression: a configured check must appear in `gopherd status` immediately,
// even before its initial-delay window elapses and its first probe runs.
// Pre-fix, the checks map was populated lazily by the first CheckResult call,
// so a check with initial-delay was invisible during the blind window.
func TestE2ECheckInStatsBeforeFirstProbe(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown

checks:
  slow:
    exec:
      command: /bin/true
    period: 60s
    initial-delay: 60s
`)
	defer td.kill()

	resp := td.sendCommand("status")
	if !strings.Contains(resp, "slow") {
		t.Errorf("expected check 'slow' in stats before first probe, got: %s", resp)
	}

	td.stop()
}

func TestE2EExecHealthCheck(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "healthy")

	// The check script succeeds only when the marker file exists.
	// The service creates the marker after 1s.
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "sleep 1 && touch %s && sleep 300"]
    on-failure: shutdown

checks:
  app-health:
    exec:
      command: test
      args: ["-f", "%s"]
    period: 500ms
    timeout: 1s
    threshold: 1
    initial-delay: 500ms
`, marker, marker))
	defer td.kill()

	// Wait for the check to start passing.
	time.Sleep(3 * time.Second)

	resp := td.sendCommand("status")
	if !strings.Contains(resp, "app-health") {
		t.Errorf("expected app-health in stats, got: %s", resp)
	}

	td.stop()
}

func TestE2EExecHealthCheckFailureAction(t *testing.T) {
	// Health check always fails, threshold 1, action: shutdown.
	td := startDaemon(t, `
processes:
  - name: app
    command: sleep
    args: ["300"]
    on-failure: shutdown
    on-check-failure:
      bad-check: shutdown

checks:
  bad-check:
    exec:
      command: /bin/sh
      args: ["-c", "exit 1"]
    period: 500ms
    timeout: 1s
    threshold: 2
    initial-delay: 100ms
`)
	defer td.kill()

	// Daemon should shut down because the check keeps failing.
	code := td.wait(15 * time.Second)
	if code != 1 {
		t.Errorf("expected exit code 1 from check failure shutdown, got %d", code)
	}
}

// A childless daemon must keep reaping exec probe children: each probe fork
// wakes the idle reap loop, which delivers the status to the checker. Pre-fix
// the loop idled in its no-children select, probe children piled up as
// zombies, and every probe starved into an inconclusive result.
func TestE2EExecCheckOnIdleDaemon(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: once
    command: /bin/true
    on-success: ignore

checks:
  pulse:
    exec:
      command: /bin/true
    period: 200ms
    timeout: 500ms
    threshold: 3
    initial-delay: 1s
`)
	defer td.kill()

	// Several probe periods on an idle (childless) daemon.
	time.Sleep(3 * time.Second)

	if !td.daemonAlive() {
		t.Fatal("daemon exited instead of idling as a live supervisor")
	}
	if z := zombieChildren(t, td.Pid()); len(z) > 0 {
		t.Errorf("daemon has %d unreaped probe zombies: %v", len(z), z)
	}
	if out := td.Output(); strings.Contains(out, "inconclusive") {
		t.Errorf("probes starved on the idle daemon:\n%s", out)
	}

	td.stop()
}

// zombieChildren scans /proc for direct children of pid in zombie state.
func zombieChildren(t *testing.T, pid int) []int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatalf("read /proc: %v", err)
	}
	var zombies []int
	for _, e := range entries {
		child, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue // process vanished
		}
		// Fields follow the last ')' (comm may contain spaces): state, ppid, ...
		idx := strings.LastIndexByte(string(data), ')')
		if idx < 0 {
			continue
		}
		fields := strings.Fields(string(data[idx+1:]))
		if len(fields) < 2 {
			continue
		}
		if fields[0] == "Z" && fields[1] == strconv.Itoa(pid) {
			zombies = append(zombies, child)
		}
	}
	return zombies
}

func TestE2EReadyCheck(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ready")
	orderLog := filepath.Join(dir, "order.log")

	// First service creates a marker file after 1s.
	// Second service depends on first via ready-check.
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: slow-starter
    command: /bin/sh
    args: ["-c", "echo slow-started >> %s && sleep 1 && touch %s && sleep 300"]
    on-failure: shutdown

  - name: dependent
    command: /bin/sh
    args: ["-c", "echo dependent-started >> %s && sleep 300"]
    after: [slow-starter]
    ready-check: slow-ready
    ready-timeout: 10s
    on-failure: shutdown

checks:
  slow-ready:
    exec:
      command: test
      args: ["-f", "%s"]
    period: 500ms
    timeout: 1s
    threshold: 1
    level: ready
`, orderLog, marker, orderLog, marker))
	defer td.kill()

	time.Sleep(4 * time.Second)

	// Both should be running.
	resp := td.sendCommand("status slow-starter")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected slow-starter running, got: %s", resp)
	}
	resp = td.sendCommand("status dependent")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected dependent running, got: %s", resp)
	}

	// Verify order: slow-starter logged before dependent.
	data, err := os.ReadFile(orderLog)
	if err != nil {
		t.Fatalf("read order log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got: %q", string(data))
	}
	if lines[0] != "slow-started" || lines[1] != "dependent-started" {
		t.Errorf("wrong start order: %v", lines)
	}

	td.stop()
}

// TestE2EReadyCheckTimeout verifies that a ready-check which never passes
// causes the daemon to log.Fatalf and exit non-zero within bounded time
// (ready-timeout). Pre-fix there was no e2e proof that the timeout path
// fires at all.
func TestE2EReadyCheckTimeout(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gopherd.yml")
	sockPath := filepath.Join(dir, "gopherd.sock")

	config := fmt.Sprintf(`
control:
  socket: %s

checks:
  never-ready:
    exec:
      command: /bin/sh
      args: ["-c", "exit 1"]
    period: 100ms
    timeout: 100ms
    threshold: 1
    initial-delay: 0s

processes:
  - name: app
    command: sleep
    args: ["300"]
    ready-check: never-ready
    ready-timeout: 500ms
`, sockPath)
	os.WriteFile(cfgPath, []byte(config), 0o644)

	cmd := exec.Command(testBinary)
	cmd.Env = append(os.Environ(), "GOPHERD_CONFIG="+cfgPath, "GOPHERD_SOCKET="+sockPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		cmd.Process.Signal(syscall.SIGKILL)
		cmd.Wait()
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected non-zero exit when ready-check times out")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("daemon took %s — ready-timeout did not fire promptly", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit after ready-check timeout")
	}
}

// TestE2ESDNotifyTimeout verifies that a service with sd-notify: true that
// never writes READY=1 trips sd-notify-timeout and the daemon exits non-zero
// within bounded time.
func TestE2ESDNotifyTimeout(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gopherd.yml")
	sockPath := filepath.Join(dir, "gopherd.sock")

	config := fmt.Sprintf(`
control:
  socket: %s

processes:
  - name: silent
    command: sleep
    args: ["300"]
    sd-notify: true
    sd-notify-timeout: 500ms
`, sockPath)
	os.WriteFile(cfgPath, []byte(config), 0o644)

	cmd := exec.Command(testBinary)
	cmd.Env = append(os.Environ(), "GOPHERD_CONFIG="+cfgPath, "GOPHERD_SOCKET="+sockPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		cmd.Process.Signal(syscall.SIGKILL)
		cmd.Wait()
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected non-zero exit when sd-notify times out")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("daemon took %s — sd-notify-timeout did not fire promptly", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit after sd-notify timeout")
	}
}

// TestE2ECheckThresholdEdgeTriggered asserts that a check with threshold N
// fires its action exactly once after N consecutive failures (edge-triggered
// healthy→unhealthy), and not on each subsequent failure. We use shutdown
// as the action and time the daemon exit: it must take at least
// (threshold-1) × period before firing.
func TestE2ECheckThresholdEdgeTriggered(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gopherd.yml")
	sockPath := filepath.Join(dir, "gopherd.sock")

	const period = 200 * time.Millisecond
	const threshold = 3

	config := fmt.Sprintf(`
control:
  socket: %s

checks:
  flaky:
    exec:
      command: /bin/sh
      args: ["-c", "exit 1"]
    period: %s
    timeout: 100ms
    threshold: %d
    initial-delay: 0s

processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown
    on-check-failure:
      flaky: shutdown
`, sockPath, period, threshold)
	os.WriteFile(cfgPath, []byte(config), 0o644)

	cmd := exec.Command(testBinary)
	cmd.Env = append(os.Environ(), "GOPHERD_CONFIG="+cfgPath, "GOPHERD_SOCKET="+sockPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		cmd.Process.Signal(syscall.SIGKILL)
		cmd.Wait()
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected non-zero exit from check-driven shutdown")
		}
		elapsed := time.Since(start)
		// Action fires only on the Nth failure. With threshold=3 and period=200ms
		// the earliest plausible action time is ~2× period after the first probe
		// (probe 1 immediate, probe 2 at ~200ms, probe 3 at ~400ms).
		minWait := time.Duration(threshold-1) * period
		if elapsed < minWait {
			t.Errorf("daemon exited in %s, expected at least %s for threshold=%d", elapsed, minWait, threshold)
		}
		if elapsed > 5*time.Second {
			t.Errorf("daemon took %s — check-driven shutdown did not fire", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit after threshold breaches")
	}
}
