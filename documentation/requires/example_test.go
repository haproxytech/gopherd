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

package requires

import (
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// waitStopped polls until the service reports stopped.
func waitStopped(t *testing.T, d *doctest.Daemon, service string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var resp string
	for time.Now().Before(deadline) {
		resp = d.Command("status " + service)
		if strings.Contains(resp, "stopped") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("service %s not stopped within %s, got: %s", service, timeout, resp)
}

// A db failure stops web too; after db recovers, web stays stopped until
// started manually.
func TestRequires(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{
		Commands: map[string]string{
			"/usr/local/bin/db":  "sleep",
			"/usr/local/bin/web": "sleep",
		},
	})

	d.WaitRunning("db", 5*time.Second)
	d.WaitRunning("web", 5*time.Second)

	// SIGKILL = failure exit; on-failure: ignore keeps the daemon alive
	d.Command("signal db SIGKILL")
	waitStopped(t, d, "db", 5*time.Second)
	waitStopped(t, d, "web", 5*time.Second)

	// dependency recovery does not resurrect the dependent
	d.Command("start db")
	d.WaitRunning("db", 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	if resp := d.Command("status web"); !strings.Contains(resp, "stopped") {
		t.Fatalf("web should stay stopped after db recovery, got: %s", resp)
	}

	d.Command("start web")
	d.WaitRunning("web", 5*time.Second)

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
