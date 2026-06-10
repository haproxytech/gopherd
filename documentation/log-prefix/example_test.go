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

package logprefix

import (
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// `logs app` (non-follow) returns the recent ring-buffer lines. The line
// echoed by the service appears with the configured prefix ([app] + a UTC
// timestamp).
func TestLogPrefixExample(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{})

	d.WaitRunning("app", 5*time.Second)

	// Give the service a moment to emit and the ring buffer to record it.
	var logs string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		logs = d.Command("logs app")
		if strings.Contains(logs, "hello-from-app") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(logs, "hello-from-app") {
		t.Fatalf("expected echoed line in logs, got: %q", logs)
	}
	// Prefix tokens: [app] service tag present on the captured line.
	if !strings.Contains(logs, "[app]") {
		t.Errorf("expected [app] service prefix, got: %q", logs)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
