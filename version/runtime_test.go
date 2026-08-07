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

package version

import (
	"runtime/debug"
	"testing"
)

func TestSetPopulatesFields(t *testing.T) {
	// Reset to defaults
	Repo = ""
	Version = "dev"
	Tag = "dev"
	CommitDate = ""

	err := Set()
	if err != nil {
		t.Fatalf("Set() returned error: %v", err)
	}

	if Repo == "" {
		t.Error("expected Repo to be populated")
	}
	// Version should be modified from the bare default — it gets a commit suffix appended.
	// During `go test` it becomes "dev.unknown" (no VCS info).
	if Version == "dev" {
		t.Error("expected Version to be changed from bare 'dev'")
	}
	// Tag may remain "dev" when run via `go test` (no VCS info), so just check it's not empty.
	if Tag == "" {
		t.Error("expected Tag to be non-empty")
	}
}

func TestSetFromModuleProxyBuild(t *testing.T) {
	// go install from the module proxy: exact tag, no vcs.* settings.
	setFrom(&debug.BuildInfo{
		Main: debug.Module{Path: "github.com/haproxytech/gopherd", Version: "v1.0.0"},
	})

	if Version != "v1.0.0" {
		t.Errorf("Version = %q, want %q", Version, "v1.0.0")
	}
	if Tag != "v1.0.0" {
		t.Errorf("Tag = %q, want %q", Tag, "v1.0.0")
	}
}

func TestSetFromDevBuildWithoutVCS(t *testing.T) {
	setFrom(&debug.BuildInfo{
		Main: debug.Module{Path: "github.com/haproxytech/gopherd", Version: "(devel)"},
	})

	if Version != "dev.unknown" {
		t.Errorf("Version = %q, want %q", Version, "dev.unknown")
	}
}

func TestSetFromGitBuild(t *testing.T) {
	setFrom(&debug.BuildInfo{
		Main: debug.Module{Path: "github.com/haproxytech/gopherd", Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef"},
			{Key: "vcs.modified", Value: "true"},
		},
	})

	if Version != "dev.01234567.dirty" {
		t.Errorf("Version = %q, want %q", Version, "dev.01234567.dirty")
	}
}

func TestSetIdempotent(t *testing.T) {
	err := Set()
	if err != nil {
		t.Fatalf("first Set() error: %v", err)
	}
	v1 := Version

	err = Set()
	if err != nil {
		t.Fatalf("second Set() error: %v", err)
	}
	if Version != v1 {
		t.Errorf("Version changed between calls: %q vs %q", v1, Version)
	}
}
