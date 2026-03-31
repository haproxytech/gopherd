package service

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestNewDefaults(t *testing.T) {
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
	svc := New(Process{Name: "myapp", Command: "/usr/bin/myapp"}, "")
	if svc.Name != "myapp" {
		t.Errorf("name = %q", svc.Name)
	}
}

func TestNewDisabled(t *testing.T) {
	svc := New(Process{Command: "true", Startup: "disabled"}, "")
	if svc.Enabled {
		t.Error("expected disabled")
	}
}

func TestNewOneshot(t *testing.T) {
	svc := New(Process{Command: "true", Startup: "oneshot"}, "")
	if !svc.Oneshot {
		t.Error("expected oneshot")
	}
	if !svc.Enabled {
		t.Error("oneshot should be enabled")
	}
}

func TestNewCustomStopSignal(t *testing.T) {
	svc := New(Process{Command: "true", StopSignal: "SIGUSR1"}, "")
	if svc.stopSignal != syscall.SIGUSR1 {
		t.Errorf("stopSignal = %v", svc.stopSignal)
	}
}

func TestNewExitActions(t *testing.T) {
	svc := New(Process{Command: "true", OnSuccess: "restart", OnFailure: "ignore"}, "")
	if svc.OnSuccess != ActionRestart {
		t.Errorf("onSuccess = %q", svc.OnSuccess)
	}
	if svc.OnFailure != ActionIgnore {
		t.Errorf("onFailure = %q", svc.OnFailure)
	}
}

func TestNewRequires(t *testing.T) {
	svc := New(Process{Command: "true", Requires: []string{"db", "cache"}}, "")
	if !svc.Requires["db"] || !svc.Requires["cache"] {
		t.Error("expected requires")
	}
}

func TestNewOnCheckFailure(t *testing.T) {
	svc := New(Process{
		Command:        "true",
		OnCheckFailure: map[string]string{"health": "restart", "ready": "shutdown"},
	}, "")
	if svc.OnCheckFailure["health"] != ActionRestart {
		t.Errorf("onCheckFailure[health] = %q", svc.OnCheckFailure["health"])
	}
}

func TestStartStop(t *testing.T) {
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

func TestSignalNotRunning(_ *testing.T) {
	svc := New(Process{Command: "true"}, "")
	svc.Signal(syscall.SIGUSR1) // should not panic
	svc.Stop()
}

func TestEnvironment(t *testing.T) {
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
	svc := New(Process{Command: "true", KillDelay: "30s"}, "")
	if svc.killDelay != 30*time.Second {
		t.Errorf("killDelay = %v", svc.killDelay)
	}
}
