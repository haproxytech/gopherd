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

package yml

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFull(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestLoadSDNotifyFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sd-notify.yml")
	os.WriteFile(cfgPath, []byte(`
processes:
  - name: app
    command: /bin/app
    sd-notify: true
    sd-notify-timeout: 30s
`), 0o644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Processes[0].SDNotify {
		t.Error("expected SDNotify=true")
	}
	if cfg.Processes[0].SDNotifyTimeout != "30s" {
		t.Errorf("SDNotifyTimeout = %q, want 30s", cfg.Processes[0].SDNotifyTimeout)
	}
}

func TestLoadInvalidSDNotifyTimeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "bad.yml")
	os.WriteFile(cfgPath, []byte(`
processes:
  - name: app
    command: /bin/app
    sd-notify: true
    sd-notify-timeout: bogus
`), 0o644)

	if _, err := Load(cfgPath); err == nil {
		t.Error("expected error for invalid sd-notify-timeout")
	}
}

func TestLoadNoProcesses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "empty.yml")
	os.WriteFile(cfgPath, []byte("# empty\n"), 0o644)

	_, err := Load(cfgPath)
	if err == nil {
		t.Error("expected error for no processes")
	}
}

func TestLoadGlobalPrefix(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestUseEntrypointArgs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "entrypoint.yml")
	os.WriteFile(cfgPath, []byte(`
processes:
  - name: app
    command: /bin/app
    args: ["--verbose"]
    use-entrypoint-args: true
  - name: sidecar
    command: /bin/sidecar
`), 0o644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Processes[0].UseEntrypointArgs {
		t.Errorf("expected use-entrypoint-args=true for app")
	}
	if cfg.Processes[1].UseEntrypointArgs {
		t.Errorf("sidecar should have use-entrypoint-args=false")
	}
}

func TestTemplateArgsAndDotenv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "tmpl.yml")
	os.WriteFile(cfgPath, []byte(`
processes:
  - name: haproxy
    command: /usr/local/sbin/haproxy
    dotenv: /var/lib/gopherd/haproxy.env
    args: ["-W", "-db", "-m", "{{.MEMLIMIT}}", "-S", "/var/run/haproxy-master.sock,level,admin"]
`), 0o644)

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.Processes[0]
	if p.DotEnv != "/var/lib/gopherd/haproxy.env" {
		t.Errorf("dotenv = %q", p.DotEnv)
	}
	// The raw arg should contain the template literal, not be expanded yet.
	found := false
	for _, a := range p.Args {
		t.Logf("arg: %q", a)
		if a == "{{.MEMLIMIT}}" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected {{.MEMLIMIT}} in args, got %v", p.Args)
	}
}

func TestLoadFileNotFound(t *testing.T) {
	t.Parallel()
	_, err := Load("/nonexistent/path.yml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestShutdownOrderValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		value string
		want  string
	}{
		{"reverse-dep", ShutdownReverseDep},
		{"dep", ShutdownDep},
		{"simultaneous", ShutdownSimultaneous},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()
			cfg, err := Unmarshal(fmt.Appendf(nil, `
shutdown-order: %s
processes:
  - name: app
    command: /bin/app
`, tt.value))
			if err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if cfg.ShutdownOrder != tt.want {
				t.Errorf("ShutdownOrder = %q, want %q", cfg.ShutdownOrder, tt.want)
			}
		})
	}
}

func TestShutdownOrderDefault(t *testing.T) {
	t.Parallel()
	cfg, err := Unmarshal([]byte(`
processes:
  - name: app
    command: /bin/app
`))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.ShutdownOrder != "" {
		t.Errorf("expected empty default, got %q", cfg.ShutdownOrder)
	}
}

func TestShutdownOrderInvalid(t *testing.T) {
	t.Parallel()
	_, err := Unmarshal([]byte(`
shutdown-order: bogus
processes:
  - name: app
    command: /bin/app
`))
	if err == nil {
		t.Error("expected error for invalid shutdown-order")
	}
}

// TestUnmarshalRejectsEmptyCommand verifies that Unmarshal returns an error when
// a process entry has no command configured. Without the fix, gopherd would
// silently try to exec "" and fail at runtime (I1).
func TestUnmarshalRejectsEmptyCommand(t *testing.T) {
	t.Parallel()
	_, err := Unmarshal([]byte(`
processes:
  - name: nocommand
    command:
`))
	if err == nil {
		t.Error("expected error for process with empty command")
	}
}

// TestUnmarshalRejectsNamedProcessNoCommand verifies that a named process
// without a command is also rejected.
func TestUnmarshalRejectsNamedProcessNoCommand(t *testing.T) {
	t.Parallel()
	_, err := Unmarshal([]byte(`
processes:
  - name: app
`))
	if err == nil {
		t.Error("expected error for named process with no command field")
	}
}

// TestUnmarshalRejectsInvalidOnSuccess covers O10: an invalid on-success value
// must be caught by Unmarshal and returned as an error, not deferred to
// ParseExitAction's log.Fatalf which would crash the daemon at service start.
func TestUnmarshalRejectsInvalidOnSuccess(t *testing.T) {
	t.Parallel()
	_, err := Unmarshal([]byte(`
processes:
  - command: /bin/app
    on-success: bogus-action
`))
	if err == nil {
		t.Error("expected error for invalid on-success action")
	}
}

// TestUnmarshalRejectsInvalidOnFailure covers O10: same validation for on-failure.
func TestUnmarshalRejectsInvalidOnFailure(t *testing.T) {
	t.Parallel()
	_, err := Unmarshal([]byte(`
processes:
  - command: /bin/app
    on-failure: not-valid
`))
	if err == nil {
		t.Error("expected error for invalid on-failure action")
	}
}

// TestUnmarshalRejectsInvalidOnCheckFailure covers O10: same validation for
// on-check-failure map values.
func TestUnmarshalRejectsInvalidOnCheckFailure(t *testing.T) {
	t.Parallel()
	_, err := Unmarshal([]byte(`
processes:
  - command: /bin/app
    on-check-failure:
      mycheck: explode
`))
	if err == nil {
		t.Error("expected error for invalid on-check-failure action value")
	}
}

// withEnv overrides envFromOS for a single test. Restores the original on cleanup.
func withEnv(t *testing.T, env map[string]string) {
	t.Helper()
	orig := envFromOS
	envFromOS = func() map[string]string { return env }
	t.Cleanup(func() { envFromOS = orig })
}

func TestStartupLiteralUnchanged(t *testing.T) {
	t.Parallel()
	cfg, err := Unmarshal([]byte(`
processes:
  - name: svc
    command: /bin/svc
    startup: oneshot
`))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Processes[0].Startup != "oneshot" {
		t.Errorf("Startup = %q, want oneshot", cfg.Processes[0].Startup)
	}
}

func TestStartupEnvUnsetBecomesDisabled(t *testing.T) {
	withEnv(t, map[string]string{})
	cfg, err := Unmarshal([]byte(`
processes:
  - name: svc
    command: /bin/svc
    startup: "{{.START_X}}"
`))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Processes[0].Startup != "disabled" {
		t.Errorf("Startup = %q, want disabled", cfg.Processes[0].Startup)
	}
}

func TestStartupEnvEmptyBecomesDisabled(t *testing.T) {
	withEnv(t, map[string]string{"START_X": ""})
	cfg, err := Unmarshal([]byte(`
processes:
  - name: svc
    command: /bin/svc
    startup: "{{.START_X}}"
`))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Processes[0].Startup != "disabled" {
		t.Errorf("Startup = %q, want disabled", cfg.Processes[0].Startup)
	}
}

func TestStartupEnvSetToOneshot(t *testing.T) {
	withEnv(t, map[string]string{"START_X": "oneshot"})
	cfg, err := Unmarshal([]byte(`
processes:
  - name: svc
    command: /bin/svc
    startup: "{{.START_X}}"
`))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Processes[0].Startup != "oneshot" {
		t.Errorf("Startup = %q, want oneshot", cfg.Processes[0].Startup)
	}
}

func TestStartupEnvDefaultEnabled(t *testing.T) {
	withEnv(t, map[string]string{})
	cfg, err := Unmarshal([]byte(`
processes:
  - name: svc
    command: /bin/svc
    startup: "{{.START_X:-enabled}}"
`))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if cfg.Processes[0].Startup != "enabled" {
		t.Errorf("Startup = %q, want enabled", cfg.Processes[0].Startup)
	}
}

func TestStartupEnvGarbageErrors(t *testing.T) {
	withEnv(t, map[string]string{"START_X": "garbage"})
	_, err := Unmarshal([]byte(`
processes:
  - name: svc
    command: /bin/svc
    startup: "{{.START_X}}"
`))
	if err == nil {
		t.Error("expected error for invalid startup value from env var")
	}
}

func TestStartupTypoErrors(t *testing.T) {
	t.Parallel()
	_, err := Unmarshal([]byte(`
processes:
  - name: svc
    command: /bin/svc
    startup: enable
`))
	if err == nil {
		t.Error("expected error for typo in startup field")
	}
	// A plain literal typo may legitimately appear in the error message —
	// there is no secret to protect.
	if !strings.Contains(err.Error(), `"enable"`) {
		t.Errorf("literal typo error should name the offending value; got: %v", err)
	}
}

// TestStartupEnvGarbageRedactsSecret verifies that when a template expansion
// produces a disallowed value, the error message references the template text
// (which is visible in the config file) rather than the expanded value
// (which may be a secret pulled from the environment).
func TestStartupEnvGarbageRedactsSecret(t *testing.T) {
	const secret = "s3cret-value-from-env"
	withEnv(t, map[string]string{"DB_PASS": secret})
	_, err := Unmarshal([]byte(`
processes:
  - name: svc
    command: /bin/svc
    startup: "{{.DB_PASS}}"
`))
	if err == nil {
		t.Fatal("expected error for invalid startup expanded from template")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaked expanded secret into log: %v", err)
	}
	if !strings.Contains(err.Error(), "{{.DB_PASS}}") {
		t.Errorf("error should cite the raw template text; got: %v", err)
	}
}
