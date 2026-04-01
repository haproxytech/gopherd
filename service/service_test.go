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

package service

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestNewDefaults(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "/bin/true"}, "")
	if svc.Name != "/bin/true" {
		t.Errorf("name = %q", svc.Name)
	}
	if !svc.Enabled {
		t.Error("expected enabled")
	}
	if svc.Oneshot {
		t.Error("expected oneshot=false")
	}
	if svc.OnSuccess != ActionShutdown {
		t.Errorf("onSuccess = %q", svc.OnSuccess)
	}
	if svc.OnFailure != ActionShutdown {
		t.Errorf("onFailure = %q", svc.OnFailure)
	}
}

func TestNewCustomName(t *testing.T) {
	t.Parallel()
	svc := New(Process{Name: "myapp", Command: "/usr/bin/myapp"}, "")
	if svc.Name != "myapp" {
		t.Errorf("name = %q", svc.Name)
	}
}

func TestNewDisabled(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "true", Startup: "disabled"}, "")
	if svc.Enabled {
		t.Error("expected disabled")
	}
}

func TestNewOneshot(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "true", Startup: "oneshot"}, "")
	if !svc.Oneshot {
		t.Error("expected oneshot")
	}
	if !svc.Enabled {
		t.Error("oneshot should be enabled")
	}
}

func TestNewCustomStopSignal(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "true", StopSignal: "SIGUSR1"}, "")
	if svc.stopSignal != syscall.SIGUSR1 {
		t.Errorf("stopSignal = %v", svc.stopSignal)
	}
}

func TestNewExitActions(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "true", OnSuccess: "restart", OnFailure: "ignore"}, "")
	if svc.OnSuccess != ActionRestart {
		t.Errorf("onSuccess = %q", svc.OnSuccess)
	}
	if svc.OnFailure != ActionIgnore {
		t.Errorf("onFailure = %q", svc.OnFailure)
	}
}

func TestNewRequires(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "true", Requires: []string{"db", "cache"}}, "")
	if !svc.Requires["db"] || !svc.Requires["cache"] {
		t.Error("expected requires")
	}
}

func TestNewOnCheckFailure(t *testing.T) {
	t.Parallel()
	svc := New(Process{
		Command:        "true",
		OnCheckFailure: map[string]string{"health": "restart", "ready": "shutdown"},
	}, "")
	if svc.OnCheckFailure["health"] != ActionRestart {
		t.Errorf("onCheckFailure[health] = %q", svc.OnCheckFailure["health"])
	}
}

func TestStartStop(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "sleep", Args: []string{"10"}}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if !svc.IsRunning() {
		t.Error("expected running")
	}

	svc.Stop()
	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, 0, nil)
	dur := svc.MarkExited()
	if dur <= 0 {
		t.Error("expected positive duration")
	}
	if svc.IsRunning() {
		t.Error("expected stopped")
	}
}

func TestWasStoppedFlag(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "sleep", Args: []string{"10"}}, "")

	if svc.WasStopped() {
		t.Error("WasStopped should be false before Stop()")
	}

	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	if svc.WasStopped() {
		t.Error("WasStopped should be false after Start()")
	}

	svc.Stop()

	if !svc.WasStopped() {
		t.Error("WasStopped should be true after Stop()")
	}

	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, 0, nil)
	svc.MarkExited()

	// WasStopped stays true until next Start().
	if !svc.WasStopped() {
		t.Error("WasStopped should still be true after MarkExited()")
	}
}

func TestWasStoppedResetsOnStart(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "sleep", Args: []string{"10"}}, "")

	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	svc.Stop()
	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, 0, nil)
	svc.MarkExited()

	if !svc.WasStopped() {
		t.Error("should be stopped")
	}

	// Start again — flag should reset.
	pid, err = svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if svc.WasStopped() {
		t.Error("WasStopped should reset after Start()")
	}

	svc.Stop()
	syscall.Wait4(pid, &ws, 0, nil)
	svc.MarkExited()
}

func TestSignal(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "sleep", Args: []string{"10"}}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	svc.Signal(syscall.SIGUSR1)
	if !svc.IsRunning() {
		t.Error("expected still running")
	}
	svc.Stop()
	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, 0, nil)
	svc.MarkExited()
}

func TestSignalNotRunning(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "true"}, "")
	svc.Signal(syscall.SIGUSR1) // should not panic
	svc.Stop()
}

func TestEnvironment(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "env", Environment: map[string]string{"TEST_VAR": "hello"}}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, 0, nil)
	svc.MarkExited()
}

func TestWorkingDir(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "true", WorkingDir: os.TempDir()}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, 0, nil)
	svc.MarkExited()
}

func TestParseExitActionValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		def      ExitAction
		expected ExitAction
	}{
		{"restart", ActionShutdown, ActionRestart},
		{"shutdown", ActionIgnore, ActionShutdown},
		{"success-shutdown", ActionShutdown, ActionSuccessShutdown},
		{"failure-shutdown", ActionShutdown, ActionFailureShutdown},
		{"ignore", ActionShutdown, ActionIgnore},
		{"", ActionShutdown, ActionShutdown},
		{"", ActionRestart, ActionRestart},
	}
	for _, tt := range tests {
		got := ParseExitAction(tt.input, tt.def)
		if got != tt.expected {
			t.Errorf("ParseExitAction(%q, %q) = %q, want %q", tt.input, tt.def, got, tt.expected)
		}
	}
}

func TestCustomKillDelay(t *testing.T) {
	t.Parallel()
	svc := New(Process{Command: "true", KillDelay: "30s"}, "")
	if svc.killDelay != 30*time.Second {
		t.Errorf("killDelay = %v", svc.killDelay)
	}
}

func TestExpandEnvTemplates(t *testing.T) {
	t.Setenv("MEMLIMIT", "1024")
	t.Setenv("HOST", "localhost")

	tests := []struct {
		name    string
		args    []string
		dotenv  string
		procEnv map[string]string
		want    []string
	}{
		{
			name: "no templates",
			args: []string{"-W", "-db"},
			want: []string{"-W", "-db"},
		},
		{
			name: "env expansion",
			args: []string{"-m", "{{.MEMLIMIT}}", "--host={{.HOST}}"},
			want: []string{"-m", "1024", "--host=localhost"},
		},
		{
			name: "missing env returns empty",
			args: []string{"--val={{.NONEXISTENT}}"},
			want: []string{"--val="},
		},
		{
			name:    "proc env overrides os env",
			args:    []string{"-m", "{{.MEMLIMIT}}"},
			procEnv: map[string]string{"MEMLIMIT": "2048"},
			want:    []string{"-m", "2048"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env, err := buildEnvMap(tt.dotenv, tt.procEnv, false)
			if err != nil {
				t.Fatalf("buildEnvMap: %v", err)
			}
			got, err := expandTemplates(tt.args, env, 4096)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("arg[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDotEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	envFile := dir + "/app.env"
	os.WriteFile(envFile, []byte("MEMLIMIT=1024\nHOST=example.com\n# comment\n\nEMPTY=\n"), 0o644)

	env, err := buildEnvMap(envFile, nil, false)
	if err != nil {
		t.Fatalf("buildEnvMap: %v", err)
	}
	if env["MEMLIMIT"] != "1024" {
		t.Errorf("MEMLIMIT = %q", env["MEMLIMIT"])
	}
	if env["HOST"] != "example.com" {
		t.Errorf("HOST = %q", env["HOST"])
	}

	// dotenv values available in templates
	got, err := expandTemplates([]string{"-m", "{{.MEMLIMIT}}"}, env, 4096)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if got[1] != "1024" {
		t.Errorf("got %q, want 1024", got[1])
	}
}

func TestDotEnvProcEnvOverrides(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	envFile := dir + "/app.env"
	os.WriteFile(envFile, []byte("MEMLIMIT=1024\n"), 0o644)

	env, err := buildEnvMap(envFile, map[string]string{"MEMLIMIT": "2048"}, false)
	if err != nil {
		t.Fatalf("buildEnvMap: %v", err)
	}
	if env["MEMLIMIT"] != "2048" {
		t.Errorf("proc env should override dotenv, got %q", env["MEMLIMIT"])
	}
}
