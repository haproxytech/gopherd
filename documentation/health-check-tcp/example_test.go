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

package healthchecktcp

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// checkState returns the status line containing name, or "" if absent.
func checkState(resp, name string) string {
	for line := range strings.SplitSeq(resp, "\n") {
		if strings.Contains(line, name) {
			return line
		}
	}
	return ""
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func readExample(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestHealthCheckTCPExample(t *testing.T) {
	port := strconv.Itoa(freePort(t))
	cfg := strings.ReplaceAll(readExample(t, "example.yml"), "PORT", port)

	d := doctest.RunConfig(t, cfg, doctest.Options{})

	d.WaitRunning("svc", 5*time.Second)

	// Wait for the listener to bind and the TCP probe to connect.
	deadline := time.Now().Add(5 * time.Second)
	var resp string
	for time.Now().Before(deadline) {
		resp = d.Command("status")
		line := checkState(resp, "svc-tcp")
		if strings.Contains(line, "healthy") && !strings.Contains(line, "unhealthy") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	line := checkState(resp, "svc-tcp")
	if line == "" {
		t.Fatalf("expected svc-tcp check in status, got: %s", resp)
	}
	if !strings.Contains(line, "healthy") || strings.Contains(line, "unhealthy") {
		t.Fatalf("expected svc-tcp healthy (listener reachable), got: %s", line)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
