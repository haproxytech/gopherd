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

package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestBeforeOrdering(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
processes:
  - name: first
    command: /bin/sh
    args: ["-c", "echo first >> /test/order.log && sleep 300"]
    before: [second]
    on-failure: shutdown

  - name: second
    command: /bin/sh
    args: ["-c", "echo second >> /test/order.log && sleep 1"]
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	data := readTestFile(t, dir, "order.log")
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), data)
	}
	if lines[0] != "first" || lines[1] != "second" {
		t.Errorf("expected [first, second], got %v", lines)
	}
}

func TestAfterOrdering(t *testing.T) {
	// Use oneshot for "first" so it completes before "second" starts,
	// guaranteeing deterministic write order.
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
processes:
  - name: first
    command: /bin/sh
    args: ["-c", "echo first >> /test/order.log"]
    startup: oneshot

  - name: second
    command: /bin/sh
    args: ["-c", "echo second >> /test/order.log && sleep 1"]
    after: [first]
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	data := readTestFile(t, dir, "order.log")
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), data)
	}
	if lines[0] != "first" || lines[1] != "second" {
		t.Errorf("expected [first, second], got %v", lines)
	}
}

func TestRequiresCascade(t *testing.T) {
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
export-socket: true
processes:
  - name: db
    command: /bin/sh
    args: ["-c", "sleep 1 && exit 1"]
    on-failure: ignore

  - name: app
    command: /bin/sh
    args: ["-c", "sleep 300"]
    requires: [db]
    on-success: ignore
    on-failure: ignore

  - name: test-runner
    command: /test/assert.sh
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
		"assert.sh": `#!/bin/sh
# Wait for db to crash and cascade to stop app.
sleep 4

status=$(/usr/local/bin/gopherd app status 2>&1)
echo "app status: $status"
echo "$status" | grep -q "stopped" || { echo "FAIL: app should be stopped"; exit 1; }
echo "PASS: requires cascade worked"
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "PASS") {
		t.Errorf("requires cascade check inconclusive:\n%s", out)
	}
}
