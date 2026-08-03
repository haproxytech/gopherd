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

package dotenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

func writeEnvFile(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "app.env")
	if err := os.WriteFile(path, []byte("PORT=9090\nAPI_TOKEN=s3cr3t\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}

// Running proves both halves: {{.PORT:-8080}} expanded from the dotenv value
// and API_TOKEN reached the child environment. The daemon's working dir is
// this package dir, so `dotenv: .env` resolves to the checked-in file.
func TestDotenvExample(t *testing.T) {
	// CI checkout (umask 0000) leaves 0666; dotenv refuses world-writable.
	if err := os.Chmod(".env", 0o600); err != nil {
		t.Fatalf("chmod .env: %v", err)
	}

	d := doctest.RunFile(t, "example.yml", doctest.Options{})

	d.WaitRunning("app", 5*time.Second)
	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}

// A symlinked dotenv path is refused by default; dotenv-follow: true permits
// it. Both services start disabled so the refusal surfaces as a start error
// instead of killing the daemon.
func TestDotenvSymlink(t *testing.T) {
	dir := t.TempDir()
	writeEnvFile(t, dir)
	link := filepath.Join(dir, "link.env")
	if err := os.Symlink("app.env", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cfg := `processes:
  - name: refused
    command: /bin/sh
    args: ["-c", "sleep 300"]
    dotenv: ` + link + `
    startup: disabled

  - name: followed
    command: /bin/sh
    args: ["-c", "sleep 300"]
    dotenv: ` + link + `
    dotenv-follow: true
    startup: disabled
`
	d := doctest.RunConfig(t, cfg, doctest.Options{})

	if resp := d.Command("start refused"); !strings.Contains(resp, "symlink") {
		t.Errorf("expected symlink refusal, got: %s", resp)
	}
	d.Command("start followed")
	d.WaitRunning("followed", 5*time.Second)

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
