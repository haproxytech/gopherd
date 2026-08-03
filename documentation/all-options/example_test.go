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

package alloptions

import (
	"os"
	"regexp"
	"testing"

	"github.com/haproxytech/gopherd/internal/yml"
)

// TestLoad proves the reference config passes the real loader: option
// renames, signal/duration/action validation changes surface here.
func TestLoad(t *testing.T) {
	// Deterministic service-gating template regardless of the CI env.
	t.Setenv("START_METRICS", "")
	if _, err := yml.Load("example.yml"); err != nil {
		t.Fatalf("example.yml must pass the config loader: %v", err)
	}
}

// getKeyRe captures every option key the hand-rolled parser reads.
var getKeyRe = regexp.MustCompile(`\.Get\("([^"]+)"\)`)

// TestCompleteness fails when a parser option key is absent from
// example.yml — adding an option without documenting it breaks CI.
// Commented-out `# key: value` lines count as documented.
func TestCompleteness(t *testing.T) {
	src, err := os.ReadFile("../../internal/yml/config.go")
	if err != nil {
		t.Fatalf("reading parser source: %v", err)
	}
	example, err := os.ReadFile("example.yml")
	if err != nil {
		t.Fatalf("reading example.yml: %v", err)
	}

	keys := map[string]bool{}
	for _, m := range getKeyRe.FindAllStringSubmatch(string(src), -1) {
		keys[m[1]] = true
	}
	if len(keys) < 40 {
		t.Fatalf("extracted only %d keys from config.go; extraction regexp likely broken", len(keys))
	}

	for key := range keys {
		// Preceding boundary so e.g. "export-socket:" cannot satisfy "socket:".
		re := regexp.MustCompile(`(?m)(^|[\s#])` + regexp.QuoteMeta(key) + `:`)
		if !re.Match(example) {
			t.Errorf("option %q is parsed by config.go but missing from example.yml", key)
		}
	}
}
