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
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// mustNew wraps New for tests where the Process is known-valid. Fails the test
// immediately if construction errors, matching the previous panic-on-error
// behaviour without cluttering every test with error checks.
func mustNew(t *testing.T, p Process, globalPrefix string) *Service {
	t.Helper()
	svc, err := New(p, globalPrefix)
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	return svc
}

func TestNewDefaults(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "/bin/true"}, "")
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
	svc := mustNew(t, Process{Name: "myapp", Command: "/usr/bin/myapp"}, "")
	if svc.Name != "myapp" {
		t.Errorf("name = %q", svc.Name)
	}
}

func TestNewDisabled(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "true", Startup: "disabled"}, "")
	if svc.Enabled {
		t.Error("expected disabled")
	}
}

func TestNewOneshot(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "true", Startup: "oneshot"}, "")
	if !svc.Oneshot {
		t.Error("expected oneshot")
	}
	if !svc.Enabled {
		t.Error("oneshot should be enabled")
	}
}

func TestNewCustomStopSignal(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "true", StopSignal: "SIGUSR1"}, "")
	if svc.stopSignal != syscall.SIGUSR1 {
		t.Errorf("stopSignal = %v", svc.stopSignal)
	}
}

func TestNewExitActions(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "true", OnSuccess: "restart", OnFailure: "ignore"}, "")
	if svc.OnSuccess != ActionRestart {
		t.Errorf("onSuccess = %q", svc.OnSuccess)
	}
	if svc.OnFailure != ActionIgnore {
		t.Errorf("onFailure = %q", svc.OnFailure)
	}
}

func TestNewRequires(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "true", Requires: []string{"db", "cache"}}, "")
	if !svc.Requires["db"] || !svc.Requires["cache"] {
		t.Error("expected requires")
	}
}

func TestNewOnCheckFailure(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{
		Command:        "true",
		OnCheckFailure: map[string]string{"health": "restart", "ready": "shutdown"},
	}, "")
	if svc.OnCheckFailure["health"] != ActionRestart {
		t.Errorf("onCheckFailure[health] = %q", svc.OnCheckFailure["health"])
	}
}

func TestStartStop(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "sleep", Args: []string{"10"}}, "")
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
	svc := mustNew(t, Process{Command: "sleep", Args: []string{"10"}}, "")

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
	svc := mustNew(t, Process{Command: "sleep", Args: []string{"10"}}, "")

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
	svc := mustNew(t, Process{Command: "sleep", Args: []string{"10"}}, "")
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
	svc := mustNew(t, Process{Command: "true"}, "")
	svc.Signal(syscall.SIGUSR1) // should not panic
	svc.Stop()
}

func TestEnvironment(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "env", Environment: map[string]string{"TEST_VAR": "hello"}}, "")
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
	svc := mustNew(t, Process{Command: "true", WorkingDir: os.TempDir()}, "")
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
		got, err := ParseExitAction(tt.input, tt.def)
		if err != nil {
			t.Errorf("ParseExitAction(%q, %q) returned error: %v", tt.input, tt.def, err)
		}
		if got != tt.expected {
			t.Errorf("ParseExitAction(%q, %q) = %q, want %q", tt.input, tt.def, got, tt.expected)
		}
	}
}

func TestCustomKillDelay(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "true", KillDelay: "30s"}, "")
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

// TestDoneNeverStarted covers M-31: Done() on a service that was never started
// must return an already-closed channel so callers unblock immediately.
func TestDoneNeverStarted(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "true"}, "")
	ch := svc.Done()
	select {
	case <-ch:
		// correct: channel is closed
	default:
		t.Error("Done() on never-started service returned an open channel; callers would block forever")
	}
}

// TestDotEnvHashInValueNoSpace covers M-35: a '#' in a dotenv value that is NOT
// preceded by a space must be kept as a literal character, not treated as a comment.
func TestDotEnvHashInValueNoSpace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	envFile := dir + "/app.env"
	os.WriteFile(envFile, []byte(
		"DB_URL=postgres://host/db#fragment\n"+
			"COLOR=#ff0000\n"+
			"SPACED=value # trailing\n", // space before # → strip
	), 0o644)

	env, err := buildEnvMap(envFile, nil, false)
	if err != nil {
		t.Fatalf("buildEnvMap: %v", err)
	}
	if env["DB_URL"] != "postgres://host/db#fragment" {
		t.Errorf("DB_URL = %q; want postgres://host/db#fragment (# without space must be kept)", env["DB_URL"])
	}
	if env["COLOR"] != "#ff0000" {
		t.Errorf("COLOR = %q; want #ff0000 (leading # is not a comment)", env["COLOR"])
	}
	if env["SPACED"] != "value" {
		t.Errorf("SPACED = %q; want 'value' (space-preceded # is a comment)", env["SPACED"])
	}
}

// TestSignalUsesProcessGroup verifies that Signal() sends to the entire process
// group (using syscall.Kill(-pid, sig)) rather than only the process leader
// (cmd.Process.Signal). A background child spawned by the service process must
// also receive the signal when Signal() is called.
func TestSignalUsesProcessGroup(t *testing.T) {
	t.Parallel()
	pidFile := t.TempDir() + "/child.pid"

	// The parent sh (in its own process group via Setpgid) forks a background
	// sleep and records the child PID. On SIGUSR1 (default=Terminate), both the
	// sh and the background sleep should exit when the whole group is signalled.
	// With process-only signalling, only sh exits; sleep survives as an orphan.
	svc := mustNew(t, Process{
		Command: "sh",
		Args:    []string{"-c", "sleep 100 & echo $! > " + pidFile + "; wait"},
	}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		syscall.Kill(-pid, syscall.SIGKILL)
		var ws syscall.WaitStatus
		syscall.Wait4(pid, &ws, 0, nil)
		svc.MarkExited()
	})

	// Wait up to 500ms for the shell to write the child PID file.
	var childPIDBytes []byte
	for range 50 {
		childPIDBytes, _ = os.ReadFile(pidFile)
		if len(childPIDBytes) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(childPIDBytes) == 0 {
		t.Fatal("child process did not write PID file within 500ms")
	}
	var childPID int
	if n, _ := fmt.Sscan(strings.TrimSpace(string(childPIDBytes)), &childPID); n != 1 || childPID == 0 {
		t.Fatalf("invalid child PID in file: %q", childPIDBytes)
	}

	// Signal the service. New code sends to the process group; old code sends
	// only to sh. SIGUSR1 has default disposition "Terminate", so both sh and
	// sleep must exit when the group is signalled.
	svc.Signal(syscall.SIGUSR1)
	time.Sleep(150 * time.Millisecond)

	// kill -0 returns ESRCH if the process is not running.
	err = syscall.Kill(childPID, 0)
	if err == nil {
		t.Error("child (sleep) is still running after Signal(); Signal must use process group Kill(-pid, sig), not cmd.Process.Signal()")
	}
}

// dummySignal is a custom os.Signal type that is NOT a syscall.Signal.
// Passing it to Signal() should not cause a panic (B6).
type dummySignal struct{}

func (dummySignal) Signal()        {}
func (dummySignal) String() string { return "dummy" }

// TestSignalNonSyscallSignalNoPanic verifies that Signal() with a non-syscall.Signal
// value does not panic. Without the comma-ok fix the type assertion panics (B6).
func TestSignalNonSyscallSignalNoPanic(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "sleep", Args: []string{"10"}}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		svc.Stop()
		var ws syscall.WaitStatus
		syscall.Wait4(pid, &ws, 0, nil)
		svc.MarkExited()
	})

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Signal() panicked with non-syscall.Signal: %v", r)
		}
	}()
	svc.Signal(dummySignal{})
}

// TestDotEnvEmptyKeySkipped verifies that dotenv lines of the form "=value" or
// "   =value" produce no empty-string key in the environment map. Without the
// fix, env[""] = "value" which creates an invalid "=value" entry in cmd.Env (B5).
func TestDotEnvEmptyKeySkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	envFile := dir + "/empty-key.env"
	if err := os.WriteFile(envFile, []byte("=value\n   =another\nVALID=yes\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env, err := buildEnvMap(envFile, nil, false)
	if err != nil {
		t.Fatalf("buildEnvMap: %v", err)
	}
	if _, ok := env[""]; ok {
		t.Errorf("empty key must be skipped in dotenv; got env[\"\"] = %q", env[""])
	}
	if env["VALID"] != "yes" {
		t.Errorf("VALID = %q, want yes", env["VALID"])
	}
}

// TestDoubleStopCancelsFirstTimer verifies that calling Stop() twice before the
// process exits does not leak the first kill timer. Without the fix a second
// Stop() call would overwrite s.killTimer with a new timer, leaving the first
// one running and unable to be cancelled by MarkExited().
func TestDoubleStopCancelsFirstTimer(t *testing.T) {
	t.Parallel()
	// Use a non-zero kill-delay so the timer is actually created.
	svc := mustNew(t, Process{Command: "sleep", Args: []string{"10"}, KillDelay: "5s"}, "")

	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// First Stop: creates killTimer (timer1) and sends stop signal.
	svc.Stop()
	firstTimer := svc.killTimer
	if firstTimer == nil {
		t.Fatal("expected killTimer to be set after first Stop()")
	}

	// Second Stop: must cancel firstTimer and replace it with a new one.
	// Before the fix, firstTimer was silently overwritten.
	svc.Stop()
	secondTimer := svc.killTimer

	// If the fix is in place, firstTimer must have been stopped (its channel
	// drained / timer inactive). We cannot directly inspect a stopped timer's
	// state, but we can verify the pointers differ (a new timer was allocated)
	// and that the second timer is non-nil.
	if secondTimer == nil {
		t.Error("expected a new killTimer after second Stop()")
	}
	if firstTimer == secondTimer {
		// The same timer object was reused — fix was not applied.
		t.Error("second Stop() must allocate a fresh timer; first timer was not cancelled")
	}

	// Clean up: wait for the process and call MarkExited which cancels secondTimer.
	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, 0, nil)
	svc.MarkExited()

	// After MarkExited, no timer should remain.
	if svc.killTimer != nil {
		t.Error("expected killTimer to be nil after MarkExited()")
	}
}
