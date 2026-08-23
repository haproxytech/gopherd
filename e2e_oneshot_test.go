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
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestE2EOneshotBeforeDependents(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "init-done")

	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: init
    command: /bin/sh
    args: ["-c", "echo done > %s"]
    startup: oneshot

  - name: app
    command: sleep
    args: ["300"]
    after: [init]
    on-failure: shutdown
`, marker))
	defer td.kill()

	// Verify the oneshot ran (marker file exists).
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("oneshot marker not created: %v", err)
	}
	if !strings.Contains(string(data), "done") {
		t.Errorf("unexpected marker content: %s", data)
	}

	// app should be running.
	resp := td.sendCommand("status app")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected app running, got: %s", resp)
	}

	td.stop()
}

func TestE2EOneshotFailure(t *testing.T) {
	// A failing oneshot should cause the daemon to exit (fatal).
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gopherd.yml")
	sockPath := filepath.Join(dir, "gopherd.sock")

	config := fmt.Sprintf(`
control:
  socket: %s

processes:
  - name: bad-init
    command: /bin/sh
    args: ["-c", "exit 1"]
    startup: oneshot

  - name: app
    command: sleep
    args: ["300"]
    after: [bad-init]
`, sockPath)
	os.WriteFile(cfgPath, []byte(config), 0o644)

	cmd := exec.Command(testBinary)
	cmd.Env = append(os.Environ(), "GOPHERD_CONFIG="+cfgPath, "GOPHERD_SOCKET="+sockPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	err := cmd.Start()
	if err != nil {
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
			t.Fatal("expected non-zero exit for failed oneshot")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit after failed oneshot")
	}
}

// TestE2EOneshotExitCodeMap verifies that exit-code-map is applied to oneshots
// that run during the startup sequence, just like the reap loop applies it to
// long-running services. A oneshot exiting 17 with exit-code-map {17: 0} must
// be treated as success: the daemon survives startup and dependents proceed.
func TestE2EOneshotExitCodeMap(t *testing.T) {
	// startDaemon waits for the control socket; if the remapped-to-0 oneshot
	// were still treated as a failure it would fatal the daemon during startup
	// and the socket would never come up, failing this call.
	td := startDaemon(t, `
processes:
  - name: migrate
    command: /bin/sh
    args: ["-c", "exit 17"]
    startup: oneshot
    on-failure: shutdown
    exit-code-map:
      17: 0

  - name: app
    command: sleep
    args: ["300"]
    after: [migrate]
    on-failure: shutdown
`)
	defer td.kill()

	// The dependent only starts if the oneshot was treated as completed.
	resp := td.sendCommand("status app")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected app running after remapped oneshot, got: %s", resp)
	}

	td.stop()
}

// TestE2EOneshotStartupTimeout verifies that a oneshot which hangs past its
// startup-timeout is killed and treated as a fatal startup failure. Without
// this, a wedged init step would block the daemon indefinitely before any
// dependent service could be started.
func TestE2EOneshotStartupTimeout(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gopherd.yml")
	sockPath := filepath.Join(dir, "gopherd.sock")

	config := fmt.Sprintf(`
control:
  socket: %s

processes:
  - name: hung-init
    command: sleep
    args: ["300"]
    startup: oneshot
    startup-timeout: 500ms

  - name: app
    command: sleep
    args: ["300"]
    after: [hung-init]
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
			t.Fatal("expected non-zero exit for timed-out oneshot")
		}
		// Generous upper bound: the timeout is 500ms; allow startup overhead
		// but ensure we did not wait anywhere near the sleep 300 horizon.
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("daemon took %s to exit after 500ms timeout — startup-timeout did not fire", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not exit after oneshot startup-timeout")
	}
}

// Regression: oneshots that complete during startup must appear in
// `gopherd status`. Pre-fix, runLayerOneshots called svc.Start() directly,
// bypassing the metrics map, so a oneshot was invisible in stats until
// something else (a check-failure restart, a control restart) re-entered
// startService.
func TestE2EOneshotInStats(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: init-step
    command: /bin/sh
    args: ["-c", "true"]
    startup: oneshot
  - name: keeper
    command: sleep
    args: ["300"]
    after: [init-step]
    on-failure: shutdown
`)
	defer td.kill()

	time.Sleep(300 * time.Millisecond)

	resp := td.sendCommand("status")
	if !strings.Contains(resp, "init-step") {
		t.Errorf("expected init-step in stats, got: %s", resp)
	}

	td.stop()
}

// TestE2EControlStartOneshot verifies a oneshot can be re-triggered via the
// control socket after startup and that the daemon stays alive afterward —
// the reap loop's oneshot branch must take ActionIgnore on a successful
// post-startup exit rather than dispatching the default OnSuccess=Shutdown.
func TestE2EControlStartOneshot(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: task
    command: /bin/sh
    args: ["-c", "echo run >> %s"]
    startup: oneshot
  - name: keeper
    command: sleep
    args: ["300"]
    after: [task]
    on-failure: shutdown
`, marker))
	defer td.kill()

	// Oneshot ran once during startup.
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker not created during startup: %v", err)
	}
	if got := strings.Count(string(data), "run"); got != 1 {
		t.Fatalf("expected 1 startup run, got %d (%q)", got, data)
	}

	// Re-trigger the oneshot via the control socket. This exercises the
	// post-startup oneshot path in the reap loop.
	resp := td.sendCommand("start task")
	if strings.Contains(resp, "error") {
		t.Fatalf("start failed: %s", resp)
	}

	time.Sleep(500 * time.Millisecond)

	if !td.daemonAlive() {
		t.Fatalf("daemon exited unexpectedly after control-triggered oneshot")
	}
	data, err = os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker after second run: %v", err)
	}
	if got := strings.Count(string(data), "run"); got != 2 {
		t.Errorf("expected 2 runs after control start, got %d (%q)", got, data)
	}

	td.stop()
}
