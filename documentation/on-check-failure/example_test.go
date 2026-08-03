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

package oncheckfailure

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

var restartsRe = regexp.MustCompile(`(?m)^\s*app\s+\S+\s+exits=\d+ restarts=(\d+)`)

func restarts(t *testing.T, d *doctest.Daemon) int {
	t.Helper()
	m := restartsRe.FindStringSubmatch(d.Command("status"))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// waitHealthy polls the status listing until the check reports healthy.
func waitHealthy(t *testing.T, d *doctest.Daemon, check string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var resp string
	for time.Now().Before(deadline) {
		resp = d.Command("status")
		for line := range strings.SplitSeq(resp, "\n") {
			if strings.Contains(line, check) && strings.Contains(line, "healthy") &&
				!strings.Contains(line, "unhealthy") {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("check %s not healthy within %s, got: %s", check, timeout, resp)
}

// Self-healing cycle: deleting the flag file trips the check, gopherd
// restarts the app, the app recreates the flag, the check recovers.
func TestOnCheckFailureRestart(t *testing.T) {
	flag := filepath.Join(t.TempDir(), "healthy.flag")
	cfg := strings.ReplaceAll(readExample(t, "example.yml"), "FLAGFILE", flag)

	d := doctest.RunConfig(t, cfg, doctest.Options{})

	d.WaitRunning("app", 5*time.Second)
	waitHealthy(t, d, "app-alive", 5*time.Second)

	if err := os.Remove(flag); err != nil {
		t.Fatalf("remove flag: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && restarts(t, d) < 1 {
		time.Sleep(100 * time.Millisecond)
	}
	if n := restarts(t, d); n < 1 {
		t.Fatalf("expected a check-triggered restart, restarts=%d", n)
	}

	// restarted app recreated the flag; the check recovers
	d.WaitRunning("app", 5*time.Second)
	waitHealthy(t, d, "app-alive", 5*time.Second)

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
