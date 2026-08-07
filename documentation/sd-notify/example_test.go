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

package sdnotify

import (
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

func TestSDNotifyExample(t *testing.T) {
	// sdnotifyready sends READY=1 to $NOTIFY_SOCKET (abstract-namespace
	// datagram) and stays running, standing in for a real sd_notify daemon.
	d := doctest.RunFile(t, "example.yml", doctest.Options{
		Commands: map[string]string{
			"/usr/local/bin/mydaemon": doctest.Tool(t, "sdnotifyready"),
			"/usr/local/bin/app":      "sleep",
		},
	})

	// app gates on notifier writing READY=1; a running app proves the gate opened.
	deadline := time.Now().Add(6 * time.Second)
	var resp string
	for time.Now().Before(deadline) {
		resp = d.Command("status app")
		if strings.Contains(resp, "running") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(resp, "running") {
		t.Fatalf("expected app running (READY=1 received), got: %s", resp)
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
