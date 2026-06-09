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

package exitcodemap

import (
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// task exits 17, which is remapped to 0 before on-failure/on-success is
// evaluated. The success path (on-success: ignore) means neither on-failure:
// shutdown nor a success-shutdown fires, so the daemon stays alive.
func TestExitCodeMapExample(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{})

	if resp := d.Command("status task"); !strings.Contains(resp, "running") {
		t.Fatalf("expected task running, got: %s", resp)
	}

	// Wait past the sleep so task exits 17 (remapped to 0).
	time.Sleep(2 * time.Second)

	if !d.Alive() {
		t.Fatalf("expected daemon alive: exit 17 should remap to 0 and not shut down")
	}
	if resp := d.Command("status task"); strings.Contains(resp, "running") {
		t.Errorf("expected task no longer running, got: %s", resp)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
