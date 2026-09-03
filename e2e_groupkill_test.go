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
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// groupLeakConfig runs a shell whose background child survives the stop
// signal: POSIX non-interactive shells start background jobs with SIGINT
// ignored, so only the SIGKILL escalation at kill-delay can reap it.
// sleep 20 (not 300) bounds the damage if the escalation regresses.
func groupLeakConfig(childPidFile string) string {
	return fmt.Sprintf(`
processes:
  - name: leaker
    command: /bin/sh
    args: ["-c", "sleep 20 & echo $! > %s; trap 'exit 0' INT; wait"]
    stop-signal: SIGINT
    kill-delay: 300ms
    on-success: ignore
    on-failure: ignore
`, childPidFile)
}

func readPidFile(t *testing.T, path string) int {
	t.Helper()
	lines := waitForLines(t, path, 1, 5*time.Second)
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		t.Fatalf("bad pid in %s: %q (%v)", path, lines[0], err)
	}
	return pid
}

// waitDead polls until pid no longer exists (ESRCH) or the timeout elapses.
func waitDead(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive after %s: group member not killed at kill-delay", pid, timeout)
}

// TestE2EGroupKillAfterLeaderExit: the leader exits on the stop signal but
// its background child ignores it; the kill-delay escalation must SIGKILL
// the surviving group member even though the leader is already reaped.
func TestE2EGroupKillAfterLeaderExit(t *testing.T) {
	childPidFile := filepath.Join(t.TempDir(), "child.pid")
	td := startDaemon(t, groupLeakConfig(childPidFile))
	defer td.kill()

	td.WaitRunning("leaker", 5*time.Second)
	child := readPidFile(t, childPidFile)

	resp := td.sendCommand("stop leaker")
	if strings.Contains(resp, "error") {
		t.Fatalf("stop failed: %s", resp)
	}

	// kill-delay is 300ms; generous window for loaded CI.
	waitDead(t, child, 3*time.Second)

	if code := td.stop(); code != 0 {
		t.Errorf("expected clean daemon exit, got %d", code)
	}
}

// TestE2EGroupKillCompletesBeforeDaemonExit: the daemon must not return until
// the SIGKILL escalation armed by stopAll has finished. The reap loop breaks
// once the last *managed* pid is reaped, but a group member that survived the
// stop signal is only pending its kill-delay then. Exiting abandons the timer
// and reparents the straggler to the host init — invisible to any "stop and
// wait" test, where the daemon is still alive to finish the job.
func TestE2EGroupKillCompletesBeforeDaemonExit(t *testing.T) {
	childPidFile := filepath.Join(t.TempDir(), "child.pid")
	td := startDaemon(t, groupLeakConfig(childPidFile))
	defer td.kill()

	td.WaitRunning("leaker", 5*time.Second)
	child := readPidFile(t, childPidFile)

	// Shut the whole daemon down and wait for it to exit.
	if code := td.stop(); code != 0 {
		t.Fatalf("expected clean daemon exit, got %d", code)
	}

	// The daemon has returned: the surviving group member must already be gone,
	// not merely scheduled to die.
	if err := syscall.Kill(child, 0); !errors.Is(err, syscall.ESRCH) {
		// Clean up before failing so the stray does not outlive the test run.
		_ = syscall.Kill(child, syscall.SIGKILL)
		t.Errorf("group member %d was still alive when the daemon exited "+
			"(kill(0) = %v); shutdown must wait out the SIGKILL escalation "+
			"instead of leaking the process to the host init", child, err)
	}
}

// TestE2EGroupKillOnRestart: restart must not leak the old instance's group
// members — the old child dies at kill-delay while the new instance (new
// process group) keeps running.
func TestE2EGroupKillOnRestart(t *testing.T) {
	childPidFile := filepath.Join(t.TempDir(), "child.pid")
	td := startDaemon(t, groupLeakConfig(childPidFile))
	defer td.kill()

	td.WaitRunning("leaker", 5*time.Second)
	oldChild := readPidFile(t, childPidFile)

	resp := td.sendCommand("restart leaker")
	if strings.Contains(resp, "error") {
		t.Fatalf("restart failed: %s", resp)
	}

	// New instance overwrites the pid file; poll until it differs.
	var newChild int
	deadline := time.Now().Add(5 * time.Second)
	for {
		if p := readPidFile(t, childPidFile); p != oldChild {
			newChild = p
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("restart did not produce a new instance (child pid still %d)", oldChild)
		}
		time.Sleep(20 * time.Millisecond)
	}

	waitDead(t, oldChild, 3*time.Second)

	// The escalation targeted the old pgid only: new instance unaffected.
	if err := syscall.Kill(newChild, 0); err != nil {
		t.Errorf("new instance's child %d should be alive: %v", newChild, err)
	}
	td.WaitRunning("leaker", 5*time.Second)

	td.stop()
}
