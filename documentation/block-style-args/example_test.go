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

package blockstyleargs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// Block-style args reach the process as separate arguments; the quoted JSON
// item (commas, colons, '#') arrives intact as one argument.
func TestBlockStyleArgs(t *testing.T) {
	data, err := os.ReadFile("example.yml")
	if err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	cfg := strings.ReplaceAll(string(data), "ARGSFILE", argsFile)

	d := doctest.RunConfig(t, cfg, doctest.Options{})
	d.WaitRunning("app", 5*time.Second)

	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(argsFile); err == nil && len(data) > 0 {
			got = strings.TrimSpace(string(data))
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	want := `{"theme": {"foreground": "#d0d0d0", "background": "#1c1c1c"}}`
	if got != want {
		t.Errorf("JSON arg = %q, want %q", got, want)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
