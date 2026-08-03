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

package exportsocket

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

// waitFile polls until path exists and is non-empty, returning trimmed content.
func waitFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("file %s not written within %s", path, timeout)
	return ""
}

// The sidecar gets GOPHERD_SOCKET injected and runs a real client command
// against its own supervisor; the opted-out service sees no variable.
func TestExportSocket(t *testing.T) {
	dir := t.TempDir()
	bin := doctest.Binary(t)
	statusFile := filepath.Join(dir, "status.txt")
	sockFile := filepath.Join(dir, "sock.txt")
	plainFile := filepath.Join(dir, "plain.txt")

	cfg := readExample(t, "example.yml")
	cfg = strings.ReplaceAll(cfg, "/usr/local/bin/myapp", "sleep")
	// Retry until app reports running: sidecar may spawn before app does.
	cfg = strings.ReplaceAll(cfg, "gopherd status app > STATUSFILE 2>&1",
		"until "+bin+" status app | grep -q running; do sleep 0.2; done; "+bin+" status app > STATUSFILE 2>&1")
	cfg = strings.ReplaceAll(cfg, "STATUSFILE", statusFile)
	cfg = strings.ReplaceAll(cfg, "SOCKFILE", sockFile)
	cfg = strings.ReplaceAll(cfg, "PLAINFILE", plainFile)

	d := doctest.RunConfig(t, cfg, doctest.Options{})

	d.WaitRunning("app", 5*time.Second)
	d.WaitRunning("sidecar", 5*time.Second)
	d.WaitRunning("plain", 5*time.Second)

	if got := waitFile(t, sockFile, 5*time.Second); got != d.SocketPath() {
		t.Errorf("sidecar GOPHERD_SOCKET = %q, want %q", got, d.SocketPath())
	}
	if got := waitFile(t, plainFile, 5*time.Second); got != "unset" {
		t.Errorf("plain GOPHERD_SOCKET = %q, want unset", got)
	}
	status := waitFile(t, statusFile, 10*time.Second)
	if !strings.Contains(status, "app") || !strings.Contains(status, "running") {
		t.Errorf("in-child status output missing running app, got: %s", status)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
