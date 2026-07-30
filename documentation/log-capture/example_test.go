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

package logcapture

import (
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// Capture is opt-in: `raw` (default) writes straight to the OS FDs with no
// prefix and no ring buffer; `captured` (log-capture: true) is prefixed and
// queryable via `logs`.
func TestLogCaptureExample(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{})

	d.WaitRunning("raw", 5*time.Second)
	d.WaitRunning("captured", 5*time.Second)

	// Captured service: line reaches the ring buffer, with the [captured] tag.
	var logs string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		logs = d.Command("logs captured")
		if strings.Contains(logs, "captured-line") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(logs, "captured-line") || !strings.Contains(logs, "[captured]") {
		t.Errorf("expected prefixed captured-line in logs, got: %q", logs)
	}

	// Non-captured service: logs refuses with a clear reason.
	if resp := d.Command("logs raw"); !strings.Contains(resp, "log capture disabled") {
		t.Errorf("expected 'log capture disabled' error, got: %q", resp)
	}

	// Raw output passes through un-prefixed (child inherits gopherd's stdout,
	// which the harness captures).
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(d.Output(), "raw-passthrough") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	out := d.Output()
	if !strings.Contains(out, "raw-passthrough") {
		t.Fatalf("expected raw-passthrough in daemon output, got: %q", out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "raw-passthrough") && strings.Contains(line, "[raw]") {
			t.Errorf("raw line must not be prefixed, got: %q", line)
		}
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
