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

package shutdownorder

import (
	"os"
	"path/filepath"
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

func TestShutdownOrderExample(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "stop.log")
	cfg := strings.ReplaceAll(readExample(t, "example.yml"), "STOPLOG", logPath)

	d := doctest.RunConfig(t, cfg, doctest.Options{})

	d.WaitRunning("db", 5*time.Second)
	d.WaitRunning("app", 5*time.Second)
	// Give the shells a moment to install their signal traps.
	time.Sleep(300 * time.Millisecond)

	// reverse-dep stops app before db and waits for each exit before
	// signaling the next, so log order is deterministic.
	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read stop log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines in stop log, got %d: %q", len(lines), string(data))
	}
	if lines[0] != "app" || lines[1] != "db" {
		t.Errorf("expected stop order [app, db], got %v", lines)
	}
}
