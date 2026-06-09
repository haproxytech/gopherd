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

package readygate

import (
	"strings"
	"testing"

	"github.com/haproxytech/gopherd/internal/doctest"
)

func TestReadyGateExample(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{
		Commands: map[string]string{
			"/usr/local/bin/db":  "/usr/bin/sleep",
			"/usr/local/bin/app": "/usr/bin/sleep",
		},
	})

	// app has a ready-check gate; it reaches running only after db-ready passes,
	// so a running app proves the gate opened.
	if resp := d.Command("status app"); !strings.Contains(resp, "running") {
		t.Fatalf("expected app running (gate opened), got: %s", resp)
	}
	if resp := d.Command("status db"); !strings.Contains(resp, "running") {
		t.Fatalf("expected db running, got: %s", resp)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
