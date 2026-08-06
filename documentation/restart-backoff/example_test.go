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

package restartbackoff

import (
	"os"
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

// Counters only appear in the full status listing, not `status <name>`.
// Counters only appear in the full status listing, not `status <name>`.
func restartsOf(t *testing.T, d *doctest.Daemon, service string) int {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*` + service + `\s+\S+\s+exits=\d+ restarts=(\d+)`)
	m := re.FindStringSubmatch(d.Command("status"))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// flaky becomes /bin/false (crash loop) and batch /bin/true (clean-exit
// rerun loop); with a shortened backoff both restart counters climb while
// app and the daemon stay healthy.
func TestRestartBackoff(t *testing.T) {
	cfg := readExample(t, "example.yml")
	cfg = strings.ReplaceAll(cfg, "/usr/local/bin/myapp", "sleep")
	cfg = strings.ReplaceAll(cfg, "/usr/local/bin/flaky-worker", "/bin/false")
	cfg = strings.ReplaceAll(cfg, "/usr/local/bin/batch-job", "/bin/true")
	cfg = strings.ReplaceAll(cfg, "backoff-delay: 500ms", "backoff-delay: 50ms")
	cfg = strings.ReplaceAll(cfg, "backoff-limit: 5s", "backoff-limit: 200ms")

	d := doctest.RunConfig(t, cfg, doctest.Options{})

	d.WaitRunning("app", 5*time.Second)

	deadline := time.Now().Add(10 * time.Second)
	var flaky, batch int
	for time.Now().Before(deadline) {
		flaky, batch = restartsOf(t, d, "flaky"), restartsOf(t, d, "batch")
		if flaky >= 3 && batch >= 3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if flaky < 3 {
		t.Fatalf("expected >=3 flaky restarts within 10s, got %d", flaky)
	}
	if batch < 3 {
		t.Fatalf("expected >=3 batch reruns (on-success: restart) within 10s, got %d", batch)
	}

	// crash loop must not take the supervisor or the healthy service down
	if !d.Alive() {
		t.Fatal("daemon died during restart loop")
	}
	d.WaitRunning("app", time.Second)

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
