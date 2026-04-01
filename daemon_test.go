package main

import (
	"strings"
	"syscall"
	"testing"

	"github.com/haproxytech/gopherd/metrics"
	"github.com/haproxytech/gopherd/service"
	"github.com/haproxytech/gopherd/yml"
)

func newTestDaemon(procs []service.Process) *daemon {
	cfg := &yml.Config{
		Processes: procs,
	}
	d := &daemon{
		cfg:       cfg,
		m:         metrics.New(),
		pidMap:    make(map[int]*service.Service),
		restartCh: make(chan restartReq, 64),
		services:  make(map[string]*service.Service),
	}
	d.buildServices()
	return d
}

func TestBuildServices(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "web", Command: "/bin/web"},
		{Name: "worker", Command: "/bin/worker"},
	})
	if len(d.services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(d.services))
	}
	if _, ok := d.services["web"]; !ok {
		t.Error("missing service 'web'")
	}
	if _, ok := d.services["worker"]; !ok {
		t.Error("missing service 'worker'")
	}
}

func TestBuildServicesNameFallback(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Command: "/bin/myapp"},
	})
	if _, ok := d.services["/bin/myapp"]; !ok {
		t.Error("expected service name to fall back to command")
	}
}

func TestBuildServicesEntrypointArgs(t *testing.T) {
	d := &daemon{
		cfg: &yml.Config{
			Processes: []service.Process{
				{Name: "app", Command: "/bin/app", Args: []string{"--base"}, UseEntrypointArgs: true},
				{Name: "sidecar", Command: "/bin/sidecar"},
			},
		},
		m:              metrics.New(),
		pidMap:         make(map[int]*service.Service),
		restartCh:      make(chan restartReq, 64),
		entrypointArgs: []string{"--extra1", "--extra2"},
	}
	d.buildServices()

	app := d.services["app"]
	if app == nil {
		t.Fatal("missing service 'app'")
	}
	// The process args should include the entrypoint args.
	if app.Proc.Args[len(app.Proc.Args)-1] != "--extra2" {
		t.Errorf("expected entrypoint args appended, got %v", app.Proc.Args)
	}

	sidecar := d.services["sidecar"]
	if sidecar == nil {
		t.Fatal("missing service 'sidecar'")
	}
	if len(sidecar.Proc.Args) != 0 {
		t.Errorf("sidecar should have no args, got %v", sidecar.Proc.Args)
	}
}

func TestBuildServicesNoEntrypointArgsWhenEmpty(t *testing.T) {
	d := &daemon{
		cfg: &yml.Config{
			Processes: []service.Process{
				{Name: "app", Command: "/bin/app", Args: []string{"--base"}, UseEntrypointArgs: true},
			},
		},
		m:              metrics.New(),
		pidMap:         make(map[int]*service.Service),
		restartCh:      make(chan restartReq, 64),
		entrypointArgs: nil,
	}
	d.buildServices()

	app := d.services["app"]
	if len(app.Proc.Args) != 1 || app.Proc.Args[0] != "--base" {
		t.Errorf("expected only base args when entrypointArgs is empty, got %v", app.Proc.Args)
	}
}

func TestStartOrder(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "web", Command: "/bin/web", After: []string{"db"}},
		{Name: "db", Command: "/bin/db"},
	})
	ord, err := d.startOrder()
	if err != nil {
		t.Fatalf("startOrder: %v", err)
	}
	if len(ord) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(ord))
	}
	// db must come before web.
	dbIdx, webIdx := -1, -1
	for i, name := range ord {
		if name == "db" {
			dbIdx = i
		}
		if name == "web" {
			webIdx = i
		}
	}
	if dbIdx >= webIdx {
		t.Errorf("expected db before web, got order %v", ord)
	}
}

func TestStartOrderCycle(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "a", Command: "/bin/a", After: []string{"b"}},
		{Name: "b", Command: "/bin/b", After: []string{"a"}},
	})
	_, err := d.startOrder()
	if err == nil {
		t.Error("expected error for cyclic dependencies")
	}
}

func TestInitiateShutdown(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app"},
	})
	d.initiateShutdown(42)
	if !d.shuttingDown {
		t.Error("expected shuttingDown=true")
	}
	if d.exitCode != 42 {
		t.Errorf("expected exitCode=42, got %d", d.exitCode)
	}
}

func TestHandleCheckFailureRestart(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app", OnCheckFailure: map[string]string{"health": "restart"}},
	})
	// handleCheckFailure should not panic on non-running services.
	d.handleCheckFailure("health")
}

func TestHandleCheckFailureShutdown(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app", OnCheckFailure: map[string]string{"health": "shutdown"}},
	})
	d.handleCheckFailure("health")
	if !d.shuttingDown {
		t.Error("expected shutdown on check failure with shutdown action")
	}
}

func TestHandleCheckFailureIgnore(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app", OnCheckFailure: map[string]string{"health": "ignore"}},
	})
	d.handleCheckFailure("health")
	if d.shuttingDown {
		t.Error("expected no shutdown on ignore action")
	}
}

func TestHandleCheckFailureUnknownCheck(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app"},
	})
	// Should not panic for checks no service cares about.
	d.handleCheckFailure("nonexistent")
	if d.shuttingDown {
		t.Error("should not shut down for unrelated check")
	}
}

func TestHandleCheckFailureSkipsDuringShutdown(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app", OnCheckFailure: map[string]string{"health": "shutdown"}},
	})
	d.shuttingDown = true
	d.exitCode = 0
	d.handleCheckFailure("health")
	// exitCode should not change since we were already shutting down.
	if d.exitCode != 0 {
		t.Errorf("expected exitCode to remain 0 during shutdown, got %d", d.exitCode)
	}
}

func TestSetupControl(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app"},
	})
	ctrl := d.setupControl()
	if ctrl == nil {
		t.Fatal("setupControl returned nil")
	}

	// Test StatsFn.
	stats := ctrl.StatsFn()
	if stats == "" {
		t.Error("StatsFn returned empty string")
	}

	// Test ListFn.
	list := ctrl.ListFn()
	if !strings.Contains(list, "app") {
		t.Errorf("ListFn should list 'app', got %q", list)
	}

	// Test StatusFn for existing service.
	status, err := ctrl.StatusFn("app")
	if err != nil {
		t.Errorf("StatusFn(app): %v", err)
	}
	if !strings.Contains(status, "stopped") {
		t.Errorf("expected stopped status, got %q", status)
	}

	// Test StatusFn for unknown service.
	_, err = ctrl.StatusFn("nonexistent")
	if err == nil {
		t.Error("expected error for unknown service")
	}

	// Test StopFn for non-running service.
	msg, err := ctrl.StopFn("app")
	if err != nil {
		t.Errorf("StopFn(app): %v", err)
	}
	if !strings.Contains(msg, "already stopped") {
		t.Errorf("expected already stopped, got %q", msg)
	}

	// Test SignalFn for non-running service.
	_, err = ctrl.SignalFn("app", "SIGTERM")
	if err == nil {
		t.Error("expected error signaling non-running service")
	}

	// Test unknown service for StartFn.
	_, err = ctrl.StartFn("nonexistent")
	if err == nil {
		t.Error("expected error for unknown service")
	}
}

func TestSetupControlListEmpty(t *testing.T) {
	d := newTestDaemon(nil)
	ctrl := d.setupControl()
	list := ctrl.ListFn()
	if list != "no services" {
		t.Errorf("expected 'no services', got %q", list)
	}
}

func TestWaitStatusCodeExited(t *testing.T) {
	// WaitStatus where process exited normally with code 0.
	ws := syscall.WaitStatus(0) // exited, status 0
	code := waitStatusCode(ws)
	if code != 0 {
		t.Errorf("expected 0, got %d", code)
	}
}

func TestWaitStatusCodeExitedNonZero(t *testing.T) {
	// WaitStatus for exit code 42: code << 8.
	ws := syscall.WaitStatus(42 << 8)
	code := waitStatusCode(ws)
	if code != 42 {
		t.Errorf("expected 42, got %d", code)
	}
}

func TestWaitStatusCodeSignaled(t *testing.T) {
	// WaitStatus for killed by SIGKILL (9).
	ws := syscall.WaitStatus(int(syscall.SIGKILL))
	code := waitStatusCode(ws)
	if code != 128+9 {
		t.Errorf("expected %d, got %d", 128+9, code)
	}
}

func TestBuildServicesWithPrefix(t *testing.T) {
	cfg := &yml.Config{
		Prefix:    "global",
		Processes: []service.Process{{Name: "app", Command: "/bin/app"}},
	}
	d := &daemon{
		cfg:       cfg,
		m:         metrics.New(),
		pidMap:    make(map[int]*service.Service),
		restartCh: make(chan restartReq, 64),
	}
	d.buildServices()
	if _, ok := d.services["app"]; !ok {
		t.Error("missing service 'app'")
	}
}

func TestStopAllNoRunning(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app"},
	})
	// Should not panic when no services are running.
	d.stopAll()
}

func TestCloseLogTargetsNoTargets(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app"},
	})
	// Should not panic with no log targets.
	d.closeLogTargets()
	if d.logTargets != nil {
		t.Error("expected logTargets to be nil after close")
	}
}

func TestStopChecksEmpty(t *testing.T) {
	d := newTestDaemon(nil)
	// Should not panic with no checkers.
	d.stopChecks()
	if d.checkers != nil {
		t.Error("expected checkers to be nil after stop")
	}
}
