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
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

func TestPrepareStartLogCaptureWiring(t *testing.T) {
	t.Parallel()
	boolPtr := func(v bool) *bool { return &v }

	// Default (nil): direct FD passthrough, gopherd out of the output path.
	svc := mustNew(t, Process{Command: "/bin/true"}, "")
	if svc.LogCapture {
		t.Error("LogCapture should default to false")
	}
	plan, err := svc.PrepareStart()
	if err != nil {
		t.Fatalf("PrepareStart: %v", err)
	}
	if plan.cmd.Stdout != os.Stdout || plan.cmd.Stderr != os.Stderr {
		t.Error("capture off: cmd should inherit os.Stdout/os.Stderr directly")
	}

	// Opted in: output flows through the PrefixWriters.
	svc = mustNew(t, Process{Command: "/bin/true", LogCapture: boolPtr(true)}, "")
	if !svc.LogCapture {
		t.Error("LogCapture should resolve to true")
	}
	plan, err = svc.PrepareStart()
	if err != nil {
		t.Fatalf("PrepareStart: %v", err)
	}
	if plan.cmd.Stdout != svc.Stdout || plan.cmd.Stderr != svc.Stderr {
		t.Error("capture on: cmd should use the service PrefixWriters")
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

// MarkExited may be called more than once for the same exit (defensive against
// reap-path races); the second call must not panic by double-closing s.done.
func TestMarkExitedIdempotent(t *testing.T) {
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
	svc.MarkExited() // must not panic

	select {
	case <-svc.Done():
	default:
		t.Error("Done() should be closed after MarkExited")
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

// TestZeroKillDelay confirms 0 is accepted as the explicit "never escalate to
// SIGKILL" value (Stop skips the timer when killDelay is not > 0).
func TestZeroKillDelay(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "true", KillDelay: "0s"}, "")
	if svc.killDelay != 0 {
		t.Errorf("killDelay = %v, want 0", svc.killDelay)
	}
}

// TestNegativeKillDelayRejected guards the fix for the silent SIGKILL-escalation
// bug: time.ParseDuration accepts "-5s", which would slip past Stop()'s
// `killDelay > 0` check and never force-kill a process that ignores the stop
// signal, hanging shutdown. New must reject it.
func TestNegativeKillDelayRejected(t *testing.T) {
	t.Parallel()
	if _, err := New(Process{Command: "true", KillDelay: "-5s"}, ""); err == nil {
		t.Error("expected error for negative kill-delay, got nil")
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
		{
			name: "default used when var unset",
			args: []string{"--host={{.NONEXISTENT:-127.0.0.1}}"},
			want: []string{"--host=127.0.0.1"},
		},
		{
			name: "default ignored when var set",
			args: []string{"--host={{.HOST:-127.0.0.1}}"},
			want: []string{"--host=localhost"},
		},
		{
			name:    "default used when var set to empty",
			args:    []string{"--host={{.HOST:-127.0.0.1}}"},
			procEnv: map[string]string{"HOST": ""},
			want:    []string{"--host=127.0.0.1"},
		},
		{
			name: "multiple placeholders with mixed defaults",
			args: []string{"{{.HOST}}:{{.PORT:-8080}}"},
			want: []string{"localhost:8080"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// This test relies on OS env (t.Setenv MEMLIMIT / HOST), so
			// opt in to pass-env.
			env, _, err := buildEnvMap(tt.dotenv, false, tt.procEnv, true)
			if err != nil {
				t.Fatalf("buildEnvMap: %v", err)
			}
			got, err := expandTemplates(tt.args, env, 4096, 4)
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

	env, _, err := buildEnvMap(envFile, false, nil, false)
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
	got, err := expandTemplates([]string{"-m", "{{.MEMLIMIT}}"}, env, 4096, 4)
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

	env, _, err := buildEnvMap(envFile, false, map[string]string{"MEMLIMIT": "2048"}, false)
	if err != nil {
		t.Fatalf("buildEnvMap: %v", err)
	}
	if env["MEMLIMIT"] != "2048" {
		t.Errorf("proc env should override dotenv, got %q", env["MEMLIMIT"])
	}
}

// TestDoneNeverStarted verifies that Done() on a service that was never started
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

// TestDotEnvHashInValueNoSpace verifies that a '#' in a dotenv value that is NOT
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

	env, _, err := buildEnvMap(envFile, false, nil, false)
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

	// The parent sh (leads its own process group via Setsid) forks a background
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

	env, _, err := buildEnvMap(envFile, false, nil, false)
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

// TestBuildEnvMapDefaultPassEnv verifies the security-hardened default:
// when PassEnv is false (the default), gopherd's OS environment is NOT
// forwarded to the child. Operators who need inheritance must opt in with
// pass-env: true.
func TestBuildEnvMapDefaultPassEnv(t *testing.T) {
	t.Setenv("GOPHERD_TEST_ONLY_IN_OS", "leakme")
	// passEnv=false (default) → OS env is dropped
	env, _, err := buildEnvMap("", false, nil, false)
	if err != nil {
		t.Fatalf("buildEnvMap: %v", err)
	}
	if _, ok := env["GOPHERD_TEST_ONLY_IN_OS"]; ok {
		t.Error("OS env leaked into child env despite default pass-env:false")
	}
	// Explicit opt-in: pass-env:true forwards OS env
	env, _, err = buildEnvMap("", false, nil, true)
	if err != nil {
		t.Fatalf("buildEnvMap: %v", err)
	}
	if env["GOPHERD_TEST_ONLY_IN_OS"] != "leakme" {
		t.Error("explicit pass-env:true should forward OS env")
	}
}

// TestStartDefaultsToNoPassEnv verifies that a Process with PassEnv==nil
// does not forward gopherd's OS env — that only happens with an explicit
// pass-env:true opt-in.
func TestStartDefaultsToNoPassEnv(t *testing.T) {
	t.Setenv("GOPHERD_TEST_DEFAULT_CLEAN", "should-be-dropped")
	// PassEnv nil (not set in config) → effective pass-env: false
	svc := mustNew(t, Process{
		Command:     "sh",
		Args:        []string{"-c", "[ -z \"$GOPHERD_TEST_DEFAULT_CLEAN\" ]"},
		Environment: map[string]string{"PATH": "/bin:/usr/bin"},
	}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, 0, nil)
	svc.MarkExited()
	if ws.ExitStatus() != 0 {
		t.Errorf("default pass-env did not drop GOPHERD_TEST_DEFAULT_CLEAN; child saw it set (exit %d)", ws.ExitStatus())
	}
}

// TestRemoveEnvStripsKeys verifies that keys listed in RemoveEnv are
// removed from the final child env, regardless of whether they came from
// the Environment map or (when pass-env: true) OS env.
func TestRemoveEnvStripsKeys(t *testing.T) {
	t.Setenv("GOPHERD_REMOVE_OS", "from-os")
	trueVal := true // opt in to pass-env so OS-env removal is exercised too
	svc := mustNew(t, Process{
		Command:     "sh",
		Args:        []string{"-c", "[ -z \"$GOPHERD_REMOVE_OS\" ] && [ -z \"$GOPHERD_REMOVE_PROC\" ]"},
		PassEnv:     &trueVal,
		Environment: map[string]string{"GOPHERD_REMOVE_PROC": "from-proc", "KEEP_ME": "yes"},
		RemoveEnv:   []string{"GOPHERD_REMOVE_OS", "GOPHERD_REMOVE_PROC"},
	}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, 0, nil)
	svc.MarkExited()
	if ws.ExitStatus() != 0 {
		t.Errorf("remove-env did not strip listed keys from child env (exit %d)", ws.ExitStatus())
	}
}

// TestParseDotEnvRejectsOversizedFile verifies that parseDotEnv refuses a
// dotenv file larger than the size cap. Without the cap, PID 1 could be
// driven into an unbounded allocation by a misconfigured or swapped-out
// dotenv path.
func TestParseDotEnvRejectsOversizedFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.env")
	// One byte over the cap is enough to trip the guard.
	huge := make([]byte, maxDotEnvSize+1)
	for i := range huge {
		huge[i] = 'a'
	}
	if err := os.WriteFile(path, huge, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := parseDotEnv(path, false); err == nil {
		t.Fatal("expected error for oversized dotenv, got nil")
	} else if !strings.Contains(err.Error(), "size cap") {
		t.Errorf("error %q does not mention size cap", err.Error())
	}
}

// TestParseDotEnvRejectsSymlinkedAncestor verifies that parseDotEnv refuses a
// dotenv path whose intermediate directory is a symlink, not just the leaf.
// O_NOFOLLOW alone only guards the final component; a symlink higher up would
// otherwise be traversed when the open walks the path.
func TestParseDotEnvRejectsSymlinkedAncestor(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "app.env"), []byte("K=v\n"), 0o644); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := parseDotEnv(filepath.Join(link, "app.env"), false); err == nil {
		t.Fatal("expected error for symlinked ancestor, got nil")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error %q does not mention symlink", err.Error())
	}
}

// TestParseDotEnvFollowAllowsSymlinkedAncestor verifies the dotenv-follow escape
// hatch: with follow set, a symlinked ancestor (e.g. the K8s /var/run -> /run or
// ..data/ secret pattern) is accepted and the file is parsed, while follow off
// still rejects it.
func TestParseDotEnvFollowAllowsSymlinkedAncestor(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "app.env"), []byte("K=v\n"), 0o600); err != nil {
		t.Fatalf("write dotenv: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	path := filepath.Join(link, "app.env")

	if _, err := parseDotEnv(path, false); err == nil {
		t.Fatal("follow=false: expected rejection of symlinked ancestor, got nil")
	}
	env, err := parseDotEnv(path, true)
	if err != nil {
		t.Fatalf("follow=true: unexpected error: %v", err)
	}
	if env["K"] != "v" {
		t.Errorf("follow=true: K = %q, want %q", env["K"], "v")
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

// TestMarkExitedInvalidatesPidAndRunning verifies that MarkExited clears
// svc.Pid and svc.running atomically. This is the narrowing that Stop,
// Signal, and the killTimer callback rely on to avoid issuing a
// syscall.Kill against a pid the kernel has freed for reuse. If these
// stores regress, a concurrent signal path holding svc.mu would still
// see a valid pid and running==true after Wait4 has already reaped the
// service, widening the PID-reuse race window.
func TestMarkExitedInvalidatesPidAndRunning(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "sleep", Args: []string{"30"}}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if svc.Pid.Load() != int64(pid) {
		t.Fatalf("Pid atomic not set after Start: got %d, want %d", svc.Pid.Load(), pid)
	}
	if !svc.running.Load() {
		t.Fatalf("running atomic should be true after Start")
	}

	// Clean up the child before MarkExited so we're not lying to the kernel.
	syscall.Kill(pid, syscall.SIGKILL)
	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, 0, nil)

	svc.MarkExited()

	if got := svc.Pid.Load(); got != 0 {
		t.Errorf("Pid after MarkExited = %d, want 0 (must invalidate to block concurrent signals)", got)
	}
	if svc.running.Load() {
		t.Error("running after MarkExited = true, want false")
	}
}

// TestSDNotifyListenerCreatedOnStart verifies that starting a service with
// SDNotify=true allocates a listener, places its abstract socket path in
// $NOTIFY_SOCKET for the child, and that WaitSDNotifyReady unblocks as soon
// as a READY=1 datagram arrives from any sender in the same netns (here,
// the test itself). MarkExited must also close the listener so the socket
// name is released for subsequent restarts.
func TestSDNotifyListenerCreatedOnStart(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{
		Name:     "sd-notify-create",
		Command:  "sleep",
		Args:     []string{"10"},
		SDNotify: true,
	}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		syscall.Kill(pid, syscall.SIGKILL)
		var ws syscall.WaitStatus
		syscall.Wait4(pid, &ws, 0, nil)
		svc.MarkExited()
	})

	if svc.sdNotifyListener == nil {
		t.Fatal("sdNotifyListener should be set after Start when SDNotify is true")
	}
	path := svc.sdNotifyListener.Path()
	if !strings.HasPrefix(path, "@gopherd-sd-notify-") {
		t.Errorf("listener path = %q, want @gopherd-sd-notify- prefix", path)
	}

	// Impersonate the child by sending READY=1 to the abstract socket.
	// In real use the child reads $NOTIFY_SOCKET from its env; here we
	// use the listener path directly since we've already asserted the
	// env wiring elsewhere (TestSDNotifyEnvSocketSet below).
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Net: "unixgram", Name: path})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("READY=1\n")); err != nil {
		t.Fatalf("write READY: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.WaitSDNotifyReady(ctx); err != nil {
		t.Fatalf("WaitSDNotifyReady: %v", err)
	}
}

// TestSDNotifyEnvSocketSet checks that NOTIFY_SOCKET is injected into the
// spawned child's environment. We use `sh -c 'echo $NOTIFY_SOCKET'` and
// capture stdout to assert the child observed the variable.
func TestSDNotifyEnvSocketSet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out")
	svc := mustNew(t, Process{
		Name:     "sd-notify-env",
		Command:  "sh",
		Args:     []string{"-c", `printf '%s' "$NOTIFY_SOCKET" > ` + outPath},
		SDNotify: true,
	}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var ws syscall.WaitStatus
	if _, err := syscall.Wait4(pid, &ws, 0, nil); err != nil {
		t.Fatalf("wait: %v", err)
	}
	svc.MarkExited()

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read stdout capture: %v", err)
	}
	got := string(data)
	if !strings.HasPrefix(got, "@gopherd-sd-notify-") {
		t.Errorf("child $NOTIFY_SOCKET = %q, want @gopherd-sd-notify- prefix", got)
	}
}

// TestSDNotifyListenerReplacedOnRestart verifies that a second Start on the
// same Service closes the first listener and creates a fresh one, so
// stale READY state from the prior run does not leak across restarts.
func TestSDNotifyListenerReplacedOnRestart(t *testing.T) {
	// NOT parallel: repeated binds to the same abstract socket name
	// occasionally race with other parallel-started tests that also fork
	// child processes, even when the names do not overlap. Running
	// serially keeps the failure out of flaky-test territory; the listener
	// lifecycle itself is what we are verifying, not concurrent startup.
	svc := mustNew(t, Process{
		Name:     "sd-notify-restart",
		Command:  "sleep",
		Args:     []string{"10"},
		SDNotify: true,
	}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("start 1: %v", err)
	}
	first := svc.sdNotifyListener
	// Simulate exit and restart.
	syscall.Kill(pid, syscall.SIGKILL)
	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, 0, nil)
	svc.MarkExited()
	if svc.sdNotifyListener != nil {
		t.Fatal("listener should be nil after MarkExited")
	}

	pid2, err := svc.Start()
	if err != nil {
		t.Fatalf("start 2: %v", err)
	}
	t.Cleanup(func() {
		syscall.Kill(pid2, syscall.SIGKILL)
		syscall.Wait4(pid2, &ws, 0, nil)
		svc.MarkExited()
	})

	if svc.sdNotifyListener == nil {
		t.Fatal("listener should be re-created on restart")
	}
	if svc.sdNotifyListener == first {
		t.Fatal("listener should be a fresh instance, not reused from prior run")
	}
}

func TestRemapExitCode(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{
		Command:     "true",
		ExitCodeMap: map[int]int{143: 0, 137: 0, 42: 7},
	}, "")
	tests := []struct {
		in   int
		want int
	}{
		{143, 0}, {137, 0}, {42, 7}, {0, 0}, {1, 1}, {99, 99},
	}
	for _, tc := range tests {
		if got := svc.RemapExitCode(tc.in); got != tc.want {
			t.Errorf("RemapExitCode(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestRemapExitCodeEmptyMap(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{Command: "true"}, "")
	// Empty map must pass through unchanged.
	for _, code := range []int{0, 1, 42, 143, 255} {
		if got := svc.RemapExitCode(code); got != code {
			t.Errorf("RemapExitCode(%d) on empty map = %d, want unchanged", code, got)
		}
	}
}

func TestRewriteSignalOptsIn(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{
		Command:       "true",
		SignalRewrite: map[string]string{"SIGUSR1": "SIGHUP", "SIGTERM": "SIGQUIT"},
	}, "")
	got, ok := svc.RewriteSignal(syscall.SIGUSR1)
	if !ok || got != syscall.SIGHUP {
		t.Errorf("RewriteSignal(USR1) = (%v, %v), want (SIGHUP, true)", got, ok)
	}
	got, ok = svc.RewriteSignal(syscall.SIGTERM)
	if !ok || got != syscall.SIGQUIT {
		t.Errorf("RewriteSignal(TERM) = (%v, %v), want (SIGQUIT, true)", got, ok)
	}
}

func TestRewriteSignalNoEntryMeansNoForward(t *testing.T) {
	t.Parallel()
	// The default "no signal-rewrite" behaviour must mean "do not forward".
	// A regression here would re-enable the pre-3d blast-every-signal-to-
	// every-service behaviour.
	svc := mustNew(t, Process{Command: "true"}, "")
	if _, ok := svc.RewriteSignal(syscall.SIGUSR1); ok {
		t.Error("empty signal-rewrite should not forward any signal")
	}

	// A map that does not list the received signal must likewise not forward.
	svc2 := mustNew(t, Process{
		Command:       "true",
		SignalRewrite: map[string]string{"SIGUSR1": "SIGUSR1"},
	}, "")
	if _, ok := svc2.RewriteSignal(syscall.SIGUSR2); ok {
		t.Error("signal-rewrite without SIGUSR2 entry should not forward SIGUSR2")
	}
}

// TestExpandCPUTemplates verifies that the {{cpu}} regex matches bare
// {{cpu}} and {{cpu EXPR}} but leaves identifiers like "cpus" or "cpu_x"
// as literal text (so a user typo does not produce a confusing
// "invalid cpu expression" error at service start).
func TestExpandCPUTemplates(t *testing.T) {
	t.Parallel()
	env := map[string]string{}
	tests := []struct {
		in   string
		want string
	}{
		{"--threads={{cpu}}", "--threads=8"},
		{"--threads={{cpu 50%}}", "--threads=4"},
		{"--threads={{cpu 50% - 1}}", "--threads=3"},
		// `{{cpus 50%}}` must NOT match cpuRe: the token after `cpu` is
		// part of an identifier, not whitespace. Left verbatim.
		{"--note={{cpus 50%}}", "--note={{cpus 50%}}"},
		{"--note={{cpu_x}}", "--note={{cpu_x}}"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := expandTemplates([]string{tt.in}, env, 1024, 8)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got[0] != tt.want {
				t.Errorf("expand(%q) = %q, want %q", tt.in, got[0], tt.want)
			}
		})
	}
}
