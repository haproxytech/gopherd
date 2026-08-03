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

package removeenv

import (
	"os"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// worker sees the shared dotenv token; metrics proves it was stripped while
// PORT survived. Either assertion failing exits 9 and fails the test.
func TestRemoveEnv(t *testing.T) {
	// CI checkout (umask 0000) leaves 0666; dotenv refuses world-writable.
	if err := os.Chmod(".env", 0o600); err != nil {
		t.Fatalf("chmod .env: %v", err)
	}

	d := doctest.RunFile(t, "example.yml", doctest.Options{})

	d.WaitRunning("worker", 5*time.Second)
	d.WaitRunning("metrics", 5*time.Second)

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
