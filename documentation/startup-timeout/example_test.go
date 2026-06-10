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

package startuptimeout

import (
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

func TestStartupTimeoutExample(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{
		Commands: map[string]string{"/usr/local/bin/slow": "sleep"},
	})

	// The hung oneshot is killed at startup-timeout; that failure is fatal,
	// so gopherd exits non-zero well before the `sleep 30` horizon.
	if code := d.Wait(5 * time.Second); code == 0 {
		t.Errorf("expected non-zero exit after startup-timeout kill, got %d", code)
	}
}
