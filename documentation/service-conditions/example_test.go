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

package serviceconditions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// First run: the file is missing, the oneshot creates it, the dependent
// starts. Second run: the file exists, the oneshot is skipped, the dependent
// still starts (skip = satisfied), and status/start report the skip.
func TestServiceConditions(t *testing.T) {
	data, err := os.ReadFile("example.yml")
	if err != nil {
		t.Fatal(err)
	}
	auxFile := filepath.Join(t.TempDir(), "haproxy-aux.cfg")
	cfg := strings.ReplaceAll(string(data), "AUXFILE", auxFile)

	// Run 1: condition met (file missing) — the oneshot runs.
	d := doctest.RunConfig(t, cfg, doctest.Options{})
	d.WaitRunning("app", 5*time.Second)
	if _, err := os.Stat(auxFile); err != nil {
		t.Fatalf("aux file not created: %v", err)
	}
	if out := d.Output(); !strings.Contains(out, "oneshot aux-cfg completed") {
		t.Errorf("run 1 output missing oneshot completion:\n%s", out)
	}
	if code := d.Stop(); code != 0 {
		t.Fatalf("run 1: expected clean exit 0, got %d", code)
	}

	// Run 2: condition unmet (file exists) — the oneshot is skipped.
	d = doctest.RunConfig(t, cfg, doctest.Options{})
	d.WaitRunning("app", 5*time.Second)
	if out := d.Output(); !strings.Contains(out, "aux-cfg skipped (condition-file-missing: "+auxFile+" exists)") {
		t.Errorf("run 2 output missing skip line:\n%s", out)
	}

	if got := d.Command("status aux-cfg"); !strings.Contains(got, "skipped (condition-file-missing") {
		t.Errorf("status = %q, want skipped with reason", got)
	}
	if got := d.Command("start aux-cfg"); !strings.Contains(got, "skipped (condition-file-missing") {
		t.Errorf("start = %q, want skip report", got)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("run 2: expected clean exit 0, got %d", code)
	}
}
