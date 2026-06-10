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

package healthcheckexec

import (
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

func TestHealthCheckExecExample(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{
		Commands: map[string]string{"/usr/local/bin/svc": "sleep"},
	})

	d.WaitRunning("svc", 5*time.Second)

	// Wait for several check periods so the exec probe runs and reports healthy.
	deadline := time.Now().Add(5 * time.Second)
	var resp string
	for time.Now().Before(deadline) {
		resp = d.Command("status")
		line := checkState(resp, "svc-alive")
		if strings.Contains(line, "healthy") && !strings.Contains(line, "unhealthy") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Status line format: "<check>  healthy  failures=0".
	line := checkState(resp, "svc-alive")
	if line == "" {
		t.Fatalf("expected svc-alive check in status, got: %s", resp)
	}
	if !strings.Contains(line, "healthy") || strings.Contains(line, "unhealthy") {
		t.Fatalf("expected svc-alive healthy, got: %s", line)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
