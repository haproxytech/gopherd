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

package initstopsignal

import (
	"syscall"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// init-stop-signal: [SIGUSR2] makes SIGUSR2 gopherd's graceful-shutdown
// trigger, replacing the default [SIGTERM, SIGINT]. SIGUSR2 is normally NOT a
// stop signal, so a clean shutdown on SIGUSR2 proves the override took effect.
func TestInitStopSignalExample(t *testing.T) {
	opts := doctest.Options{Commands: map[string]string{"/usr/local/bin/app": "sleep"}}

	d := doctest.RunFile(t, "example.yml", opts)
	d.WaitRunning("app", 5*time.Second)

	// SIGUSR2 triggers gopherd's graceful shutdown: it stops app cleanly and
	// exits 0. Without the override, SIGUSR2 would be dropped (unmapped) and
	// the daemon would keep running.
	d.Signal(syscall.SIGUSR2)
	if code := d.Wait(5 * time.Second); code != 0 {
		t.Errorf("expected clean exit 0 after SIGUSR2, got %d", code)
	}
}
