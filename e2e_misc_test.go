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

	"github.com/haproxytech/gopherd/control"
	"github.com/haproxytech/gopherd/version"
)

// daemonStdout starts the daemon with a minimal config plus extraEnv, waits
// for the control socket, shuts it down, and returns everything it wrote to
// stdout. Stdout goes to a regular file rather than a pipe so cmd.Wait does
// not block on service children (sleep) that inherit the descriptor.
func daemonStdout(t *testing.T, extraEnv ...string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gopherd.yml")
	sockPath := filepath.Join(dir, "gopherd.sock")

	config := fmt.Sprintf(`control:
  socket: %s

processes:
  - name: app
    command: sleep
    args: ["300"]
`, sockPath)
	if err := os.WriteFile(cfgPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	outPath := filepath.Join(dir, "stdout")
	outFile, err := os.Create(outPath)
	if err != nil {
		t.Fatalf("create stdout file: %v", err)
	}
	defer outFile.Close()

	cmd := exec.Command(testBinary)
	cmd.Env = append(os.Environ(), "GOPHERD_CONFIG="+cfgPath, "GOPHERD_SOCKET="+sockPath)
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stdout = outFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Wait()
	}()

	deadline := time.Now().Add(10 * time.Second)
	for !control.IsAlive(sockPath) {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}

	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()

	stdout, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read stdout file: %v", err)
	}
	return string(stdout)
}

func TestE2ELogoPrintedByDefault(t *testing.T) {
	stdout := daemonStdout(t)
	if !strings.Contains(stdout, version.Logo) {
		t.Errorf("logo not printed on startup:\n%s", stdout)
	}
}

func TestE2ELogoSuppressedByEnv(t *testing.T) {
	// Env suppression keeps test configs free of test-only flags.
	stdout := daemonStdout(t, "GOPHERD_NO_LOGO=1")
	if strings.Contains(stdout, version.Logo) {
		t.Errorf("logo printed despite GOPHERD_NO_LOGO=1:\n%s", stdout)
	}
}

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
