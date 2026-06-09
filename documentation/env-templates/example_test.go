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

package envtemplates

import (
	"strings"
	"testing"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// WORD is unset, so {{.WORD:-hello}} falls back to "hello" and GREETING
// expands to "hello-world"; the shell test passes and the service keeps
// running instead of exiting 9.
func TestEnvTemplateDefault(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{})

	if resp := d.Command("status app"); !strings.Contains(resp, "running") {
		t.Fatalf("expected app running (GREETING=hello-world), got: %s", resp)
	}
	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
