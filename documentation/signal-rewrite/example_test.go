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

package signalrewrite

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// SIGUSR1 to gopherd is rewritten to SIGTERM for `app`; the trap fires and the
// service exits 0. on-success/on-failure: ignore keep the daemon alive, so it
// is still controllable afterward.
func TestSignalRewriteExample(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{})

	d.WaitRunning("app", 5*time.Second)

	d.Signal(syscall.SIGUSR1)

	// Wait for the rewritten SIGTERM to reach the service and exit it.
	var resp string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp = d.Command("status app")
		if !strings.Contains(resp, "running") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if strings.Contains(resp, "running") {
		t.Fatalf("expected app stopped after rewritten SIGTERM, got: %s", resp)
	}

	// Daemon stayed alive (ignore actions); SIGTERM cleanly shuts it down.
	if !d.Alive() {
		t.Fatalf("expected daemon alive after service exit")
	}
	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
