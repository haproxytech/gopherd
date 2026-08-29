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

package blockscalars

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// A literal block scalar arrives at the shell as one multi-line argument:
// both lines of the script run, in order, with '#'-free YAML intact.
func TestBlockScalarScript(t *testing.T) {
	data, err := os.ReadFile("example.yml")
	if err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(t.TempDir(), "out.txt")
	cfg := strings.ReplaceAll(string(data), "OUTFILE", outFile)

	d := doctest.RunConfig(t, cfg, doctest.Options{})
	d.WaitRunning("app", 5*time.Second)

	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(outFile); err == nil && strings.Count(string(data), "\n") >= 2 {
			got = string(data)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if want := "line one\nline two\n"; got != want {
		t.Errorf("script output = %q, want %q", got, want)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
