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

package environmentpassthrough

import (
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

func TestEnvironmentPassthroughExample(t *testing.T) {
	// gopherd inherits this; pass-env: true forwards it to the child.
	t.Setenv("DEMO_TOKEN", "xyz")

	d := doctest.RunFile(t, "example.yml", doctest.Options{})

	// Child sees DEMO_TOKEN and sleeps; without pass-env it would exit 7.
	time.Sleep(500 * time.Millisecond)

	if resp := d.Command("status printer"); !strings.Contains(resp, "running") {
		t.Fatalf("expected printer running (child saw DEMO_TOKEN), got: %s", resp)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
