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

package restartwithdependents

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

var pidRe = regexp.MustCompile(`pid (\d+)`)

func pidOf(t *testing.T, d *doctest.Daemon, svc string) string {
	t.Helper()
	m := pidRe.FindStringSubmatch(d.Command("status " + svc))
	if m == nil {
		t.Fatalf("%s is not running", svc)
	}
	return m[1]
}

func TestRestartWithDependentsExample(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{
		Commands: map[string]string{
			"/usr/local/bin/db":  "sleep",
			"/usr/local/bin/web": "sleep",
		},
	})
	d.WaitRunning("web", 5*time.Second)
	dbPid, webPid := pidOf(t, d, "db"), pidOf(t, d, "web")

	resp := d.Command("restart db --wait")
	if !strings.Contains(resp, "db: restarted") || !strings.Contains(resp, "web") {
		t.Fatalf("expected db restarted with web listed, got: %s", resp)
	}
	if p := pidOf(t, d, "db"); p == dbPid {
		t.Fatalf("db pid unchanged: %s", p)
	}
	if p := pidOf(t, d, "web"); p == webPid {
		t.Fatalf("web pid unchanged: %s", p)
	}

	// web has no flag: restarting it leaves db alone.
	dbPid = pidOf(t, d, "db")
	if resp := d.Command("restart web --wait"); !strings.Contains(resp, "web: restarted") {
		t.Fatalf("restart web: %s", resp)
	}
	if p := pidOf(t, d, "db"); p != dbPid {
		t.Fatalf("db must keep its pid, old=%s new=%s", dbPid, p)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
