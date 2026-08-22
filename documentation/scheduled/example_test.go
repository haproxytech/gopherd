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

package scheduled

import (
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

func TestScheduledExample(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{
		Commands: map[string]string{
			"/usr/local/bin/app":    "sleep",
			"/usr/local/bin/backup": "/bin/sh",
		},
	})

	d.WaitRunning("app", 5*time.Second)

	// The scheduled service is registered but not started at boot: status
	// reports the next cron fire instead of running/stopped.
	if resp := d.Command("status backup"); !strings.Contains(resp, "scheduled (next run") {
		t.Fatalf("expected scheduled status with next run, got: %s", resp)
	}

	// `start` triggers a manual run through the same oneshot-style path a
	// cron tick uses: it runs to completion and the daemon stays up.
	if resp := d.Command("start backup"); !strings.Contains(resp, "started") {
		t.Fatalf("manual start failed: %s", resp)
	}

	// After the run exits, the service returns to waiting for its next tick.
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp := d.Command("status backup")
		if strings.Contains(resp, "scheduled (next run") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backup did not return to scheduled after manual run, got: %s", resp)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// A clean scheduled-run exit must not shut the daemon down.
	if resp := d.Command("status app"); !strings.Contains(resp, "running") {
		t.Fatalf("app not running after scheduled run: %s", resp)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
