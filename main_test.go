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
	"testing"

	"github.com/haproxytech/gopherd/yml"
)

func TestLoadConfigValid(t *testing.T) {
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
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "empty.yml")
	os.WriteFile(cfgPath, []byte("# empty\n"), 0o644)

	_, err := yml.Load(cfgPath)
	if err == nil {
		t.Error("expected error for config with no processes")
	}
}

func TestLoadConfigFileNotFound(t *testing.T) {
	_, err := yml.Load("/nonexistent/path.yml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadConfigFull(t *testing.T) {
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
