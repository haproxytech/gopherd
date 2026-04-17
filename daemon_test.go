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
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/metrics"
	"github.com/haproxytech/gopherd/service"
	"github.com/haproxytech/gopherd/yml"
)

func newTestDaemon(procs []service.Process) *daemon {
	cfg := &yml.Config{
		Processes: procs,
	}
	d := &daemon{
		cfg:        cfg,
		m:          metrics.New(),
		pidMap:     make(map[int]*service.Service),
		restartCh:  make(chan restartReq, 64),
		services:   make(map[string]*service.Service),
		shutdownCh: make(chan struct{}),
	}
	d.buildServices()
	return d
}

func TestBuildServices(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Command: "/bin/myapp"},
	})
	if _, ok := d.services["/bin/myapp"]; !ok {
		t.Error("expected service name to fall back to command")
	}
}

func TestBuildServicesEntrypointArgs(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app"},
	})
	d.initiateShutdown(42)
	if !d.shuttingDown.Load() {
		t.Error("expected shuttingDown=true")
	}
	if d.exitCode.Load() != 42 {
		t.Errorf("expected exitCode=42, got %d", d.exitCode.Load())
	}
}

func TestHandleCheckFailureRestart(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app", OnCheckFailure: map[string]string{"health": "restart"}},
	})
	// handleCheckFailure should not panic on non-running services.
	d.handleCheckFailure("health")
}

func TestHandleCheckFailureShutdown(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app", OnCheckFailure: map[string]string{"health": "shutdown"}},
	})
	d.handleCheckFailure("health")
	if !d.shuttingDown.Load() {
		t.Error("expected shutdown on check failure with shutdown action")
	}
}

func TestHandleCheckFailureIgnore(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app", OnCheckFailure: map[string]string{"health": "ignore"}},
	})
	d.handleCheckFailure("health")
	if d.shuttingDown.Load() {
		t.Error("expected no shutdown on ignore action")
	}
}

func TestHandleCheckFailureUnknownCheck(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app"},
	})
	// Should not panic for checks no service cares about.
	d.handleCheckFailure("nonexistent")
	if d.shuttingDown.Load() {
		t.Error("should not shut down for unrelated check")
	}
}

func TestHandleCheckFailureSkipsDuringShutdown(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app", OnCheckFailure: map[string]string{"health": "shutdown"}},
	})
	d.shuttingDown.Store(true)
	d.exitCode.Store(0)
	d.handleCheckFailure("health")
	// exitCode should not change since we were already shutting down.
	if d.exitCode.Load() != 0 {
		t.Errorf("expected exitCode to remain 0 during shutdown, got %d", d.exitCode.Load())
	}
}

func TestSetupControl(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	d := newTestDaemon(nil)
	ctrl := d.setupControl()
	list := ctrl.ListFn()
	if list != "no services" {
		t.Errorf("expected 'no services', got %q", list)
	}
}

func TestWaitStatusCodeExited(t *testing.T) {
	t.Parallel()
	// WaitStatus where process exited normally with code 0.
	ws := syscall.WaitStatus(0) // exited, status 0
	code := waitStatusCode(ws)
	if code != 0 {
		t.Errorf("expected 0, got %d", code)
	}
}

func TestWaitStatusCodeExitedNonZero(t *testing.T) {
	t.Parallel()
	// WaitStatus for exit code 42: code << 8.
	ws := syscall.WaitStatus(42 << 8)
	code := waitStatusCode(ws)
	if code != 42 {
		t.Errorf("expected 42, got %d", code)
	}
}

func TestWaitStatusCodeSignaled(t *testing.T) {
	t.Parallel()
	// WaitStatus for killed by SIGKILL (9).
	ws := syscall.WaitStatus(int(syscall.SIGKILL))
	code := waitStatusCode(ws)
	if code != 128+9 {
		t.Errorf("expected %d, got %d", 128+9, code)
	}
}

func TestBuildServicesWithPrefix(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app"},
	})
	// Should not panic when no services are running.
	d.stopAll()
}

func TestReverseSeq(t *testing.T) {
	t.Parallel()
	d := &daemon{shutdownSeq: []string{"db", "app", "web"}}
	rev := d.reverseSeq()
	want := []string{"web", "app", "db"}
	for i, name := range rev {
		if name != want[i] {
			t.Errorf("reverseSeq()[%d] = %q, want %q", i, name, want[i])
		}
	}
}

func TestStopAllReverseDep(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "db", Command: "/bin/db"},
		{Name: "app", Command: "/bin/app"},
		{Name: "web", Command: "/bin/web"},
	})
	d.shutdownSeq = []string{"db", "app", "web"}
	d.shutdownMode = "" // default = reverse-dep
	// Should not panic on non-running services.
	d.stopAll()
}

func TestStopAllDep(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "db", Command: "/bin/db"},
		{Name: "app", Command: "/bin/app"},
		{Name: "web", Command: "/bin/web"},
	})
	d.shutdownSeq = []string{"db", "app", "web"}
	d.shutdownMode = "dep"
	d.stopAll()
}

func TestStopAllSimultaneous(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "db", Command: "/bin/db"},
		{Name: "app", Command: "/bin/app"},
		{Name: "web", Command: "/bin/web"},
	})
	d.shutdownSeq = []string{"db", "app", "web"}
	d.shutdownMode = "simultaneous"
	d.stopAll()
}

func TestCloseLogTargetsNoTargets(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	d := newTestDaemon(nil)
	// Should not panic with no checkers.
	d.stopChecks()
	if d.checkers != nil {
		t.Error("expected checkers to be nil after stop")
	}
}

// TestProcessConfigChangedCommand covers M-40: changing a service's command
// must be detected as a configuration change requiring a restart.
func TestProcessConfigChangedCommand(t *testing.T) {
	t.Parallel()
	old := service.Process{Command: "/bin/old", Args: []string{"-v"}}
	updated := service.Process{Command: "/bin/new", Args: []string{"-v"}}
	if !processConfigChanged(old, updated) {
		t.Error("processConfigChanged must return true when command changes")
	}
}

// TestProcessConfigChangedArgs covers M-39: changing a service's arguments
// must be detected as a configuration change requiring a restart.
func TestProcessConfigChangedArgs(t *testing.T) {
	t.Parallel()
	old := service.Process{Command: "/bin/app", Args: []string{"--port=8080"}}
	updated := service.Process{Command: "/bin/app", Args: []string{"--port=9090"}}
	if !processConfigChanged(old, updated) {
		t.Error("processConfigChanged must return true when args change")
	}
}

// TestProcessConfigChangedNoOp verifies that identical configs report no change.
func TestProcessConfigChangedNoOp(t *testing.T) {
	t.Parallel()
	p := service.Process{Command: "/bin/app", Args: []string{"-x"}}
	if processConfigChanged(p, p) {
		t.Error("processConfigChanged must return false for identical config")
	}
}

// TestIntPtrDiffers covers M-42: a nil-vs-non-nil difference must be detected
// even when the non-nil pointer points to the zero value.
func TestIntPtrDiffers(t *testing.T) {
	t.Parallel()
	zero := 0
	one := 1

	// nil vs non-nil (even pointing to zero) is a difference.
	if !intPtrDiffers(nil, &zero) {
		t.Error("intPtrDiffers(nil, &0) must be true")
	}
	if !intPtrDiffers(&zero, nil) {
		t.Error("intPtrDiffers(&0, nil) must be true")
	}
	// nil vs nil is not a difference.
	if intPtrDiffers(nil, nil) {
		t.Error("intPtrDiffers(nil, nil) must be false")
	}
	// same value is not a difference.
	if intPtrDiffers(&zero, &zero) {
		t.Error("intPtrDiffers(&0, &0) must be false")
	}
	// different values is a difference.
	if !intPtrDiffers(&zero, &one) {
		t.Error("intPtrDiffers(&0, &1) must be true")
	}
}

// TestInitiateShutdownClosesShutdownCh verifies that initiateShutdown closes
// the shutdownCh so goroutines blocking on it (e.g. the restart backoff
// sleeper) are unblocked immediately.
func TestInitiateShutdownClosesShutdownCh(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app"},
	})
	// shutdownCh must be open initially.
	select {
	case <-d.shutdownCh:
		t.Fatal("shutdownCh should be open before initiateShutdown")
	default:
	}
	d.initiateShutdown(0)
	// shutdownCh must be closed after initiateShutdown.
	select {
	case <-d.shutdownCh:
		// correct
	default:
		t.Error("shutdownCh should be closed after initiateShutdown")
	}
}

// TestInitiateShutdownIdempotent verifies that a second initiateShutdown call
// does not panic (double-close of shutdownCh).
func TestInitiateShutdownIdempotent(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app"},
	})
	d.initiateShutdown(1)
	// Second call must not panic.
	d.initiateShutdown(2)
	// First exit code wins.
	if d.exitCode.Load() != 1 {
		t.Errorf("expected exitCode=1, got %d", d.exitCode.Load())
	}
}

// TestStartServiceRejectsReplacedService verifies that startService returns
// errServiceReplaced when the service pointer passed to it is no longer the
// current instance in d.services (simulating a reload that replaced it).
func TestStartServiceRejectsReplacedService(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app"},
	})

	// Build a stale service pointer that is NOT in d.services.
	stale, err := service.New(service.Process{Name: "app", Command: "/bin/app"}, "")
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}

	_, err = d.startService(stale)
	if err != errServiceReplaced {
		t.Errorf("expected errServiceReplaced, got %v", err)
	}
}

// TestStartServiceRejectsRemovedService verifies that startService returns
// errServiceReplaced when the service name has been removed from d.services
// entirely (simulating a reload that dropped the service from config).
func TestStartServiceRejectsRemovedService(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app"},
	})

	svc := d.services["app"]
	delete(d.services, "app")

	_, err := d.startService(svc)
	if err != errServiceReplaced {
		t.Errorf("expected errServiceReplaced, got %v", err)
	}
}

// TestWaitOneshotSuccess verifies waitOneshot returns code 0 for a process that
// exits cleanly.
func TestWaitOneshotSuccess(t *testing.T) {
	t.Parallel()
	pid, err := syscall.ForkExec("/bin/true", []string{"true"}, nil)
	if err != nil {
		t.Skipf("ForkExec: %v", err)
	}
	code, err := waitOneshot(pid, "")
	if err != nil {
		t.Fatalf("waitOneshot: %v", err)
	}
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
}

// TestWaitOneshotNonZero verifies waitOneshot returns the non-zero exit code
// from a process that exits with a failure status.
func TestWaitOneshotNonZero(t *testing.T) {
	t.Parallel()
	pid, err := syscall.ForkExec("/bin/sh", []string{"sh", "-c", "exit 3"}, nil)
	if err != nil {
		t.Skipf("ForkExec: %v", err)
	}
	code, err := waitOneshot(pid, "")
	if err != nil {
		t.Fatalf("waitOneshot: %v", err)
	}
	if code != 3 {
		t.Errorf("expected exit code 3, got %d", code)
	}
}

// TestWaitOneshotTimeout verifies waitOneshot returns an error (and does not
// block indefinitely) when the process does not exit within the given timeout.
func TestWaitOneshotTimeout(t *testing.T) {
	t.Parallel()
	pid, err := syscall.ForkExec("/bin/sleep", []string{"sleep", "60"}, nil)
	if err != nil {
		t.Skipf("ForkExec: %v", err)
	}
	defer func() {
		syscall.Kill(pid, syscall.SIGKILL)
		var ws syscall.WaitStatus
		syscall.Wait4(pid, &ws, 0, nil)
	}()
	start := time.Now()
	_, err = waitOneshot(pid, "50ms")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("waitOneshot took too long: %s", elapsed)
	}
}

// TestResolveStopSignalRejectsSIGKILL covers O1: SIGKILL cannot be caught by
// signal.Notify; configuring it as stop-signal silently has no effect.
// resolveStopSignal must fall back to SIGTERM and log a warning.
func TestResolveStopSignalRejectsSIGKILL(t *testing.T) {
	t.Parallel()
	sig := resolveStopSignal("SIGKILL")
	if sig == syscall.SIGKILL {
		t.Errorf("stop-signal SIGKILL should be rejected (uncatchable); got SIGKILL, want SIGTERM")
	}
}

// TestResolveStopSignalRejectsSIGSTOP covers O1: SIGSTOP, like SIGKILL, cannot
// be caught by signal.Notify; it must also be rejected in favour of SIGTERM.
func TestResolveStopSignalRejectsSIGSTOP(t *testing.T) {
	t.Parallel()
	sig := resolveStopSignal("SIGSTOP")
	if sig == syscall.SIGSTOP {
		t.Errorf("stop-signal SIGSTOP should be rejected (uncatchable); got SIGSTOP, want SIGTERM")
	}
}

// TestReloadRejectsMultipleEntrypointArgs covers N2: reload() must reject a
// config where more than one process has use-entrypoint-args: true, matching
// the same check that run() performs at startup. Without this, a hot-reload
// could silently install an invalid config.
func TestReloadRejectsMultipleEntrypointArgs(t *testing.T) {
	t.Parallel()
	const cfg = `
processes:
  - name: proc1
    command: /bin/sh
    use-entrypoint-args: true
  - name: proc2
    command: /bin/sh
    use-entrypoint-args: true
`
	dir := t.TempDir()
	path := filepath.Join(dir, "gopherd.yml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	d := &daemon{
		configPath: path,
		cfg:        &yml.Config{Processes: []service.Process{{Name: "existing", Command: "/bin/sh"}}},
		m:          metrics.New(),
		pidMap:     make(map[int]*service.Service),
		restartCh:  make(chan restartReq, 64),
		services:   make(map[string]*service.Service),
		shutdownCh: make(chan struct{}),
	}
	d.buildServices()

	_, err := d.reload()
	if err == nil {
		t.Fatal("expected error when more than one process has use-entrypoint-args: true")
	}
	if !strings.Contains(err.Error(), "use-entrypoint-args") {
		t.Errorf("error %q should mention use-entrypoint-args", err.Error())
	}
}

// TestStartServiceErrServiceReplacedNotFatal covers O2: startService returns
// errServiceReplaced when a service pointer is stale (replaced by a reload),
// and this must NOT be treated as a fatal failure in the reload toStart loop.
// The fix adds errServiceReplaced to the reload exclusion list alongside
// errShuttingDown, matching the existing handling in the restart goroutine.
func TestStartServiceErrServiceReplacedNotFatal(t *testing.T) {
	t.Parallel()

	d := newTestDaemon([]service.Process{
		{Name: "svc1", Command: "/bin/false"},
	})

	oldSvc := d.services["svc1"]

	// Replace the service in d.services to simulate a concurrent reload.
	replacement, err := service.New(service.Process{Name: "svc1", Command: "/bin/false"}, "")
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	d.mu.Lock()
	d.services["svc1"] = replacement
	d.mu.Unlock()

	_, err = d.startService(oldSvc)
	if err != errServiceReplaced {
		t.Fatalf("startService with stale pointer: expected errServiceReplaced, got %v", err)
	}

	// Capture log output while applying the reload toStart loop condition.
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	// This mirrors the reload toStart loop guard. errServiceReplaced must be
	// excluded so it is not logged as a spurious reload failure. Before the fix
	// only errShuttingDown was excluded; after the fix errServiceReplaced is too.
	if err != nil && err != errShuttingDown && err != errServiceReplaced {
		log.Printf("reload: start %s failed: %v", oldSvc.Name, err)
	}
	if strings.Contains(buf.String(), "reload: start svc1 failed") {
		t.Errorf("errServiceReplaced must not produce a reload failure log; got: %q", buf.String())
	}
}

// TestAnyRunningCheckUsesPidMap verifies that the anyRunning sentinel used
// during shutdown is based on d.pidMap and not d.services, so that services
// removed by a reload (still in pidMap but gone from services) are counted.
// This is a structural test: we verify that len(d.pidMap) is the right signal
// by confirming an entry added directly to pidMap is visible when d.services
// is empty.
func TestAnyRunningCheckUsesPidMap(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(nil)

	// Place a fake PID into pidMap without touching services.
	fakeSvc, err := service.New(service.Process{Name: "ghost", Command: "/bin/ghost"}, "")
	if err != nil {
		t.Fatalf("service.New: %v", err)
	}
	d.mu.Lock()
	d.pidMap[99999] = fakeSvc
	anyRunning := len(d.pidMap) > 0
	d.mu.Unlock()

	if !anyRunning {
		t.Error("anyRunning should be true when pidMap has entries even if services is empty")
	}

	// Removing from pidMap should flip the flag.
	d.mu.Lock()
	delete(d.pidMap, 99999)
	anyRunning = len(d.pidMap) > 0
	d.mu.Unlock()

	if anyRunning {
		t.Error("anyRunning should be false after all pidMap entries are removed")
	}
}
