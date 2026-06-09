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

package multiservice

import (
	"strings"
	"testing"

	"github.com/haproxytech/gopherd/internal/doctest"
)

func TestMultiServiceExample(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{
		Commands: map[string]string{
			"/usr/local/bin/web":    "/usr/bin/sleep",
			"/usr/local/bin/worker": "/usr/bin/sleep",
		},
	})

	resp := d.Command("status")
	if !strings.Contains(resp, "web") || !strings.Contains(resp, "worker") {
		t.Fatalf("expected both services in status, got: %s", resp)
	}

	if r := d.Command("status web"); !strings.Contains(r, "running") {
		t.Fatalf("expected web running, got: %s", r)
	}
	if r := d.Command("status worker"); !strings.Contains(r, "running") {
		t.Fatalf("expected worker running, got: %s", r)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
