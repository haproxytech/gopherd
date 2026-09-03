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

package umask

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

func TestUmaskExample(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "mask")
	script := filepath.Join(dir, "app.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\numask > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := doctest.RunFile(t, "example.yml", doctest.Options{
		Commands: map[string]string{
			"/usr/local/bin/app":    script,
			"/usr/local/bin/keeper": "sleep",
		},
	})
	d.WaitRunning("keeper", 5*time.Second)

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("app did not write its mask: %v", err)
	}
	if strings.TrimSpace(string(got)) != "0027" {
		t.Fatalf("umask = %q, want 0027", strings.TrimSpace(string(got)))
	}
	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
