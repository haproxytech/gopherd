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
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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
