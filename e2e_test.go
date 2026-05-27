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

// Package main: shared e2e harness. The actual e2e tests live in the
// topical e2e_*_test.go files (lifecycle, actions, oneshot, shutdown,
// control, reload, checks, env, misc). This file only holds TestMain,
// the testDaemon struct, and the helpers all those files reuse.
package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// testBinary is the path to the built gopherd binary, set by TestMain.
var testBinary string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "gopherd-e2e-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: mktemp: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	testBinary = filepath.Join(tmp, "gopherd")
	build := exec.Command("go", "build", "-o", testBinary, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build failed: %v\n", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

type testDaemon struct {
	cmd        *exec.Cmd
	configPath string
	socketPath string
	dir        string
	t          *testing.T
}

// startDaemon writes a config, starts the binary, and waits for the control socket.
func startDaemon(t *testing.T, config string, extraArgs ...string) *testDaemon {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gopherd.yml")
	sockPath := filepath.Join(dir, "gopherd.sock")

	// Inject control socket path into config if not already present.
	if !strings.Contains(config, "control:") {
		config = fmt.Sprintf("control:\n  socket: %s\n\n%s", sockPath, config)
	} else {
		config = strings.ReplaceAll(config, "{{SOCKET}}", sockPath)
	}
	config = strings.ReplaceAll(config, "no-logo: true\n", "")
	config = fmt.Sprintf("no-logo: true\n%s", config)

	if err := os.WriteFile(cfgPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	args := append([]string{}, extraArgs...)
	cmd := exec.Command(testBinary, args...)
	cmd.Env = append(os.Environ(), "GOPHERD_CONFIG="+cfgPath, "GOPHERD_SOCKET="+sockPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Put daemon in its own process group so we can signal it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}

	td := &testDaemon{
		cmd:        cmd,
		configPath: cfgPath,
		socketPath: sockPath,
		dir:        dir,
		t:          t,
	}

	// Wait for control socket to become available.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			conn.Close()
			return td
		}
		// Check if process already exited.
		if cmd.ProcessState != nil {
			t.Fatalf("daemon exited before socket ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	td.kill()
	t.Fatalf("daemon socket %s not ready within 5s", sockPath)
	return nil
}

// sendCommand sends a command over the control socket and returns the response.
func (td *testDaemon) sendCommand(command string) string {
	td.t.Helper()
	conn, err := net.DialTimeout("unix", td.socketPath, 2*time.Second)
	if err != nil {
		td.t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	fmt.Fprintf(conn, "%s\n", command)

	var lines []string
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return strings.Join(lines, "\n")
}

// signal sends a signal to the daemon process.
func (td *testDaemon) signal(sig syscall.Signal) {
	td.t.Helper()
	if err := td.cmd.Process.Signal(sig); err != nil {
		td.t.Fatalf("signal %v: %v", sig, err)
	}
}

// wait waits for the daemon to exit and returns the exit code.
func (td *testDaemon) wait(timeout time.Duration) int {
	td.t.Helper()
	done := make(chan error, 1)
	go func() { done <- td.cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return -1
	case <-time.After(timeout):
		td.kill()
		td.t.Fatalf("daemon did not exit within %s", timeout)
		return -1
	}
}

// kill forcefully terminates the daemon.
func (td *testDaemon) kill() {
	if td.cmd.Process != nil {
		td.cmd.Process.Signal(syscall.SIGKILL)
		td.cmd.Wait()
	}
}

// stop sends SIGTERM and waits for clean exit.
func (td *testDaemon) stop() int {
	td.signal(syscall.SIGTERM)
	return td.wait(10 * time.Second)
}

// daemonAlive returns true if the control socket still accepts a connection.
// Checking td.cmd.ProcessState would be misleading here because ProcessState
// is only populated after Wait() is called.
func (td *testDaemon) daemonAlive() bool {
	conn, err := net.DialTimeout("unix", td.socketPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// updateConfig writes a new config to the daemon's config file (for reload tests).
func (td *testDaemon) updateConfig(config string) {
	td.t.Helper()
	if !strings.Contains(config, "control:") {
		config = fmt.Sprintf("control:\n  socket: %s\n\n%s", td.socketPath, config)
	} else {
		config = strings.ReplaceAll(config, "{{SOCKET}}", td.socketPath)
	}
	config = fmt.Sprintf("no-logo: true\n%s", config)
	if err := os.WriteFile(td.configPath, []byte(config), 0o644); err != nil {
		td.t.Fatalf("update config: %v", err)
	}
}
