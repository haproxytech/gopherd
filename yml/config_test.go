package yml

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFull(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "test.yml")
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
    user-id: 1000
    group-id: 1000
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

	cfg, err := Load(cfgPath)
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
	init := cfg.Processes[0]
	if init.Startup != "oneshot" {
		t.Errorf("init.Startup = %q", init.Startup)
	}
	app := cfg.Processes[1]
	if app.StopSignal != "SIGUSR1" {
		t.Errorf("app.StopSignal = %q", app.StopSignal)
	}
	if app.ReadyCheck != "app-health" {
		t.Errorf("app.ReadyCheck = %q", app.ReadyCheck)
	}
	if app.BackoffFactor != 1.5 {
		t.Errorf("backoff-factor = %f", app.BackoffFactor)
	}
	if app.UserID == nil || *app.UserID != 1000 {
		t.Errorf("user-id = %v", app.UserID)
	}
	if app.GroupID == nil || *app.GroupID != 1000 {
		t.Errorf("group-id = %v", app.GroupID)
	}
	if app.Environment["FOO"] != "bar" {
		t.Errorf("env FOO = %q", app.Environment["FOO"])
	}
	if app.OnCheckFailure["app-health"] != "restart" {
		t.Errorf("on-check-failure = %v", app.OnCheckFailure)
	}

	if len(cfg.Checks) != 3 {
		t.Fatalf("expected 3 checks, got %d", len(cfg.Checks))
	}
	hc := cfg.Checks["app-health"]
	if hc.HTTP == nil || hc.HTTP.Socket != "/var/run/app.sock" {
		t.Errorf("http check = %v", hc.HTTP)
	}
	if hc.InitialDelay != "10s" {
		t.Errorf("initial-delay = %q", hc.InitialDelay)
	}
	tc := cfg.Checks["tcp-check"]
	if tc.TCP == nil || tc.TCP.Port != 5432 {
		t.Errorf("tcp check = %v", tc.TCP)
	}
	ec := cfg.Checks["exec-check"]
	if ec.Exec == nil || ec.Exec.Command != "true" {
		t.Errorf("exec check = %v", ec.Exec)
	}

	if len(cfg.LogTargets) != 1 {
		t.Fatalf("expected 1 log target, got %d", len(cfg.LogTargets))
	}
	lt := cfg.LogTargets["syslog"]
	if lt.Type != "syslog" {
		t.Errorf("type = %q", lt.Type)
	}
	if lt.Labels["env"] != "test" {
		t.Errorf("labels = %v", lt.Labels)
	}
}

func TestLoadMinimal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "min.yml")
	os.WriteFile(cfgPath, []byte(`
processes:
  - name: app
    command: /bin/app
`), 0o644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Processes) != 1 || cfg.Processes[0].Name != "app" {
		t.Errorf("processes = %v", cfg.Processes)
	}
	if cfg.Prefix != "" {
		t.Errorf("expected empty prefix by default, got %q", cfg.Prefix)
	}
}

func TestLoadNoProcesses(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "empty.yml")
	os.WriteFile(cfgPath, []byte("# empty\n"), 0o644)

	_, err := Load(cfgPath)
	if err == nil {
		t.Error("expected error for no processes")
	}
}

func TestLoadGlobalPrefix(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "prefix.yml")
	os.WriteFile(cfgPath, []byte(`
prefix: "none"

processes:
  - name: app
    command: /bin/app

  - name: verbose
    command: /bin/verbose
    prefix: "timestamp service"
`), 0o644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Prefix != "none" {
		t.Errorf("expected global prefix %q, got %q", "none", cfg.Prefix)
	}
	if cfg.Processes[0].Prefix != "" {
		t.Errorf("app should have empty (inherit global), got %q", cfg.Processes[0].Prefix)
	}
	if cfg.Processes[1].Prefix != "timestamp service" {
		t.Errorf("verbose should override prefix, got %q", cfg.Processes[1].Prefix)
	}
}

func TestLoadPerProcessPrefix(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "perproc.yml")
	os.WriteFile(cfgPath, []byte(`
processes:
  - name: raw
    command: /bin/raw
    prefix: "none"

  - name: reversed
    command: /bin/reversed
    prefix: "service timestamp"

  - name: normal
    command: /bin/normal
`), 0o644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Prefix != "" {
		t.Error("global prefix should be empty")
	}
	if cfg.Processes[0].Prefix != "none" {
		t.Errorf("raw should have prefix %q, got %q", "none", cfg.Processes[0].Prefix)
	}
	if cfg.Processes[1].Prefix != "service timestamp" {
		t.Errorf("reversed should have prefix %q, got %q", "service timestamp", cfg.Processes[1].Prefix)
	}
	if cfg.Processes[2].Prefix != "" {
		t.Errorf("normal should have empty prefix, got %q", cfg.Processes[2].Prefix)
	}
}

func TestExtraArgsEntrypoint(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "extra.yml")
	os.WriteFile(cfgPath, []byte(`
processes:
  - name: app
    command: /bin/app
    args: ["--verbose"]
    extra-args: entrypoint
  - name: sidecar
    command: /bin/sidecar
`), 0o644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Processes[0].ExtraArgs != "entrypoint" {
		t.Errorf("expected extra-args=entrypoint, got %q", cfg.Processes[0].ExtraArgs)
	}
	if cfg.Processes[1].ExtraArgs != "" {
		t.Errorf("sidecar should have empty extra-args, got %q", cfg.Processes[1].ExtraArgs)
	}
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load("/nonexistent/path.yml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}
