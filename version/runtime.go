// Copyright 2026 HAProxy Technologies LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package version provides build version information from Go's embedded VCS metadata.
package version

import (
	"errors"
	"runtime/debug"
	"strings"
)

// Build metadata, populated by Set.
var (
	Repo       = ""
	Version    = "dev"
	Tag        = "dev"
	CommitDate = ""
)

// ErrBuildDataNotReadable is returned when debug.ReadBuildInfo fails.
var ErrBuildDataNotReadable = errors.New("not able to read build data")

// Set populates version variables from Go's embedded VCS build info.
func Set() error {
	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return ErrBuildDataNotReadable
	}
	setFrom(buildInfo)

	return nil
}

func setFrom(buildInfo *debug.BuildInfo) {
	Repo = buildInfo.Main.Path
	CommitDate = get(buildInfo, "vcs.time")
	Tag = strings.ReplaceAll(buildInfo.Main.Version, "(devel)", "dev")

	commit := get(buildInfo, "vcs.revision")
	if len(commit) > 8 {
		commit = commit[:8]
	}
	if commit == "" {
		// Module-proxy builds (go install pkg@version) have an exact tag but no VCS metadata.
		if buildInfo.Main.Version != "(devel)" && buildInfo.Main.Version != "" {
			Version = Tag
			return
		}
		commit = "unknown"
	}

	var dirty string
	if get(buildInfo, "vcs.modified") == "true" {
		dirty = ".dirty"
	}
	Version = Tag + "." + commit + dirty
}

func get(buildInfo *debug.BuildInfo, key string) string {
	for _, setting := range buildInfo.Settings {
		if setting.Key == key {
			return setting.Value
		}
	}
	return ""
}
