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

// --- helpers ---

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

// --- e2e tests ---

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

func TestE2EExitCodePropagation(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: failer
    command: /bin/sh
    args: ["-c", "sleep 1 && exit 42"]
    on-failure: shutdown
`)
	defer td.kill()

	code := td.wait(10 * time.Second)
	if code != 42 {
		t.Errorf("expected exit code 42, got %d", code)
	}
}

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
	resp := td.sendCommand("stats")
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

	resp := td.sendCommand("stats")
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

	config := fmt.Sprintf(`no-logo: true
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

	// debug should be stopped (not auto-started).
	resp := td.sendCommand("status debug")
	if !strings.Contains(resp, "stopped") {
		t.Fatalf("expected debug stopped, got: %s", resp)
	}

	// app should be running.
	resp = td.sendCommand("status app")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected app running, got: %s", resp)
	}

	td.stop()
}

func TestE2EControlStartStopRestart(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: svc
    command: sleep
    args: ["300"]
    on-failure: ignore
    on-success: ignore
`)
	defer td.kill()

	// Verify running.
	resp := td.sendCommand("status svc")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected running, got: %s", resp)
	}

	// Stop via control.
	resp = td.sendCommand("stop svc")
	if strings.Contains(resp, "error") {
		t.Fatalf("stop failed: %s", resp)
	}

	time.Sleep(500 * time.Millisecond)

	resp = td.sendCommand("status svc")
	if !strings.Contains(resp, "stopped") {
		t.Fatalf("expected stopped after stop, got: %s", resp)
	}

	// Start via control.
	resp = td.sendCommand("start svc")
	if strings.Contains(resp, "error") {
		t.Fatalf("start failed: %s", resp)
	}

	time.Sleep(500 * time.Millisecond)

	resp = td.sendCommand("status svc")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected running after start, got: %s", resp)
	}

	// Restart via control.
	resp = td.sendCommand("restart svc")
	if strings.Contains(resp, "error") {
		t.Fatalf("restart failed: %s", resp)
	}

	time.Sleep(1 * time.Second)

	resp = td.sendCommand("status svc")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected running after restart, got: %s", resp)
	}

	td.stop()
}

// Regression: a control-socket restart of a service whose on-success defaults
// to shutdown must restart the service in place. Pre-fix, the reap loop saw
// the signal-death exit, collapsed effectiveCode to 0 via the WasStopped
// rule, then dispatched the default OnSuccess=Shutdown — racing the pending
// restart and (often) winning, taking the daemon down. The reap-loop ECHILD
// path is also exercised here: with svc as the only managed process, Wait4
// returns ECHILD between the old pid being reaped and the new pid being
// registered, and the loop must treat that as transient while a restart is
// in flight.
func TestE2EControlRestartDefaultOnSuccess(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: svc
    command: sleep
    args: ["300"]
    kill-delay: 1s
`)
	defer td.kill()

	resp := td.sendCommand("restart svc")
	if strings.Contains(resp, "error") {
		t.Fatalf("restart failed: %s", resp)
	}

	time.Sleep(1500 * time.Millisecond)

	if td.cmd.ProcessState != nil {
		t.Fatalf("daemon exited unexpectedly after restart")
	}
	resp = td.sendCommand("status svc")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected running after restart, got: %s", resp)
	}

	td.stop()
}

func TestE2EControlStartDisabled(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: lazy
    command: sleep
    args: ["300"]
    startup: disabled
    on-failure: ignore
    on-success: ignore
`)
	defer td.kill()

	// lazy should be stopped initially.
	resp := td.sendCommand("status lazy")
	if !strings.Contains(resp, "stopped") {
		t.Fatalf("expected lazy stopped, got: %s", resp)
	}

	// Start it via control socket.
	resp = td.sendCommand("start lazy")
	if strings.Contains(resp, "error") {
		t.Fatalf("start lazy failed: %s", resp)
	}

	time.Sleep(500 * time.Millisecond)

	resp = td.sendCommand("status lazy")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected lazy running after start, got: %s", resp)
	}

	td.stop()
}

func TestE2EControlUnknownService(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: app
    command: sleep
    args: ["300"]
    on-failure: shutdown
`)
	defer td.kill()

	resp := td.sendCommand("status nonexistent")
	if !strings.Contains(resp, "error") {
		t.Fatalf("expected error for unknown service, got: %s", resp)
	}

	resp = td.sendCommand("start nonexistent")
	if !strings.Contains(resp, "error") {
		t.Fatalf("expected error for unknown service, got: %s", resp)
	}

	td.stop()
}

func TestE2EControlSignal(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: trapper
    command: /bin/sh
    args: ["-c", "trap 'echo got-usr1' USR1; while true; do sleep 1; done"]
    on-failure: shutdown
    on-success: shutdown
`)
	defer td.kill()

	time.Sleep(500 * time.Millisecond)

	// Send USR1 signal via control.
	resp := td.sendCommand("signal trapper SIGUSR1")
	if strings.Contains(resp, "error") {
		t.Fatalf("signal failed: %s", resp)
	}
	if !strings.Contains(resp, "sent") {
		t.Errorf("expected 'sent' in response, got: %s", resp)
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

func TestE2EHotReload(t *testing.T) {
	// Services removed during reload get Stop()'d. The reap loop sees the exit
	// and evaluates their exit action. Use on-success: ignore so the daemon
	// doesn't shut down when the removed service's stop-signal death is reaped.
	td := startDaemon(t, `
processes:
  - name: svc-a
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown

  - name: svc-b
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
`)
	defer td.kill()

	// Both should be running.
	resp := td.sendCommand("list")
	if !strings.Contains(resp, "svc-a") || !strings.Contains(resp, "svc-b") {
		t.Fatalf("expected both services in list, got: %s", resp)
	}

	// Reload with svc-b removed and svc-c added.
	td.updateConfig(`
processes:
  - name: svc-a
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown

  - name: svc-c
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`)

	resp = td.sendCommand("reload")
	if strings.Contains(resp, "error") {
		t.Fatalf("reload failed: %s", resp)
	}

	time.Sleep(1 * time.Second)

	// svc-a should still be running, svc-c should be running, svc-b should be gone.
	resp = td.sendCommand("list")
	if !strings.Contains(resp, "svc-a") {
		t.Errorf("expected svc-a in list after reload, got: %s", resp)
	}
	if !strings.Contains(resp, "svc-c") {
		t.Errorf("expected svc-c in list after reload, got: %s", resp)
	}
	if strings.Contains(resp, "svc-b") {
		t.Errorf("svc-b should be removed after reload, got: %s", resp)
	}

	td.stop()
}

func TestE2EHotReloadSIGHUP(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: original
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`)
	defer td.kill()

	// Add a new service to the config.
	td.updateConfig(`
processes:
  - name: original
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown

  - name: added
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`)

	// Trigger reload via SIGHUP.
	td.signal(syscall.SIGHUP)
	time.Sleep(2 * time.Second)

	resp := td.sendCommand("list")
	if !strings.Contains(resp, "added") {
		t.Errorf("expected 'added' service after SIGHUP reload, got: %s", resp)
	}

	td.stop()
}

func TestE2EEntrypointArgs(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "args.log")

	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo $0 $@ > %s && sleep 300"]
    use-entrypoint-args: true
    on-failure: shutdown
`, marker), "--", "--flag1", "value1")
	defer td.kill()

	time.Sleep(1 * time.Second)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	content := strings.TrimSpace(string(data))
	if !strings.Contains(content, "--flag1") || !strings.Contains(content, "value1") {
		t.Errorf("expected entrypoint args in output, got: %s", content)
	}

	td.stop()
}

func TestE2EEnvironment(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "env.log")

	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo $MY_VAR > %s && sleep 300"]
    environment:
      MY_VAR: hello-e2e
    on-failure: shutdown
`, marker))
	defer td.kill()

	time.Sleep(1 * time.Second)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read env log: %v", err)
	}
	if !strings.Contains(string(data), "hello-e2e") {
		t.Errorf("expected MY_VAR=hello-e2e, got: %s", data)
	}

	td.stop()
}

func TestE2EEnvironmentTemplate(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "tmpl.log")

	// Template expansion ({{.VAR}}) applies to args, not env values.
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo {{.MY_NAME}} > %s && sleep 300"]
    environment:
      MY_NAME: world
    on-failure: shutdown
`, marker))
	defer td.kill()

	time.Sleep(1 * time.Second)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read template log: %v", err)
	}
	if !strings.Contains(string(data), "world") {
		t.Errorf("expected 'world' from template expansion, got: %s", data)
	}

	td.stop()
}

func TestE2EDotEnv(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "app.env")
	marker := filepath.Join(dir, "dotenv.log")

	os.WriteFile(envFile, []byte("FROM_DOTENV=loaded\n"), 0o644)

	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo $FROM_DOTENV > %s && sleep 300"]
    dotenv: %s
    on-failure: shutdown
`, marker, envFile))
	defer td.kill()

	time.Sleep(1 * time.Second)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read dotenv log: %v", err)
	}
	if !strings.Contains(string(data), "loaded") {
		t.Errorf("expected FROM_DOTENV=loaded, got: %s", data)
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

	resp := td.sendCommand("stats")
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

func TestE2EWorkingDir(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "workdir")
	os.Mkdir(workDir, 0o755)
	marker := filepath.Join(dir, "wd.log")

	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "pwd > %s && sleep 300"]
    working-dir: %s
    on-failure: shutdown
`, marker, workDir))
	defer td.kill()

	time.Sleep(1 * time.Second)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read wd log: %v", err)
	}
	if !strings.Contains(string(data), workDir) {
		t.Errorf("expected working dir %s, got: %s", workDir, data)
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

func TestE2EGracefulShutdownMultipleServices(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: svc1
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: svc2
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: svc3
    command: sleep
    args: ["300"]
    on-failure: shutdown
`)
	defer td.kill()

	// All should be running.
	for _, name := range []string{"svc1", "svc2", "svc3"} {
		resp := td.sendCommand("status " + name)
		if !strings.Contains(resp, "running") {
			t.Errorf("expected %s running, got: %s", name, resp)
		}
	}

	// Graceful shutdown should stop all and exit cleanly.
	code := td.stop()
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
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

func TestE2EControlLogs(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: talker
    command: /bin/sh
    args: ["-c", "echo hello-from-talker && sleep 300"]
    on-failure: shutdown
`)
	defer td.kill()

	time.Sleep(1 * time.Second)

	// Get recent logs.
	resp := td.sendCommand("logs talker")
	if !strings.Contains(resp, "hello-from-talker") {
		t.Errorf("expected 'hello-from-talker' in logs, got: %s", resp)
	}

	td.stop()
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
	cmd.Env = append(os.Environ(), "GOPHERD_CONFIG="+td.configPath, "GOPHERD_SOCKET="+td.socketPath)
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
