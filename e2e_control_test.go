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
	"strings"
	"testing"
	"time"
)

func TestE2EControlStartStopRestart(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: svc
    command: sleep
    args: ["300"]
    on-failure: ignore
    on-success: ignore
`)
	defer td.kill()

	// Verify running.
	resp := td.sendCommand("status svc")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected running, got: %s", resp)
	}

	// Stop via control.
	resp = td.sendCommand("stop svc")
	if strings.Contains(resp, "error") {
		t.Fatalf("stop failed: %s", resp)
	}

	time.Sleep(500 * time.Millisecond)

	resp = td.sendCommand("status svc")
	if !strings.Contains(resp, "stopped") {
		t.Fatalf("expected stopped after stop, got: %s", resp)
	}

	// Start via control.
	resp = td.sendCommand("start svc")
	if strings.Contains(resp, "error") {
		t.Fatalf("start failed: %s", resp)
	}

	time.Sleep(500 * time.Millisecond)

	resp = td.sendCommand("status svc")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected running after start, got: %s", resp)
	}

	// Restart via control.
	resp = td.sendCommand("restart svc")
	if strings.Contains(resp, "error") {
		t.Fatalf("restart failed: %s", resp)
	}

	time.Sleep(1 * time.Second)

	resp = td.sendCommand("status svc")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected running after restart, got: %s", resp)
	}

	td.stop()
}

// Regression: a control-socket restart of a service whose on-success defaults
// to shutdown must restart the service in place. Pre-fix, the reap loop saw
// the signal-death exit, collapsed effectiveCode to 0 via the WasStopped
// rule, then dispatched the default OnSuccess=Shutdown — racing the pending
// restart and (often) winning, taking the daemon down. The reap-loop ECHILD
// path is also exercised here: with svc as the only managed process, Wait4
// returns ECHILD between the old pid being reaped and the new pid being
// registered, and the loop must treat that as transient while a restart is
// in flight.
func TestE2EControlRestartDefaultOnSuccess(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: svc
    command: sleep
    args: ["300"]
    kill-delay: 1s
`)
	defer td.kill()

	resp := td.sendCommand("restart svc")
	if strings.Contains(resp, "error") {
		t.Fatalf("restart failed: %s", resp)
	}

	time.Sleep(1500 * time.Millisecond)

	if !td.daemonAlive() {
		t.Fatalf("daemon exited unexpectedly after restart")
	}
	resp = td.sendCommand("status svc")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected running after restart, got: %s", resp)
	}

	// A control-socket restart counts as one `restarts +1` and must NOT bump
	// `exits` or `ok`/`fail` — operator-initiated restarts are accounted as a
	// single restart event rather than exit-and-start.
	resp = td.sendCommand("status")
	if !strings.Contains(resp, "restarts=1") {
		t.Errorf("expected restarts=1, got: %s", resp)
	}
	if !strings.Contains(resp, "exits=0") {
		t.Errorf("expected exits=0 after a restart-only cycle, got: %s", resp)
	}

	td.stop()
}

// Regression: stopping the only running service must NOT take gopherd down.
// Pre-fix, the reap loop hit Wait4 ECHILD with no children and broke out,
// exiting the daemon. As a live supervisor it must idle instead, so the
// service can be started again.
func TestE2EControlStopLastServiceStaysAlive(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: svc
    command: sleep
    args: ["300"]
    on-failure: ignore
    on-success: ignore
`)
	defer td.kill()

	resp := td.sendCommand("stop svc")
	if strings.Contains(resp, "error") {
		t.Fatalf("stop failed: %s", resp)
	}

	time.Sleep(500 * time.Millisecond)

	if !td.daemonAlive() {
		t.Fatalf("daemon exited after stopping the last service; expected it to idle")
	}
	resp = td.sendCommand("status svc")
	if !strings.Contains(resp, "stopped") {
		t.Fatalf("expected stopped, got: %s", resp)
	}

	// The idling daemon must still accept a start and bring the service back.
	resp = td.sendCommand("start svc")
	if strings.Contains(resp, "error") {
		t.Fatalf("start failed: %s", resp)
	}

	time.Sleep(500 * time.Millisecond)

	resp = td.sendCommand("status svc")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected running after restart from idle, got: %s", resp)
	}

	td.stop()
}

func TestE2EControlStartDisabled(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: lazy
    command: sleep
    args: ["300"]
    startup: disabled
    on-failure: ignore
    on-success: ignore
`)
	defer td.kill()

	// lazy should be disabled initially.
	resp := td.sendCommand("status lazy")
	if !strings.Contains(resp, "disabled") {
		t.Fatalf("expected lazy disabled, got: %s", resp)
	}

	// Start it via control socket.
	resp = td.sendCommand("start lazy")
	if strings.Contains(resp, "error") {
		t.Fatalf("start lazy failed: %s", resp)
	}

	time.Sleep(500 * time.Millisecond)

	resp = td.sendCommand("status lazy")
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected lazy running after start, got: %s", resp)
	}

	td.stop()
}

func TestE2EControlUnknownService(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: app
    command: sleep
    args: ["300"]
    on-failure: shutdown
`)
	defer td.kill()

	resp := td.sendCommand("status nonexistent")
	if !strings.Contains(resp, "error") {
		t.Fatalf("expected error for unknown service, got: %s", resp)
	}

	resp = td.sendCommand("start nonexistent")
	if !strings.Contains(resp, "error") {
		t.Fatalf("expected error for unknown service, got: %s", resp)
	}

	td.stop()
}

func TestE2EControlSignal(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: trapper
    command: /bin/sh
    args: ["-c", "trap 'echo got-usr1' USR1; while true; do sleep 1; done"]
    on-failure: shutdown
    on-success: shutdown
`)
	defer td.kill()

	time.Sleep(500 * time.Millisecond)

	// Send USR1 signal via control.
	resp := td.sendCommand("signal trapper SIGUSR1")
	if strings.Contains(resp, "error") {
		t.Fatalf("signal failed: %s", resp)
	}
	if !strings.Contains(resp, "sent") {
		t.Errorf("expected 'sent' in response, got: %s", resp)
	}

	td.stop()
}

func TestE2EControlLogs(t *testing.T) {
	td := startDaemon(t, `
log-capture: true
processes:
  - name: talker
    command: /bin/sh
    args: ["-c", "echo hello-from-talker && sleep 300"]
    on-failure: shutdown
`)
	defer td.kill()

	time.Sleep(1 * time.Second)

	// Get recent logs.
	resp := td.sendCommand("logs talker")
	if !strings.Contains(resp, "hello-from-talker") {
		t.Errorf("expected 'hello-from-talker' in logs, got: %s", resp)
	}

	td.stop()
}

func TestE2EControlLogsCaptureDisabled(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: talker
    command: /bin/sh
    args: ["-c", "echo hello-from-talker && sleep 300"]
    on-failure: shutdown
`)
	defer td.kill()

	time.Sleep(500 * time.Millisecond)

	// Capture defaults to off: logs must fail with a clear reason, not silence.
	resp := td.sendCommand("logs talker")
	if !strings.Contains(resp, "log capture disabled") {
		t.Errorf("expected 'log capture disabled' error, got: %s", resp)
	}

	td.stop()
}
