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

// Package cgroup provides shared helpers for reading cgroup v1/v2 paths and
// files. Both the memory and cpu packages use these to detect container limits.
package cgroup

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ProcSelfCgroup is the path to the cgroup membership file.
// Variable so tests can override it.
var ProcSelfCgroup = "/proc/self/cgroup"

// SelfPath reads /proc/self/cgroup and returns the cgroup path for the
// given prefix. For v2, prefix is "0::". For v1, prefix is a controller
// name (e.g., "memory", "cpu", "cpuset").
//
// /proc/self/cgroup format:
//
//	v2: "0::/kubepods/pod-abc/container-xyz"
//	v1: "6:memory:/kubepods/pod-abc/container-xyz"
//
// The returned path is used with os.Root, which provides kernel-level
// protection against path traversal (symlinks, ".." sequences).
func SelfPath(prefix string) string {
	f, err := os.Open(ProcSelfCgroup)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCgroupFileSize))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		if prefix == "0::" {
			// Cgroup v2: line starts with "0::".
			if path, ok := strings.CutPrefix(line, "0::"); ok {
				return path
			}
			continue
		}
		// Cgroup v1: format is "N:controller[,controller]:path".
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		for ctrl := range strings.SplitSeq(parts[1], ",") {
			if ctrl == prefix {
				return parts[2]
			}
		}
	}
	return ""
}

// maxCgroupFileSize caps how many bytes ReadRootFile will consume from a
// single cgroup file. Real cgroup and cpuset files are tens of bytes; 64 KiB
// leaves plenty of headroom for pathological cpuset.cpus lists while
// preventing a hostile or mis-bind-mounted file from driving PID 1 into an
// unbounded allocation.
const maxCgroupFileSize = 64 << 10

// ReadRootFile reads a file relative to an os.Root handle, capped at
// maxCgroupFileSize bytes. Returns nil on any error (file not found,
// permission denied, traversal blocked).
func ReadRootFile(root *os.Root, name string) []byte {
	// Strip leading slash — os.Root paths are relative to the root.
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return nil
	}
	f, err := root.Open(name)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCgroupFileSize))
	if err != nil {
		return nil
	}
	return data
}

// WalkUpLimit walks from the leaf cgroup directory up to root, calling parse
// on the named file at each level. Returns the first positive result.
// This handles both K8s with cgroup namespaces (limit at "/") and without
// (limit at pod or container level).
func WalkUpLimit(root *os.Root, cgPath, filename string, parse func([]byte) int64) int64 {
	rel := cgPath
	for {
		data := ReadRootFile(root, filepath.Join(rel, filename))
		if data != nil {
			if v := parse(data); v > 0 {
				return v
			}
		}
		if rel == "/" || rel == "." || rel == "" {
			break
		}
		rel = filepath.Dir(rel)
	}
	return 0
}
