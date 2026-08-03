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

package stopsignal

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

func readExample(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// waitStopped polls until the service reports stopped, returning the elapsed time.
func waitStopped(t *testing.T, d *doctest.Daemon, service string, timeout time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	deadline := start.Add(timeout)
	var resp string
	for time.Now().Before(deadline) {
		resp = d.Command("status " + service)
		if strings.Contains(resp, "stopped") {
			return time.Since(start)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("service %s not stopped within %s, got: %s", service, timeout, resp)
	return 0
}

// graceful exits promptly on its custom SIGINT; stubborn ignores SIGTERM and
// is only gone after the kill-delay SIGKILL escalation.
func TestStopSignalKillDelay(t *testing.T) {
	cfg := strings.ReplaceAll(readExample(t, "example.yml"), "kill-delay: 2s", "kill-delay: 500ms")

	d := doctest.RunConfig(t, cfg, doctest.Options{})

	d.WaitRunning("graceful", 5*time.Second)
	d.WaitRunning("stubborn", 5*time.Second)

	d.Command("stop graceful")
	waitStopped(t, d, "graceful", 5*time.Second)

	d.Command("stop stubborn")
	time.Sleep(150 * time.Millisecond) // well before the 500ms escalation
	if resp := d.Command("status stubborn"); !strings.Contains(resp, "running") {
		t.Fatalf("stubborn should survive SIGTERM until kill-delay, got: %s", resp)
	}
	if elapsed := waitStopped(t, d, "stubborn", 5*time.Second); elapsed < 200*time.Millisecond {
		t.Errorf("stubborn stopped after %s; expected to hold out until the SIGKILL escalation", elapsed)
	}

	// intentional stops don't trigger exit actions; the daemon stays up
	if !d.Alive() {
		t.Fatal("daemon exited after service stops")
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
