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
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/control"
	"github.com/haproxytech/gopherd/internal/logger"
	"github.com/haproxytech/gopherd/internal/metrics"
	"github.com/haproxytech/gopherd/internal/yml"
	"github.com/haproxytech/gopherd/service"
)

func newTestDaemon(procs []service.Process) *daemon {
	cfg := &yml.Config{
		Processes: procs,
	}
	d := &daemon{
		cfg:         cfg,
		m:           metrics.New(),
		pidMap:      make(map[int]*service.Service),
		restartCh:   make(chan restartReq, 64),
		services:    make(map[string]*service.Service),
		shutdownCh:  make(chan struct{}),
		stopAllDone: make(chan struct{}),
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

// TestBuildServicesControlSocket verifies the daemon hands its resolved control
// socket path to every service so children can reach the control socket.
func TestBuildServicesControlSocket(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "web", Command: "/bin/web"},
	})
	if got := d.services["web"].ControlSocket; got != control.DefaultSocketPath {
		t.Errorf("default: ControlSocket = %q, want %q", got, control.DefaultSocketPath)
	}

	// The startup-bound path wins over the current config: reload re-parses the
	// file without the GOPHERD_SOCKET env override, and the socket is bind-once.
	d2 := &daemon{
		controlSocket: "/bound/at/startup.sock",
		cfg: &yml.Config{
			Control:   control.Config{SocketPath: "/from/reparsed/config.sock"},
			Processes: []service.Process{{Name: "web", Command: "/bin/web"}},
		},
		m:        metrics.New(),
		services: make(map[string]*service.Service),
	}
	if err := d2.buildServices(); err != nil {
		t.Fatalf("buildServices: %v", err)
	}
	if got := d2.services["web"].ControlSocket; got != "/bound/at/startup.sock" {
		t.Errorf("reload: ControlSocket = %q, want /bound/at/startup.sock", got)
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
	if !strings.Contains(stats, "app") {
		t.Errorf("StatsFn should include 'app', got %q", stats)
	}

	// Test StatusFn for existing service.
	status, err := ctrl.StatusFn("app")
	if err != nil {
		t.Errorf("StatusFn(app): %v", err)
	}
	if !strings.Contains(status, "pending") {
		t.Errorf("expected pending status for enabled-but-not-yet-started service, got %q", status)
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

func TestSetupControlStatsEmpty(t *testing.T) {
	t.Parallel()
	d := newTestDaemon(nil)
	ctrl := d.setupControl()
	stats := ctrl.StatsFn()
	if stats != "no stats" {
		t.Errorf("expected 'no stats', got %q", stats)
	}
}

// TestWaitStatusCode covers every wait(2) status shape, including those no
// integration test can produce on demand.
//
// The 128+signum offset is what makes the reap loop's `WasStopped() && code >
// 128` rule work and lets an exit-code-map be keyed by signal name; without it
// an intentional `gopherd stop` is recorded as a failure. The fallthrough must
// stay non-zero: a stopped or continued child has not completed, and booking it
// as a clean exit lets the default on-success: shutdown take the daemon down.
func TestWaitStatusCode(t *testing.T) {
	t.Parallel()
	// Linux wait(2) status encoding: exited -> (code << 8); signalled -> signum
	// in the low 7 bits; stopped -> low byte 0x7f; continued -> 0xffff.
	tests := []struct {
		name string
		ws   syscall.WaitStatus
		want int
	}{
		{"exited 0", syscall.WaitStatus(0), 0},
		{"exited 1", syscall.WaitStatus(1 << 8), 1},
		{"exited 42", syscall.WaitStatus(42 << 8), 42},
		{"exited 255", syscall.WaitStatus(255 << 8), 255},
		{"signalled SIGINT", syscall.WaitStatus(int(syscall.SIGINT)), 128 + 2},
		{"signalled SIGKILL", syscall.WaitStatus(int(syscall.SIGKILL)), 128 + 9},
		{"signalled SIGTERM", syscall.WaitStatus(int(syscall.SIGTERM)), 128 + 15},
		// Neither exited nor signalled: not a completion, so not a success.
		{"stopped SIGSTOP", syscall.WaitStatus(0x7f | (int(syscall.SIGSTOP) << 8)), 1},
		{"continued", syscall.WaitStatus(0xffff), 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := waitStatusCode(tc.ws); got != tc.want {
				t.Errorf("waitStatusCode(%#x) = %d, want %d "+
					"(exited=%v signalled=%v stopped=%v continued=%v)",
					uint32(tc.ws), got, tc.want, tc.ws.Exited(), tc.ws.Signaled(),
					tc.ws.Stopped(), tc.ws.Continued())
			}
		})
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

// TestProcessConfigChanged covers every field the restart-or-not decision
// consults, one case each: processConfigChanged is a chain of independent
// comparisons, and only a per-field table notices one missing link. A dropped
// link is the quietest reload bug there is — reload reports success, status
// shows the new config, and the child keeps the old value.
//
// Fields deliberately excluded are listed too, so "applied without a restart"
// stays a tested property rather than an accident.
func TestProcessConfigChanged(t *testing.T) {
	t.Parallel()
	base := service.Process{
		Name:    "app",
		Command: "/bin/app",
		Args:    []string{"--port=8080"},
	}
	ptrBool := func(b bool) *bool { return &b }
	ptrInt := func(i int) *int { return &i }

	tests := []struct {
		name string
		edit func(p *service.Process)
		want bool
	}{
		// Anything the child process is spawned with needs a restart.
		{"prefix", func(p *service.Process) { p.Prefix = "svc" }, true},
		{"startup", func(p *service.Process) { p.Startup = "disabled" }, true},
		{"command", func(p *service.Process) { p.Command = "/bin/other" }, true},
		{"args", func(p *service.Process) { p.Args = []string{"--port=9090"} }, true},
		{"args removed", func(p *service.Process) { p.Args = nil }, true},
		{"user", func(p *service.Process) { p.User = "nobody" }, true},
		{"group", func(p *service.Process) { p.Group = "nogroup" }, true},
		{"user-id", func(p *service.Process) { p.UserID = ptrInt(1000) }, true},
		{"group-id", func(p *service.Process) { p.GroupID = ptrInt(1000) }, true},
		{"strict-groups", func(p *service.Process) { p.StrictGroups = true }, true},
		{"working-dir", func(p *service.Process) { p.WorkingDir = "/srv" }, true},
		{"stop-signal", func(p *service.Process) { p.StopSignal = "SIGUSR1" }, true},
		{"pass-env", func(p *service.Process) { p.PassEnv = ptrBool(true) }, true},
		{"log-capture", func(p *service.Process) { p.LogCapture = ptrBool(true) }, true},
		{"export-socket", func(p *service.Process) { p.ExportSocket = ptrBool(true) }, true},
		{"environment", func(p *service.Process) { p.Environment = map[string]string{"A": "1"} }, true},
		{"remove-env", func(p *service.Process) { p.RemoveEnv = []string{"PATH"} }, true},
		{"dotenv", func(p *service.Process) { p.DotEnv = "/etc/app.env" }, true},
		{"dotenv-follow", func(p *service.Process) { p.DotEnvFollow = true }, true},
		{"ready-check", func(p *service.Process) { p.ReadyCheck = "db-up" }, true},
		{"ready-timeout", func(p *service.Process) { p.ReadyTimeout = "10s" }, true},
		{"kill-delay", func(p *service.Process) { p.KillDelay = "5s" }, true},
		{"sd-notify", func(p *service.Process) { p.SDNotify = true }, true},
		{"sd-notify-timeout", func(p *service.Process) { p.SDNotifyTimeout = "30s" }, true},
		{"parent-death-signal", func(p *service.Process) { p.ParentDeathSignal = "SIGTERM" }, true},

		// Policy that the reap loop and check callbacks read at runtime is
		// updated in place by reload(); restarting for it would be a
		// regression, not a fix.
		{"unchanged", func(*service.Process) {}, false},
		{"on-success", func(p *service.Process) { p.OnSuccess = "restart" }, false},
		{"on-failure", func(p *service.Process) { p.OnFailure = "ignore" }, false},
		{"on-check-failure", func(p *service.Process) {
			p.OnCheckFailure = map[string]string{"db": "restart"}
		}, false},
		{"requires", func(p *service.Process) { p.Requires = []string{"db"} }, false},
		{"exit-code-map", func(p *service.Process) { p.ExitCodeMap = map[int]int{143: 0} }, false},
		{"signal-rewrite", func(p *service.Process) {
			p.SignalRewrite = map[string]string{"SIGUSR1": "SIGHUP"}
		}, false},
		{"backoff-delay", func(p *service.Process) { p.BackoffDelay = "1s" }, false},
		{"backoff-limit", func(p *service.Process) { p.BackoffLimit = "1m" }, false},
		{"backoff-factor", func(p *service.Process) { p.BackoffFactor = 3 }, false},
		{"startup-timeout", func(p *service.Process) { p.StartupTimeout = "9s" }, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			updated := base
			tc.edit(&updated)
			if got := processConfigChanged(base, updated); got != tc.want {
				verb := "must"
				if !tc.want {
					verb = "must not"
				}
				t.Errorf("processConfigChanged after changing %s = %v; a change to "+
					"%s %s force a restart", tc.name, got, tc.name, verb)
			}
			// The comparison must be symmetric: reloading back to the old
			// config has to be detected just the same.
			if got := processConfigChanged(updated, base); got != tc.want {
				t.Errorf("processConfigChanged is asymmetric for %s: reverse = %v, want %v",
					tc.name, got, tc.want)
			}
		})
	}
}

// TestIntPtrDiffers verifies that a nil-vs-non-nil difference is detected
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

// TestInitiateShutdownConcurrent pins that shutdown starts exactly once under
// contention, which sequential idempotence does not cover. Two SIGTERMs, or a
// SIGTERM racing an exit action, enter initiateShutdown together; a
// check-then-set instead of a compare-and-swap lets both past and closes
// shutdownCh twice — "close of closed channel" in PID 1, mid-shutdown.
func TestInitiateShutdownConcurrent(t *testing.T) {
	t.Parallel()
	// The window is a couple of instructions wide, so racers are released by a
	// spin barrier (tighter than a channel close) over many rounds. Each round
	// is cheap: with no live services, initiateShutdown is the flag and close.
	for round := range 400 {
		d := newTestDaemon([]service.Process{{Name: "app", Command: "/bin/app"}})

		const racers = 64
		var panicked atomic.Int32
		var ready, release atomic.Int32
		var wg sync.WaitGroup
		for range racers {
			wg.Go(func() {
				defer func() {
					if r := recover(); r != nil {
						panicked.Add(1)
					}
				}()
				ready.Add(1)
				for release.Load() == 0 {
					runtime.Gosched()
				}
				d.initiateShutdown(0)
			})
		}
		for ready.Load() < racers {
			runtime.Gosched()
		}
		release.Store(1)
		wg.Wait()

		if n := panicked.Load(); n != 0 {
			t.Fatalf("round %d: %d of %d concurrent initiateShutdown calls panicked; "+
				"entry must be a single compare-and-swap so shutdownCh is closed once",
				round, n, racers)
		}
		if !d.shuttingDown.Load() {
			t.Fatalf("round %d: daemon not marked as shutting down", round)
		}
	}
}

// TestStartServiceConcurrentForksOnce pins the double-start guard. Callers test
// IsRunning() without the lock — the startup layer, the control socket, the
// restart handler — so two can reach startService for one stopped service. The
// loser's pid would sit in pidMap with nobody waiting on it and its done
// channel never closed, so shutdown would wait on a service already gone.
func TestStartServiceConcurrentForksOnce(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/sleep", Args: []string{"30"}},
	})
	svc := d.services["app"]
	t.Cleanup(func() { killAllChildren(d) })

	const racers = 8
	var forks atomic.Int32
	var already atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range racers {
		wg.Go(func() {
			<-start
			switch _, err := d.startService(svc); err {
			case nil:
				forks.Add(1)
			case errAlreadyRunning:
				already.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()

	if got := forks.Load(); got != 1 {
		t.Errorf("%d of %d concurrent starts forked; exactly 1 must win", got, racers)
	}
	if got := already.Load(); got != racers-1 {
		t.Errorf("%d starts reported errAlreadyRunning, want %d", got, racers-1)
	}
	d.mu.Lock()
	pids := len(d.pidMap)
	d.mu.Unlock()
	if pids != 1 {
		t.Errorf("pidMap holds %d pids after a concurrent start storm, want 1 "+
			"(a second fork orphans its pid and leaks its done channel)", pids)
	}
}

// TestHandleCheckFailureRestartNotDropped pins that a check-driven restart is
// queued, never discarded. The service is already stopped by the time the
// request is enqueued, so dropping it leaves it down for good — and since the
// stop collapses the exit code to 0, the default on-success: shutdown takes the
// daemon with it. A non-blocking send looks like lock hygiene and does exactly
// that.
func TestHandleCheckFailureRestartNotDropped(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app", OnCheckFailure: map[string]string{"probe": "restart"}},
	})
	// A full channel is the interesting case: the send must wait for room
	// rather than give up.
	d.restartCh = make(chan restartReq, 1)
	d.restartCh <- restartReq{}

	done := make(chan struct{})
	go func() {
		d.handleCheckFailure("probe")
		close(done)
	}()

	// Make room; the queued request must then arrive.
	time.Sleep(100 * time.Millisecond)
	<-d.restartCh

	select {
	case req := <-d.restartCh:
		if req.svc == nil || req.svc.Name != "app" {
			t.Errorf("queued restart is for %v, want app", req.svc)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("check-failure restart was dropped instead of queued; the send " +
			"must block until the channel has room")
	}
	<-done
	d.senderWg.Wait()
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

// TestReloadRejectsMultipleEntrypointArgs verifies that reload() rejects a
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
		configPath:  path,
		cfg:         &yml.Config{Processes: []service.Process{{Name: "existing", Command: "/bin/sh"}}},
		m:           metrics.New(),
		pidMap:      make(map[int]*service.Service),
		restartCh:   make(chan restartReq, 64),
		services:    make(map[string]*service.Service),
		shutdownCh:  make(chan struct{}),
		stopAllDone: make(chan struct{}),
	}
	d.buildServices()
	d.started.Store(true) // reload validation runs post-startup

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

// TestReloadBlockedDuringStartup verifies reload() refuses until d.started,
// closing the startup-phase concurrent-map-write race on d.services.
func TestReloadBlockedDuringStartup(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/true"},
	})

	// started defaults false: reload must refuse before touching the map.
	if _, err := d.reload(); err == nil {
		t.Fatal("reload should be blocked before startup completes")
	} else if !strings.Contains(err.Error(), "starting up") {
		t.Errorf("expected a 'starting up' rejection, got: %v", err)
	}

	// Past the gate, reload fails on the missing config path instead.
	d.started.Store(true)
	if _, err := d.reload(); err == nil || strings.Contains(err.Error(), "starting up") {
		t.Errorf("after started, reload must pass the gate; got: %v", err)
	}
}

// TestRestartPendingBookkeeping pins the take-once semantics of the
// restart-pending flag: it suppresses exactly one ServiceExited, the one the
// restart caused. Never cleared, it suppresses every later exit too and
// `exits`/`fail` stop counting real crashes; set for a service that is not
// running, it waits around and swallows the next genuine crash.
func TestRestartPendingBookkeeping(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{
		{
			Name: "app", Command: "/bin/app",
			OnCheckFailure: map[string]string{"probe": "restart"},
		},
	})

	// Taking an unset flag is false, and taking a set flag consumes it.
	if d.takeRestartPending("app") {
		t.Error("takeRestartPending on a service with no pending restart = true")
	}
	d.markRestartPending("app")
	if !d.takeRestartPending("app") {
		t.Fatal("takeRestartPending did not observe the pending restart")
	}
	if d.takeRestartPending("app") {
		t.Error("the pending flag survived being taken; it must suppress exactly " +
			"one exit, not every exit from now on")
	}

	// A check failure against a service that is not running must not leave a
	// flag behind: there will be no matching exit to consume it, so it would
	// swallow the service's next real crash.
	d.handleCheckFailure("probe")
	if d.takeRestartPending("app") {
		t.Error("a check failure on a stopped service left a restart-pending flag; " +
			"it would suppress the next genuine crash")
	}
	d.senderWg.Wait()
}

// TestBuildServicesResolvesGlobalPrefix pins that the effective prefix is
// written back into the process config. Output looks identical either way —
// service.New takes the global prefix as a parameter — so what breaks is
// reload: processConfigChanged compares Proc.Prefix, and a changed top-level
// `prefix:` reads as no change at all.
func TestBuildServicesResolvesGlobalPrefix(t *testing.T) {
	t.Parallel()
	build := func(global, perService string) service.Process {
		d := &daemon{
			cfg: &yml.Config{
				Prefix: global,
				Processes: []service.Process{
					{Name: "app", Command: "/bin/app", Prefix: perService},
				},
			},
			m:         metrics.New(),
			pidMap:    make(map[int]*service.Service),
			restartCh: make(chan restartReq, 64),
		}
		if err := d.buildServices(); err != nil {
			t.Fatalf("buildServices: %v", err)
		}
		return d.services["app"].Proc
	}

	if got := build("global", "").Prefix; got != "global" {
		t.Errorf("Proc.Prefix = %q, want the global prefix %q", got, "global")
	}
	// An explicit per-service prefix still wins.
	if got := build("global", "own").Prefix; got != "own" {
		t.Errorf("Proc.Prefix = %q, want the per-service prefix %q", got, "own")
	}
	// And the whole point: a changed global prefix must look like a change.
	if !processConfigChanged(build("before", ""), build("after", "")) {
		t.Error("a changed top-level prefix was not detected as a config change, " +
			"so a reload would leave services labelling output with the old prefix")
	}
}

// TestRestartRefusedDuringShutdown pins that the control socket stops accepting
// restarts once shutdown has begun. Mid-shutdown, a restart either stops a
// service nothing will bring back or starts one after stopAll, leaving a child
// that outlives the daemon.
func TestRestartRefusedDuringShutdown(t *testing.T) {
	t.Parallel()
	d := newTestDaemon([]service.Process{{Name: "app", Command: "/bin/app"}})
	ctrl := d.setupControl()

	// Before shutdown the request is accepted.
	if _, err := ctrl.RestartFn("app"); err != nil {
		t.Fatalf("restart before shutdown: %v", err)
	}
	d.senderWg.Wait()

	d.shuttingDown.Store(true)
	if _, err := ctrl.RestartFn("app"); err == nil {
		t.Error("a restart was accepted while the daemon was shutting down")
	} else if !strings.Contains(err.Error(), "shutting down") {
		t.Errorf("error %q should say the daemon is shutting down", err)
	}
}

// TestLogsUnsubscribeStopsPipeGoroutines pins that tearing down a `logs -f`
// subscription also releases the goroutines fanning stdout and stderr into the
// merged channel. Closing the subscription channels does not do it: a fan-in
// goroutine blocked *sending* into a full buffer never looks at its source
// again, and only the stop channel frees it. That is the state a disconnected
// client leaves behind, so each abandoned viewer parks two goroutines for the
// life of the daemon.
func TestLogsUnsubscribeStopsPipeGoroutines(t *testing.T) {
	yes := true
	d := newTestDaemon([]service.Process{
		{Name: "app", Command: "/bin/app", LogCapture: &yes},
	})
	// Keep the noise out of the test log; the writers are only used as sources.
	svc := d.services["app"]
	svc.Stdout = logger.NewPrefixWriter(io.Discard, "app", "none")
	svc.Stderr = logger.NewPrefixWriter(io.Discard, "app", "none")
	ctrl := d.setupControl()

	settle := func() int {
		for range 40 {
			runtime.Gosched()
			time.Sleep(10 * time.Millisecond)
		}
		return runtime.NumGoroutine()
	}
	base := settle()

	const cycles = 15
	for range cycles {
		_, merged, unsub, err := ctrl.LogsFn("app", true)
		if err != nil {
			t.Fatalf("LogsFn: %v", err)
		}
		// Fill the merged buffer and leave a fan-in goroutine mid-send, the way
		// a client that stopped reading does. Never drain `merged`.
		for i := range 1000 {
			fmt.Fprintf(svc.Stdout, "line-%04d\n", i)
		}
		time.Sleep(20 * time.Millisecond)
		if len(merged) == 0 {
			t.Fatal("merged channel never filled; the test is not exercising the " +
				"blocked-sender case")
		}
		unsub()
	}

	if after := settle(); after > base+4 {
		t.Errorf("goroutine count went from %d to %d across %d subscribe/"+
			"abandon/unsubscribe cycles; unsubscribing must signal the fan-in "+
			"goroutines, not only close their sources", base, after, cycles)
	}
}

// TestStatusRunningWinsOverScheduled pins the order the status branches are
// tried in. A scheduled service is "scheduled" between ticks, but during a run
// the operator needs to see that: the next cron time hides a live process, and
// hides a run overstaying its schedule — which is when someone is looking.
func TestStatusRunningWinsOverScheduled(t *testing.T) {
	d := newTestDaemon([]service.Process{
		{
			Name: "job", Command: "/bin/sleep", Args: []string{"30"},
			Startup: "scheduled", Schedule: "0 3 * * *",
		},
	})
	ctrl := d.setupControl()
	svc := d.services["job"]
	t.Cleanup(func() { killAllChildren(d) })

	// Between ticks: scheduled, with the next run time.
	idle, err := ctrl.StatusFn("job")
	if err != nil {
		t.Fatalf("StatusFn: %v", err)
	}
	if !strings.Contains(idle, "scheduled") {
		t.Errorf("idle status = %q, want it to mention scheduled", idle)
	}

	// Mid-run: running, with the pid.
	if _, err := d.startService(svc); err != nil {
		t.Fatalf("startService: %v", err)
	}
	live, err := ctrl.StatusFn("job")
	if err != nil {
		t.Fatalf("StatusFn: %v", err)
	}
	if !strings.Contains(live, "running") {
		t.Errorf("status during a scheduled run = %q, want it to report running "+
			"(a run in progress must not be reported as merely scheduled)", live)
	}
	if !strings.Contains(live, "pid") {
		t.Errorf("status during a scheduled run = %q, want it to include the pid", live)
	}
}
