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
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func TestE2EPassthrough(t *testing.T) {
	// When invoked with a non-client command, gopherd should exec it directly.
	cmd := exec.Command(testBinary, "echo", "hello-passthrough")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("passthrough exec failed: %v", err)
	}
	if !strings.Contains(string(out), "hello-passthrough") {
		t.Errorf("expected passthrough output, got: %s", out)
	}
}

func TestE2EPassthroughNotFound(t *testing.T) {
	cmd := exec.Command(testBinary, "nonexistent-binary-xyz")
	cmd.Stderr = nil
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected error for nonexistent passthrough command")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got: %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1, got %d", exitErr.ExitCode())
	}
}

func TestE2EVersionCommand(t *testing.T) {
	cmd := exec.Command(testBinary, "version")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("version command failed: %v", err)
	}
	if !strings.Contains(string(out), "gopherd") {
		t.Errorf("expected 'gopherd' in version output, got: %s", out)
	}
}

func TestE2EAlreadyRunning(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: app
    command: sleep
    args: ["300"]
    on-failure: shutdown
`)
	defer td.kill()

	// Start a second instance with the same socket — it should detect the running daemon and exit.
	cmd := exec.Command(testBinary)
	cmd.Env = append(os.Environ(), "GOPHERD_CONFIG="+td.ConfigPath(), "GOPHERD_SOCKET="+td.SocketPath())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected second instance to exit with error")
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got: %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("expected exit code 1 for already-running, got %d", exitErr.ExitCode())
	}

	td.stop()
}
