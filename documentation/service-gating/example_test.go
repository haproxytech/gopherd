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

package servicegating

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

// With ENABLE_SIDECAR unset (empty), the startup template expands to empty,
// which gates the service to "disabled". It is still defined, so the control
// socket can start it manually.
func TestServiceGatingDisabled(t *testing.T) {
	t.Setenv("ENABLE_SIDECAR", "") // empty behaves like unset

	d := doctest.RunConfig(t, readExample(t, "example.yml"), doctest.Options{})

	d.WaitRunning("app", 5*time.Second)
	if resp := d.Command("status sidecar"); !strings.Contains(resp, "disabled") {
		t.Fatalf("expected sidecar disabled, got: %s", resp)
	}

	// disabled means "not auto-started", not "removed"
	d.Command("start sidecar")
	d.WaitRunning("sidecar", 5*time.Second)

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}

// ENABLE_SIDECAR=enabled in gopherd's environment at config load auto-starts
// the gated service.
func TestServiceGatingEnabled(t *testing.T) {
	t.Setenv("ENABLE_SIDECAR", "enabled")

	d := doctest.RunConfig(t, readExample(t, "example.yml"), doctest.Options{})

	d.WaitRunning("app", 5*time.Second)
	d.WaitRunning("sidecar", 5*time.Second)

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
