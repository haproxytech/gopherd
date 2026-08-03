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

package entrypointargs

import (
	"os"
	"os/exec"
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

// Entrypoint args (leading "-" or after "--") are appended to the
// use-entrypoint-args service's args.
func TestEntrypointArgs(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	cfg := strings.ReplaceAll(readExample(t, "example.yml"), "ARGSFILE", argsFile)

	d := doctest.RunConfig(t, cfg, doctest.Options{
		ExtraArgs: []string{"--port", "9090"},
	})

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
	if got != "--port 9090" {
		t.Errorf("service args = %q, want %q", got, "--port 9090")
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}

// A bare non-client command execs directly: no daemon, no supervision.
func TestPassthrough(t *testing.T) {
	out, err := exec.Command(doctest.Binary(t), "/bin/echo", "hello from passthrough").CombinedOutput()
	if err != nil {
		t.Fatalf("passthrough failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "hello from passthrough") {
		t.Errorf("expected echoed output, got: %s", out)
	}
}
