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

package fileinclusion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

func readExample(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// k8sSecretMount builds the K8s secret-volume layout: key is a symlink into
// ..data/, which itself is a symlink to a timestamped directory.
func k8sSecretMount(t *testing.T, key, value string) string {
	t.Helper()
	mount := t.TempDir()
	verDir := filepath.Join(mount, "..2026_06_10")
	if err := os.Mkdir(verDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verDir, key), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..2026_06_10", filepath.Join(mount, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", key), filepath.Join(mount, key)); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(mount, key)
}

// The plain secret file holds "s3cr3t\n"; `trim` strips the newline so TOKEN
// equals "s3cr3t" exactly. The K8s-style secret is a symlink chain that only
// `follow` permits. Both shell tests pass, so the service keeps running.
func TestFileInclusionExample(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("s3cr3t\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	k8sSecret := k8sSecretMount(t, "token", "k8sv4lue\n")
	cfg := strings.ReplaceAll(readExample(t, "example.yml"), "K8SSECRETFILE", k8sSecret)
	cfg = strings.ReplaceAll(cfg, "SECRETFILE", secret)

	d := doctest.RunConfig(t, cfg, doctest.Options{})

	// running proves both files were read: the plain one trimmed, the
	// symlinked one through follow
	d.WaitRunning("app", 5*time.Second)
	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
