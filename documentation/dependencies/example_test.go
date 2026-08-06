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

package dependencies

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

func TestDependenciesExample(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "order.log")
	cfg := strings.ReplaceAll(readExample(t, "example.yml"), "ORDERLOG", logPath)

	d := doctest.RunConfig(t, cfg, doctest.Options{})

	// second running implies gopherd reached it in the start sequence.
	d.WaitRunning("second", 5*time.Second)

	// `after`/`before` guarantee gopherd's start order (fork/exec), not the
	// order the shells reach their echo, so assert on the daemon's own log.
	out := d.Output()
	zi := strings.Index(out, "started zeroth")
	fi := strings.Index(out, "started first")
	si := strings.Index(out, "started second")
	if zi == -1 || fi == -1 || si == -1 {
		t.Fatalf("expected all three start lines in daemon output, got: %q", out)
	}
	if zi > fi {
		t.Errorf("expected zeroth (before: [first]) started before first, got: %q", out)
	}
	if fi > si {
		t.Errorf("expected first started before second, got: %q", out)
	}

	d.Stop()
}
