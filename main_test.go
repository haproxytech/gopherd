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
	"path/filepath"
	"strings"
	"testing"

	"github.com/haproxytech/gopherd/internal/yml"
)

func TestLoadConfigValid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "test.yml")
	os.WriteFile(cfgPath, []byte(`
processes:
  - name: app
    command: "true"
`), 0o644)

	cfg, err := yml.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Processes) != 1 {
		t.Fatalf("expected 1 process, got %d", len(cfg.Processes))
	}
	if cfg.Processes[0].Name != "app" {
		t.Errorf("process name = %q, want app", cfg.Processes[0].Name)
	}
}

func TestLoadConfigNoProcesses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "empty.yml")
	os.WriteFile(cfgPath, []byte("# empty\n"), 0o644)

	_, err := yml.Load(cfgPath)
	if err == nil {
		t.Error("expected error for config with no processes")
	}
}

func TestLoadConfigFileNotFound(t *testing.T) {
	t.Parallel()
	_, err := yml.Load("/nonexistent/path.yml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadConfigFull(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "full.yml")
	os.WriteFile(cfgPath, []byte(`
prefix: "service"

control:
  socket: /tmp/test.sock

processes:
  - name: init-task
    command: "true"
    startup: oneshot

  - name: app
    command: myapp
    args: ["--verbose"]
    working-dir: /tmp
    stop-signal: SIGUSR1
    kill-delay: 30s
    on-success: ignore
    on-failure: restart
    backoff-delay: 1s
    backoff-factor: 1.5
    backoff-limit: 60s
    after: [init-task]
    requires: [init-task]
    ready-check: app-health
    ready-timeout: 30s
    environment:
      FOO: bar
    on-check-failure:
      app-health: restart

checks:
  app-health:
    http:
      url: http://localhost:8080/health
      socket: /var/run/app.sock
    period: 5s
    timeout: 2s
    threshold: 3
    initial-delay: 10s

  tcp-check:
    tcp:
      host: localhost
      port: 5432
    period: 10s

  exec-check:
    exec:
      command: "true"

log-targets:
  syslog:
    type: syslog
    location: udp://localhost:514
    services: [app]
    labels:
      env: test
`), 0o644)

	cfg, err := yml.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Prefix != "service" {
		t.Errorf("expected Prefix=%q, got %q", "service", cfg.Prefix)
	}
	if cfg.Control.SocketPath != "/tmp/test.sock" {
		t.Errorf("socket = %q", cfg.Control.SocketPath)
	}
	if len(cfg.Processes) != 2 {
		t.Fatalf("expected 2 processes, got %d", len(cfg.Processes))
	}
	if cfg.Processes[0].Startup != "oneshot" {
		t.Errorf("init.Startup = %q", cfg.Processes[0].Startup)
	}
	app := cfg.Processes[1]
	if app.StopSignal != "SIGUSR1" {
		t.Errorf("app.StopSignal = %q", app.StopSignal)
	}
	if app.ReadyCheck != "app-health" {
		t.Errorf("app.ReadyCheck = %q", app.ReadyCheck)
	}
	if app.Environment["FOO"] != "bar" {
		t.Errorf("env FOO = %q", app.Environment["FOO"])
	}
	if len(cfg.Checks) != 3 {
		t.Fatalf("expected 3 checks, got %d", len(cfg.Checks))
	}
	if cfg.Checks["app-health"].HTTP.Socket != "/var/run/app.sock" {
		t.Errorf("http socket = %q", cfg.Checks["app-health"].HTTP.Socket)
	}
	if cfg.Checks["app-health"].InitialDelay != "10s" {
		t.Errorf("initial-delay = %q", cfg.Checks["app-health"].InitialDelay)
	}
	if len(cfg.LogTargets) != 1 {
		t.Fatalf("expected 1 log target, got %d", len(cfg.LogTargets))
	}
	if cfg.LogTargets["syslog"].Labels["env"] != "test" {
		t.Errorf("label env = %q", cfg.LogTargets["syslog"].Labels["env"])
	}
}

// TestReadConfigFileRejectsUntrustedFile pins the trust checks on the config
// path. They have no functional symptom by construction — a world-writable or
// foreign-owned config parses exactly like a safe one — so only a negative test
// keeps them in place. The file dictates what PID 1 executes and as whom, so
// anyone who can write it owns the container.
func TestReadConfigFileRejectsUntrustedFile(t *testing.T) {
	const body = "processes:\n  - name: app\n    command: \"true\"\n"

	// Accepted: owner-only and group-readable modes owned by us.
	for _, mode := range []os.FileMode{0o400, 0o600, 0o640, 0o644} {
		dir := t.TempDir()
		path := filepath.Join(dir, "ok.yml")
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if _, err := readConfigFile(path); err != nil {
			t.Errorf("mode %04o: unexpected error: %v", mode, err)
		}
	}

	// Rejected: any mode another user can write to.
	for _, mode := range []os.FileMode{0o602, 0o606, 0o642, 0o666, 0o777} {
		dir := t.TempDir()
		path := filepath.Join(dir, "loose.yml")
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		_, err := readConfigFile(path)
		if err == nil {
			t.Errorf("mode %04o: expected refusal for a world-writable config", mode)
			continue
		}
		if !strings.Contains(err.Error(), "world-writable") {
			t.Errorf("mode %04o: error %q should say the config is world-writable",
				mode, err)
		}
	}

	// Rejected: a symlinked config path. O_NOFOLLOW makes the check atomic with
	// the open, so there is no window to swap the target.
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "real.yml")
		link := filepath.Join(dir, "link.yml")
		if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if _, err := readConfigFile(link); err == nil {
			t.Error("expected refusal for a symlinked config path")
		} else if !strings.Contains(err.Error(), "symlink") {
			t.Errorf("error %q should say the config is a symlink", err)
		}
	})

	// Rejected: owned by a different, non-root uid. Only meaningful when the
	// test process can chown, i.e. running as root.
	t.Run("foreign owner", func(t *testing.T) {
		if os.Geteuid() != 0 {
			t.Skip("needs root to chown the config to another uid")
		}
		dir := t.TempDir()
		path := filepath.Join(dir, "foreign.yml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		const otherUID = 65534 // nobody
		if err := os.Chown(path, otherUID, otherUID); err != nil {
			t.Skipf("chown unavailable: %v", err)
		}
		if _, err := readConfigFile(path); err == nil {
			t.Errorf("expected refusal for a config owned by uid %d", otherUID)
		} else if !strings.Contains(err.Error(), "owned by uid") {
			t.Errorf("error %q should name the unexpected owner", err)
		}
	})
}

// TestReadConfigFileRejectsOversized verifies that readConfigFile refuses a
// config file larger than the size cap. Without the cap, a misconfigured
// GOPHERD_CONFIG or a swapped-out config could drive PID 1 into an
// unbounded allocation.
func TestReadConfigFileRejectsOversized(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "big.yml")
	huge := make([]byte, maxConfigFileSize+1)
	for i := range huge {
		huge[i] = ' '
	}
	if err := os.WriteFile(path, huge, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := readConfigFile(path); err == nil {
		t.Fatal("expected error for oversized config, got nil")
	} else if !strings.Contains(err.Error(), "size cap") {
		t.Errorf("error %q does not mention size cap", err.Error())
	}
}
