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

package hotreload

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

const baseCfg = `processes:
  - name: app
    command: sleep
    args: ["300"]
    on-failure: shutdown
`

const withSidecar = baseCfg + `
  - name: sidecar
    command: sleep
    args: ["300"]
    on-failure: shutdown
`

func startExample(t *testing.T) *doctest.Daemon {
	t.Helper()
	d := doctest.RunFile(t, "example.yml", doctest.Options{
		Commands: map[string]string{"/usr/local/bin/myapp": "sleep"},
	})
	d.WaitRunning("app", 5*time.Second)
	return d
}

// waitGone polls status until the service disappears from the list.
func waitGone(t *testing.T, d *doctest.Daemon, service string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var resp string
	for time.Now().Before(deadline) {
		resp = d.Command("status")
		if !strings.Contains(resp, service) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("service %s still listed after %s: %s", service, timeout, resp)
}

// Rewrite the config, reload over the control socket: the added service
// starts; after a second rewrite it is stopped and dropped again.
func TestHotReloadCommand(t *testing.T) {
	d := startExample(t)

	d.UpdateConfig(withSidecar)
	if resp := d.Command("reload"); strings.Contains(resp, "error") {
		t.Fatalf("reload failed: %s", resp)
	}
	d.WaitRunning("sidecar", 5*time.Second)
	d.WaitRunning("app", time.Second) // untouched by the reload

	d.UpdateConfig(baseCfg)
	if resp := d.Command("reload"); strings.Contains(resp, "error") {
		t.Fatalf("reload failed: %s", resp)
	}
	waitGone(t, d, "sidecar", 5*time.Second)

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}

// SIGHUP triggers the same reconciliation as the reload command.
func TestHotReloadSIGHUP(t *testing.T) {
	d := startExample(t)

	d.UpdateConfig(withSidecar)
	d.Signal(syscall.SIGHUP)
	d.WaitRunning("sidecar", 5*time.Second)

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
