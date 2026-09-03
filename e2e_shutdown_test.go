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
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestE2EExitCodePropagation(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: failer
    command: /bin/sh
    args: ["-c", "sleep 1 && exit 42"]
    on-failure: shutdown
`)
	defer td.kill()

	code := td.wait(10 * time.Second)
	if code != 42 {
		t.Errorf("expected exit code 42, got %d", code)
	}
}

// TestE2EExitActionShutdownWithSiblings drives shutdown from a service *exit
// action* while other services are still running — a path no single-service
// exit-code test reaches.
//
// The reap loop must hand shutdown to a goroutine: stopAll waits on done
// channels that only the reap loop closes, so running it inline deadlocks the
// daemon as soon as there is a second service to wait for. It also pins that
// the propagated exit code is the *triggering* service's, not that of whichever
// service happened to exit last while being stopped.
func TestE2EExitActionShutdownWithSiblings(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: idle-one
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
  - name: idle-two
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
  - name: failer
    command: /bin/sh
    args: ["-c", "sleep 1; exit 42"]
    on-success: ignore
    on-failure: shutdown
`)
	defer td.kill()

	td.WaitRunning("idle-one", 10*time.Second)
	td.WaitRunning("idle-two", 10*time.Second)

	// A bounded wait is the whole point: a synchronous shutdown from the reap
	// loop hangs here rather than failing an assertion.
	code := td.wait(20 * time.Second)
	if code != 42 {
		t.Errorf("daemon exit code = %d, want 42 (the triggering service's code "+
			"must survive the sibling services being stopped)", code)
	}
}

// TestE2EInitStopSignalOverridesBuiltinMeaning pins that `init-stop-signal`
// wins over a signal's built-in meaning. SIGHUP normally reloads, so with
// `init-stop-signal: [SIGHUP]` dispatch order decides whether the container can
// be stopped at all: check the reload arm first and the configured stop signal
// reloads instead, leaving the container unable to exit.
func TestE2EInitStopSignalOverridesBuiltinMeaning(t *testing.T) {
	td := startDaemon(t, `
init-stop-signal: [SIGHUP]

processes:
  - name: app
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
`)
	defer td.kill()

	td.WaitRunning("app", 10*time.Second)
	td.signal(syscall.SIGHUP)

	if code := td.wait(15 * time.Second); code != 0 {
		t.Errorf("daemon exit code = %d, want 0 after its configured stop signal", code)
	}
	if out := td.Output(); strings.Contains(out, "reload") {
		t.Errorf("SIGHUP was handled as a reload despite being the configured "+
			"init-stop-signal; output: %s", out)
	}
}

func TestE2EGracefulShutdownMultipleServices(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: svc1
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: svc2
    command: sleep
    args: ["300"]
    on-failure: shutdown
  - name: svc3
    command: sleep
    args: ["300"]
    on-failure: shutdown
`)
	defer td.kill()

	// All should be running.
	for _, name := range []string{"svc1", "svc2", "svc3"} {
		resp := td.sendCommand("status " + name)
		if !strings.Contains(resp, "running") {
			t.Errorf("expected %s running, got: %s", name, resp)
		}
	}

	// Graceful shutdown should stop all and exit cleanly.
	code := td.stop()
	if code != 0 {
		t.Errorf("expected exit code 0, got %d", code)
	}
}

// TestE2EShutdownOrderReverseDep asserts the default `reverse-dep` shutdown
// order: dependents stop before their dependencies. With C `after: [B]` and
// B `after: [A]`, the stop order must be C, B, A.
func TestE2EShutdownOrderReverseDep(t *testing.T) {
	dir := t.TempDir()
	order := filepath.Join(dir, "shutdown-order")
	td := startDaemon(t, fmt.Sprintf(`
shutdown-order: reverse-dep
processes:
  - name: a
    command: /bin/sh
    args: ["-c", "trap 'echo a >> %[1]s; exit 0' TERM; while true; do sleep 0.1; done"]
    on-failure: shutdown
    on-success: ignore
    kill-delay: 5s
  - name: b
    command: /bin/sh
    args: ["-c", "trap 'echo b >> %[1]s; exit 0' TERM; while true; do sleep 0.1; done"]
    after: [a]
    on-failure: shutdown
    on-success: ignore
    kill-delay: 5s
  - name: c
    command: /bin/sh
    args: ["-c", "trap 'echo c >> %[1]s; exit 0' TERM; while true; do sleep 0.1; done"]
    after: [b]
    on-failure: shutdown
    on-success: ignore
    kill-delay: 5s
`, order))
	defer td.kill()

	time.Sleep(500 * time.Millisecond)
	if code := td.stop(); code != 0 {
		t.Errorf("exit code: %d", code)
	}

	want := []string{"c", "b", "a"}
	got := readShutdownOrder(t, order)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shutdown order = %v, want %v", got, want)
	}
}

// TestE2EShutdownOrderDefault asserts that omitting `shutdown-order` behaves
// exactly like `reverse-dep`. Nearly every real config takes this path, and it
// is a different one from the explicit value: silently falling back to
// all-at-once would take a database down while its app is still serving.
func TestE2EShutdownOrderDefault(t *testing.T) {
	dir := t.TempDir()
	order := filepath.Join(dir, "shutdown-order")
	// Deliberately no shutdown-order key.
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: a
    command: /bin/sh
    args: ["-c", "trap 'echo a >> %[1]s; exit 0' TERM; while true; do sleep 0.1; done"]
    on-failure: shutdown
    on-success: ignore
    kill-delay: 5s
  - name: b
    command: /bin/sh
    args: ["-c", "trap 'echo b >> %[1]s; exit 0' TERM; while true; do sleep 0.1; done"]
    after: [a]
    on-failure: shutdown
    on-success: ignore
    kill-delay: 5s
  - name: c
    command: /bin/sh
    args: ["-c", "trap 'echo c >> %[1]s; exit 0' TERM; while true; do sleep 0.1; done"]
    after: [b]
    on-failure: shutdown
    on-success: ignore
    kill-delay: 5s
`, order))
	defer td.kill()

	time.Sleep(500 * time.Millisecond)
	if code := td.stop(); code != 0 {
		t.Errorf("exit code: %d", code)
	}

	want := []string{"c", "b", "a"}
	if got := readShutdownOrder(t, order); !reflect.DeepEqual(got, want) {
		t.Errorf("default shutdown order = %v, want %v (an absent shutdown-order "+
			"key must mean reverse-dep, not simultaneous)", got, want)
	}
}

// TestE2EShutdownOrderDep asserts `shutdown-order: dep` stops dependencies
// before dependents: A, B, C.
func TestE2EShutdownOrderDep(t *testing.T) {
	dir := t.TempDir()
	order := filepath.Join(dir, "shutdown-order")
	td := startDaemon(t, fmt.Sprintf(`
shutdown-order: dep
processes:
  - name: a
    command: /bin/sh
    args: ["-c", "trap 'echo a >> %[1]s; exit 0' TERM; while true; do sleep 0.1; done"]
    on-failure: shutdown
    on-success: ignore
    kill-delay: 5s
  - name: b
    command: /bin/sh
    args: ["-c", "trap 'echo b >> %[1]s; exit 0' TERM; while true; do sleep 0.1; done"]
    after: [a]
    on-failure: shutdown
    on-success: ignore
    kill-delay: 5s
  - name: c
    command: /bin/sh
    args: ["-c", "trap 'echo c >> %[1]s; exit 0' TERM; while true; do sleep 0.1; done"]
    after: [b]
    on-failure: shutdown
    on-success: ignore
    kill-delay: 5s
`, order))
	defer td.kill()

	time.Sleep(500 * time.Millisecond)
	if code := td.stop(); code != 0 {
		t.Errorf("exit code: %d", code)
	}

	want := []string{"a", "b", "c"}
	got := readShutdownOrder(t, order)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("shutdown order = %v, want %v", got, want)
	}
}

// TestE2EShutdownOrderSimultaneous asserts that `simultaneous` mode signals
// all services together. We can't pin a strict order — only that every
// service got the signal and recorded its exit.
func TestE2EShutdownOrderSimultaneous(t *testing.T) {
	dir := t.TempDir()
	order := filepath.Join(dir, "shutdown-order")
	td := startDaemon(t, fmt.Sprintf(`
shutdown-order: simultaneous
processes:
  - name: a
    command: /bin/sh
    args: ["-c", "trap 'echo a >> %[1]s; exit 0' TERM; while true; do sleep 0.1; done"]
    on-failure: shutdown
    on-success: ignore
  - name: b
    command: /bin/sh
    args: ["-c", "trap 'echo b >> %[1]s; exit 0' TERM; while true; do sleep 0.1; done"]
    after: [a]
    on-failure: shutdown
    on-success: ignore
  - name: c
    command: /bin/sh
    args: ["-c", "trap 'echo c >> %[1]s; exit 0' TERM; while true; do sleep 0.1; done"]
    after: [b]
    on-failure: shutdown
    on-success: ignore
`, order))
	defer td.kill()

	time.Sleep(500 * time.Millisecond)
	if code := td.stop(); code != 0 {
		t.Errorf("exit code: %d", code)
	}

	got := readShutdownOrder(t, order)
	if len(got) != 3 {
		t.Fatalf("expected 3 services to record shutdown, got %d (%v)", len(got), got)
	}
	for _, name := range []string{"a", "b", "c"} {
		if !slices.Contains(got, name) {
			t.Errorf("missing %s in shutdown record %v", name, got)
		}
	}
}

// readShutdownOrder reads the trap-recorded shutdown-order file and returns
// the names in the order they were written. Whitespace and blank lines are
// ignored. Used by the shutdown-order e2e tests.
func readShutdownOrder(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// TestE2EShutdownIgnoresUnmanagedChildren pins which children the shutdown
// drain waits for: the last *managed* pid ends it. A probe mid-flight or a
// reparented orphan is not a service and must not hold the daemon open.
// Answering "are we done?" from the configured service list instead of the live
// pid map makes shutdown last as long as the longest exec probe.
func TestE2EShutdownIgnoresUnmanagedChildren(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: app
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
    on-check-failure:
      slow: ignore

checks:
  slow:
    exec:
      command: /bin/sleep
      args: ["25"]
    period: 60s
    timeout: 50s
    threshold: 100
    initial-delay: 300ms
`)
	defer td.kill()
	td.WaitRunning("app", 10*time.Second)
	// Let the probe get going, so an unmanaged child is alive at shutdown.
	time.Sleep(1500 * time.Millisecond)

	start := time.Now()
	if code := td.stop(); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("shutdown took %v with a 25s health-check probe in flight; the "+
			"drain must finish when the last managed service is gone, not wait on "+
			"unmanaged children", elapsed)
	}
}

// TestE2ERestartStormDuringShutdown hammers control-socket restarts while the
// daemon is stopping, pinning the teardown *order* in run(): the control server
// stopped and its handlers joined, every tracked restart sender drained, and
// only then the restart channel closed. Any other order lets a handler send on
// a closed channel — a panic in PID 1, under a race sequential tests never
// produce.
//
// Both kinds of restart sender run at once because teardown joins them
// differently: ctrlServer.Stop() joins the control handlers, while
// check-failure handlers live on the checker goroutines and are not.
//
// The assertion is coarse by design — clean exit code, no panic — because that
// is the whole contract a wrong order breaks.
func TestE2ERestartStormDuringShutdown(t *testing.T) {
	td := startDaemon(t, `
processes:
  - name: a
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
  - name: b
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
  - name: c
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore
    on-check-failure:
      flapping: restart

checks:
  flapping:
    exec:
      command: /bin/false
    period: 20ms
    timeout: 1s
    threshold: 1
    initial-delay: 0s
`)
	defer td.kill()
	for _, n := range []string{"a", "b", "c"} {
		td.WaitRunning(n, 10*time.Second)
	}

	// Fire restarts as fast as the socket accepts them. Refusals and closed
	// sockets are both expected once shutdown starts, so no response is
	// asserted on individually.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b", "c", "a", "b", "c"} {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				conn, err := net.DialTimeout("unix", td.SocketPath(), 200*time.Millisecond)
				if err != nil {
					return // socket gone: shutdown is under way
				}
				fmt.Fprintf(conn, "restart %s\n", name)
				io.Copy(io.Discard, conn) //nolint:errcheck
				conn.Close()
			}
		})
	}

	time.Sleep(400 * time.Millisecond) // let the storm build up
	code := td.stop()
	close(stop)
	wg.Wait()

	if code != 0 {
		t.Errorf("daemon exit code = %d, want 0 (a restart racing SIGTERM must not "+
			"crash the daemon)", code)
	}
	if out := td.Output(); strings.Contains(out, "panic") {
		t.Errorf("daemon panicked during shutdown:\n%s", out)
	}
}
