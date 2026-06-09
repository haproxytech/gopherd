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

// Rootless tests run gopherd itself as a non-root UID (no UID 0 in the
// container), the documented headline use case. The control socket lives on
// the world-writable /test mount because /run is root-owned; GOPHERD_SOCKET
// configures both the daemon and client.

const rootlessUser = "1234:1234"

// rootlessSocket is on /test so the non-root daemon can bind it.
const rootlessSocket = "/test/gopherd.sock"

// TestRootlessSupervision exercises the full control round-trip
// (status/stop/restart) and graceful shutdown with gopherd running as UID 1234.
func TestRootlessSupervision(t *testing.T) {
	dc := runDetached(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: worker
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`,
	}, containerOpts{user: rootlessUser, socket: rootlessSocket})
	defer dc.remove()

	time.Sleep(2 * time.Second)

	// Confirm gopherd really is non-root.
	if out, err := dc.exec("status"); err != nil {
		t.Fatalf("status as non-root failed: %v\n%s", err, out)
	} else if !strings.Contains(out, "worker") || !strings.Contains(out, "pid=") {
		t.Fatalf("expected running worker in status:\n%s", out)
	}

	if out, err := dc.exec("worker", "stop"); err != nil {
		t.Fatalf("stop failed: %v\n%s", err, out)
	}
	time.Sleep(1 * time.Second)

	if out, err := dc.exec("worker", "restart"); err != nil {
		t.Fatalf("restart failed: %v\n%s", err, out)
	}
	time.Sleep(1 * time.Second)

	out, err := dc.exec("status")
	if err != nil {
		t.Fatalf("status after restart failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "restarts=1") {
		t.Errorf("expected restarts=1 after restart:\n%s", out)
	}

	// Audit log records the peer UID as non-root.
	if logs := dc.logs(); !strings.Contains(logs, "uid=1234") {
		t.Errorf("expected control audit log with uid=1234:\n%s", logs)
	}

	dc.signal("TERM")
	if code := dc.wait(10 * time.Second); code != 0 {
		t.Errorf("expected clean shutdown exit 0, got %d\nlogs:\n%s", code, dc.logs())
	}
}

// TestRootlessSubreaper verifies PR_SET_CHILD_SUBREAPER works for a non-root
// gopherd (it does not require privileges) and does not crash startup.
func TestRootlessSubreaper(t *testing.T) {
	dc := runDetached(t, map[string]string{
		"gopherd.yml": `
no-logo: true
subreaper: true
processes:
  - name: worker
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`,
	}, containerOpts{user: rootlessUser, socket: rootlessSocket})
	defer dc.remove()

	time.Sleep(2 * time.Second)

	out, err := dc.exec("status")
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "worker") {
		t.Errorf("expected worker in status:\n%s", out)
	}
	if logs := dc.logs(); !strings.Contains(logs, "subreaper: enabled") {
		t.Errorf("expected subreaper enabled in logs:\n%s", logs)
	}

	dc.signal("TERM")
	if code := dc.wait(10 * time.Second); code != 0 {
		t.Errorf("expected clean shutdown exit 0, got %d\nlogs:\n%s", code, dc.logs())
	}
}
