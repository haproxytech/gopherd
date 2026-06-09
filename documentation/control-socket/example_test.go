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

package controlsocket

import (
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

func TestControlSocketExample(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{
		Commands: map[string]string{"/usr/local/bin/app": "/usr/bin/sleep"},
	})

	// startup: disabled — app is not auto-started.
	if resp := d.Command("status app"); strings.Contains(resp, "running") {
		t.Fatalf("expected app not running initially, got: %s", resp)
	}

	if resp := d.Command("start app"); strings.Contains(resp, "error") {
		t.Fatalf("start app failed: %s", resp)
	}
	time.Sleep(500 * time.Millisecond)
	if resp := d.Command("status app"); !strings.Contains(resp, "running") {
		t.Fatalf("expected app running after start, got: %s", resp)
	}

	if resp := d.Command("stop app"); strings.Contains(resp, "error") {
		t.Fatalf("stop app failed: %s", resp)
	}
	time.Sleep(500 * time.Millisecond)
	if resp := d.Command("status app"); strings.Contains(resp, "running") {
		t.Fatalf("expected app not running after stop, got: %s", resp)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
