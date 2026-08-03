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

package healthcheckhttpunix

import (
	"net"
	"net/http"
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

// waitCheck polls the status listing until the check reports the wanted state.
func waitCheck(t *testing.T, d *doctest.Daemon, check, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var resp string
	for time.Now().Before(deadline) {
		resp = d.Command("status")
		for line := range strings.SplitSeq(resp, "\n") {
			if !strings.Contains(line, check) {
				continue
			}
			if want == "healthy" && strings.Contains(line, "healthy") && !strings.Contains(line, "unhealthy") {
				return
			}
			if want == "unhealthy" && strings.Contains(line, "unhealthy") {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("check %s not %s within %s, got: %s", check, want, timeout, resp)
}

// An in-process HTTP server on a unix socket stands in for the service's
// health endpoint; closing it flips the check to unhealthy.
func TestHTTPCheckOverUnixSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "health.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	cfg := readExample(t, "example.yml")
	cfg = strings.ReplaceAll(cfg, "/usr/local/bin/app", "sleep")
	cfg = strings.ReplaceAll(cfg, "SOCKETFILE", sock)

	d := doctest.RunConfig(t, cfg, doctest.Options{})

	d.WaitRunning("app", 5*time.Second)
	waitCheck(t, d, "app-http", "healthy", 5*time.Second)

	srv.Close()
	waitCheck(t, d, "app-http", "unhealthy", 5*time.Second)

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
