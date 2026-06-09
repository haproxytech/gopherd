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

// Package doctest is the shared test harness for gopherd: it builds the
// binary once, runs configs, and talks to the control socket. Both the root
// e2e tests and the documentation/ example tests use it.
package doctest

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/yml"
)

// Options controls how Run prepares a config before launch.
type Options struct {
	// Commands maps a placeholder command string in the example to a runnable
	// stand-in (e.g. "/usr/local/bin/myapp" -> "sleep 300"). Applied as exact
	// substring replacement on the config text before the daemon starts.
	Commands map[string]string
	// ExtraArgs are passed to the gopherd binary (e.g. passthrough mode).
	ExtraArgs []string
}

// Daemon is a running gopherd instance under test.
type Daemon struct {
	waitErr    error // result of the one cmd.Wait()
	cmd        *exec.Cmd
	t          *testing.T
	configPath string
	socketPath string
	dir        string
	reapOnce   sync.Once // guards the single cmd.Wait()
}

// reap performs the single cmd.Wait() for the process and caches its result.
// Safe to call repeatedly and from multiple goroutines: only the first call
// invokes cmd.Wait(); subsequent calls return the cached error. This prevents
// concurrent cmd.Wait() races between Wait()'s goroutine, Kill(), and Stop().
func (d *Daemon) reap() error {
	d.reapOnce.Do(func() { d.waitErr = d.cmd.Wait() })
	return d.waitErr
}

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// findRepoRoot walks up from dir to the module root (go.mod).
func findRepoRoot(dir string) (string, error) {
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// repoRoot walks up from the test's working dir to the module root (go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	root, err := findRepoRoot(dir)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return root
}

// doBuild performs the one-time binary build from the given repo root.
func doBuild(root string) {
	tmp, err := os.MkdirTemp("", "gopherd-doctest-*")
	if err != nil {
		buildErr = err
		return
	}
	binPath = filepath.Join(tmp, "gopherd")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	buildErr = cmd.Run()
}

// BuildBinary builds the gopherd binary once and returns its path.
// Suitable for use in TestMain where *testing.T is unavailable.
func BuildBinary() (string, error) {
	buildOnce.Do(func() {
		dir, err := os.Getwd()
		if err != nil {
			buildErr = err
			return
		}
		root, err := findRepoRoot(dir)
		if err != nil {
			buildErr = err
			return
		}
		doBuild(root)
	})
	return binPath, buildErr
}

// binary builds the gopherd binary once per test process.
func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() { doBuild(repoRoot(t)) })
	if buildErr != nil {
		t.Fatalf("build gopherd: %v", buildErr)
	}
	return binPath
}

// RunFile loads an example.yml from disk, applies Options, and starts it.
func RunFile(t *testing.T, path string, opts Options) *Daemon {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read example %s: %v", path, err)
	}
	return RunConfig(t, string(data), opts)
}

// RunConfig starts the daemon from an in-memory config string.
func RunConfig(t *testing.T, config string, opts Options) *Daemon {
	t.Helper()
	// Longest-first so one placeholder being a substring of another can't clobber it.
	placeholders := make([]string, 0, len(opts.Commands))
	for placeholder := range opts.Commands {
		placeholders = append(placeholders, placeholder)
	}
	sort.Slice(placeholders, func(i, j int) bool {
		if len(placeholders[i]) != len(placeholders[j]) {
			return len(placeholders[i]) > len(placeholders[j])
		}
		return placeholders[i] < placeholders[j]
	})
	for _, placeholder := range placeholders {
		config = strings.ReplaceAll(config, placeholder, opts.Commands[placeholder])
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "gopherd.yml")
	sockPath := filepath.Join(dir, "gopherd.sock")

	if strings.Contains(config, "{{SOCKET}}") {
		config = strings.ReplaceAll(config, "{{SOCKET}}", sockPath)
	} else if !strings.Contains(config, "control:") {
		config = fmt.Sprintf("control:\n  socket: %s\n\n%s", sockPath, config)
	}
	config = strings.ReplaceAll(config, "no-logo: true\n", "")
	config = "no-logo: true\n" + config

	if err := os.WriteFile(cfgPath, []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(binary(t), opts.ExtraArgs...)
	cmd.Env = append(os.Environ(), "GOPHERD_CONFIG="+cfgPath, "GOPHERD_SOCKET="+sockPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}

	d := &Daemon{cmd: cmd, configPath: cfgPath, socketPath: sockPath, dir: dir, t: t}
	t.Cleanup(d.Kill)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", sockPath)
		if err == nil {
			conn.Close()
			return d
		}
		if cmd.ProcessState != nil {
			t.Fatalf("daemon exited before socket ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	d.Kill()
	t.Fatalf("daemon socket %s not ready within 5s", sockPath)
	return nil
}

// ValidateParseFile loads an example.yml and fails on any parse/schema error.
func ValidateParseFile(t *testing.T, path string) {
	t.Helper()
	if _, err := yml.Load(path); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

// ValidateParseString parses a config string and fails on error.
func ValidateParseString(t *testing.T, config string) {
	t.Helper()
	if _, err := yml.Unmarshal([]byte(config)); err != nil {
		t.Fatalf("parse config: %v", err)
	}
}

// Command sends one control command and returns the response.
func (d *Daemon) Command(command string) string {
	d.t.Helper()
	conn, err := net.DialTimeout("unix", d.socketPath, 2*time.Second)
	if err != nil {
		d.t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	fmt.Fprintf(conn, "%s\n", command)
	var lines []string
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return strings.Join(lines, "\n")
}

// Signal sends a signal to the daemon process.
func (d *Daemon) Signal(sig syscall.Signal) {
	d.t.Helper()
	if err := d.cmd.Process.Signal(sig); err != nil {
		d.t.Fatalf("signal %v: %v", sig, err)
	}
}

// Wait waits for the daemon to exit and returns its exit code.
func (d *Daemon) Wait(timeout time.Duration) int {
	d.t.Helper()
	done := make(chan error, 1)
	go func() { done <- d.reap() }()
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
		// Kill directly, then drain the in-flight reap goroutine instead of
		// calling Kill() (which would re-enter reap and could race the select).
		if d.cmd.Process != nil {
			d.cmd.Process.Signal(syscall.SIGKILL) //nolint:errcheck
		}
		<-done
		d.t.Fatalf("daemon did not exit within %s", timeout)
		return -1
	}
}

// Kill forcefully terminates the daemon. Safe to call multiple times and after
// a normal Stop()/Wait() has already reaped (the reap is a no-op then).
func (d *Daemon) Kill() {
	if d.cmd.Process != nil {
		d.cmd.Process.Signal(syscall.SIGKILL) //nolint:errcheck
		d.reap()
	}
}

// Stop sends SIGTERM and waits for clean exit.
func (d *Daemon) Stop() int {
	d.Signal(syscall.SIGTERM)
	return d.Wait(10 * time.Second)
}

// Alive reports whether the control socket still accepts connections.
func (d *Daemon) Alive() bool {
	conn, err := net.DialTimeout("unix", d.socketPath, 500*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Dir returns the daemon's temp working directory.
func (d *Daemon) Dir() string { return d.dir }

// ConfigPath returns the path to the written config file.
func (d *Daemon) ConfigPath() string { return d.configPath }

// SocketPath returns the path to the control socket.
func (d *Daemon) SocketPath() string { return d.socketPath }

// Binary returns the path to the built gopherd binary, building it if needed.
func Binary(t *testing.T) string { return binary(t) }

// UpdateConfig rewrites the config file (for reload tests).
func (d *Daemon) UpdateConfig(config string) {
	d.t.Helper()
	if strings.Contains(config, "{{SOCKET}}") {
		config = strings.ReplaceAll(config, "{{SOCKET}}", d.socketPath)
	} else if !strings.Contains(config, "control:") {
		config = fmt.Sprintf("control:\n  socket: %s\n\n%s", d.socketPath, config)
	}
	config = "no-logo: true\n" + config
	if err := os.WriteFile(d.configPath, []byte(config), 0o644); err != nil {
		d.t.Fatalf("update config: %v", err)
	}
}
