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

// TestE2EOneshotsAllCompleteBeforeNextLayer pins that the startup sequencer
// waits for *every* oneshot in a layer, not just the first to report. With one
// oneshot (TestE2EOneshotBeforeDependents) draining one result is
// indistinguishable from draining all, so the boundary needs two — one instant,
// one slow — to be tested at all. A dependent that execs early sees a
// half-finished prerequisite: the config generator that had not written the
// file yet.
func TestE2EOneshotsAllCompleteBeforeNextLayer(t *testing.T) {
	dir := t.TempDir()
	fast := filepath.Join(dir, "fast-done")
	slow := filepath.Join(dir, "slow-done")
	seen := filepath.Join(dir, "seen-by-app")

	// The markers say FASTMARK/SLOWMARK rather than fast/slow: when a marker is
	// missing, cat reports the *path* on stderr, and the paths contain "fast"
	// and "slow" -- so a substring assertion on those would be satisfied by the
	// very error that proves the test should fail.
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: fast-init
    command: /bin/sh
    args: ["-c", "echo FASTMARK > %s"]
    startup: oneshot

  - name: slow-init
    command: /bin/sh
    args: ["-c", "sleep 1; echo SLOWMARK > %s"]
    startup: oneshot

  - name: app
    command: /bin/sh
    args: ["-c", "cat %s %s > %s 2>&1; echo RECORDED >> %s; sleep 300"]
    after: [fast-init, slow-init]
    on-failure: shutdown
`, fast, slow, fast, slow, seen, seen))
	defer td.kill()

	td.WaitRunning("app", 20*time.Second)

	// app records what it could read at exec time. Both oneshots share layer 0
	// and app is in layer 1, so both markers must already exist.
	//
	// Waiting for the RECORDED sentinel, not just for app to be running: the
	// shell creates `seen` when it sets up the redirect, so a read that races
	// the cat finds the file already there and empty. The sentinel is appended
	// whatever the cat found, so it means "app has finished recording" without
	// presuming what it recorded.
	deadline := time.Now().Add(20 * time.Second)
	var got string
	for {
		data, err := os.ReadFile(seen)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", seen, err)
		}
		got = string(data)
		if strings.Contains(got, "RECORDED") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("app never finished recording what it saw; have %q", got)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !strings.Contains(got, "FASTMARK") {
		t.Errorf("app did not observe the fast oneshot's output; saw %q", got)
	}
	if !strings.Contains(got, "SLOWMARK") {
		t.Errorf("app started before the slow oneshot finished: saw %q "+
			"(the next layer must wait for every oneshot in the layer, not just "+
			"the first one to report)", got)
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

// TestE2EFailedOneshotWaitsForSiblings pins that a failing oneshot does not
// abort the layer while its siblings run. "Fail fast" here means log.Fatalf
// with siblings mid-flight: never waited on, so their children outlive the
// daemon and their result goroutines leak. Drain every result, then report.
func TestE2EFailedOneshotWaitsForSiblings(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "sibling-finished")

	start := time.Now()
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: quick-fail
    command: /bin/sh
    args: ["-c", "exit 3"]
    startup: oneshot

  - name: slow-sibling
    command: /bin/sh
    args: ["-c", "sleep 2; echo done > %s"]
    startup: oneshot
`, marker))
	defer td.kill()

	// quick-fail aborts startup, but only after slow-sibling has been reaped.
	code := td.wait(30 * time.Second)
	elapsed := time.Since(start)
	if code == 0 {
		t.Fatalf("expected a non-zero exit after the oneshot failed, got %d", code)
	}
	if elapsed < 1500*time.Millisecond {
		t.Errorf("daemon exited after %v; it must wait for every oneshot in the "+
			"layer (the sibling needs ~2s) before reporting the failure", elapsed)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("sibling oneshot never completed (%v); a failing oneshot must "+
			"not abandon its in-flight siblings", err)
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
