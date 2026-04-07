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

// Package memory detects available system memory from /proc/meminfo and
// cgroup limits (v1 and v2). All values are in MiB.
package memory

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// File paths are variables so tests can override them.
var (
	procMeminfo  = "/proc/meminfo"
	procSelfCg   = "/proc/self/cgroup"
	cgroupV2Root = "/sys/fs/cgroup"
	cgroupV1Root = "/sys/fs/cgroup/memory"
)

// cgroupV1NoLimit is the sentinel value for "no limit" in cgroup v1.
// It's the page-aligned value closest to int64 max on most kernels.
const cgroupV1NoLimit = 9223372036854771712

var (
	cachedMiB  int64
	cachedErr  error
	cachedOnce sync.Once
	cacheMu    sync.Mutex // guards cachedOnce replacement in resetCache
)

// Available returns the available memory in MiB, defined as the minimum of
// system physical memory and any active cgroup memory limit.
// The result is cached after the first call since memory limits do not
// change at runtime in typical container environments.
func Available() (int64, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cachedOnce.Do(func() {
		cachedMiB, cachedErr = available()
	})
	return cachedMiB, cachedErr
}

// resetCache allows tests to clear the cached result.
func resetCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cachedOnce = sync.Once{}
	cachedMiB = 0
	cachedErr = nil
}

func available() (int64, error) {
	sys, err := systemMemMiB()
	if err != nil {
		return 0, err
	}
	cg := cgroupMemMiB()
	if cg > 0 && cg < sys {
		return cg, nil
	}
	return sys, nil
}

// systemMemMiB reads MemTotal from /proc/meminfo.
func systemMemMiB() (int64, error) {
	f, err := os.Open(procMeminfo)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
		return kb / 1024, nil
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("MemTotal not found in %s", procMeminfo)
}

// cgroupMemMiB reads the cgroup memory limit.
// It determines the cgroup version and path from /proc/self/cgroup,
// then reads the appropriate limit file. Returns 0 if no limit is set.
func cgroupMemMiB() int64 {
	// Try cgroup v2 first, then v1.
	if mib := cgroupV2MemMiB(); mib > 0 {
		return mib
	}
	return cgroupV1MemMiB()
}

// cgroupV2MemMiB reads the memory limit from cgroup v2.
// In v2, all controllers are in a unified hierarchy. The container's
// cgroup path is found in /proc/self/cgroup (the line starting with "0::").
//
// File access uses os.OpenRoot to confine reads to the cgroup filesystem,
// preventing path traversal even if /proc/self/cgroup contains malicious paths.
func cgroupV2MemMiB() int64 {
	cgPath := selfCgroupPath("0::")
	if cgPath == "" {
		cgPath = "/"
	}

	root, err := os.OpenRoot(cgroupV2Root)
	if err != nil {
		return 0
	}
	defer root.Close()

	// Walk up from the leaf cgroup to root, checking for memory.max at each
	// level. In K8s with cgroup namespaces, the limit is typically at "/".
	// Without namespaces, it's at the pod or container level.
	rel := cgPath
	for {
		limit := readV2LimitFrom(root, filepath.Join(rel, "memory.max"))
		if limit > 0 {
			return limit
		}
		if rel == "/" || rel == "." || rel == "" {
			break
		}
		rel = filepath.Dir(rel)
	}
	return 0
}

// cgroupV1MemMiB reads the memory limit from cgroup v1.
// In v1, the memory controller has its own hierarchy. The container's
// cgroup path is the entry with controller "memory" in /proc/self/cgroup.
func cgroupV1MemMiB() int64 {
	cgPath := selfCgroupPath("memory")
	if cgPath == "" {
		cgPath = "/"
	}

	root, err := os.OpenRoot(cgroupV1Root)
	if err != nil {
		return 0
	}
	defer root.Close()

	rel := cgPath
	for {
		limit := readV1LimitFrom(root, filepath.Join(rel, "memory.limit_in_bytes"))
		if limit > 0 {
			return limit
		}
		if rel == "/" || rel == "." || rel == "" {
			break
		}
		rel = filepath.Dir(rel)
	}
	return 0
}

// selfCgroupPath reads /proc/self/cgroup and returns the path for the
// given prefix. For v2, prefix is "0::". For v1, prefix is a controller
// name (e.g., "memory").
//
// /proc/self/cgroup format:
//
//	v2: "0::/kubepods/pod-abc/container-xyz"
//	v1: "6:memory:/kubepods/pod-abc/container-xyz"
//
// The returned path is used with os.Root, which provides kernel-level
// protection against path traversal (symlinks, ".." sequences).
func selfCgroupPath(prefix string) string {
	data, err := os.ReadFile(procSelfCg)
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

// readRootFile reads a file relative to an os.Root handle.
// Returns nil on any error (file not found, permission denied, traversal blocked).
func readRootFile(root *os.Root, name string) []byte {
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
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	return data
}

// readV2LimitFrom reads a cgroup v2 memory.max file via an os.Root handle.
// Returns 0 (meaning no limit found) for "max", non-numeric content, or
// unreadable files. Callers treat 0 as "no cgroup constraint" and fall
// back to system memory.
func readV2LimitFrom(root *os.Root, name string) int64 {
	data := readRootFile(root, name)
	if data == nil {
		return 0
	}
	s := strings.TrimSpace(string(data))
	if s == "max" || s == "" {
		return 0
	}
	bytes, err := strconv.ParseInt(s, 10, 64)
	if err != nil || bytes <= 0 {
		return 0
	}
	return bytes / (1024 * 1024)
}

// readV1LimitFrom reads a cgroup v1 memory.limit_in_bytes file via an os.Root handle.
// Returns 0 (meaning no limit found) for the "no limit" sentinel value,
// non-numeric content, or unreadable files. Callers treat 0 as "no cgroup
// constraint" and fall back to system memory.
func readV1LimitFrom(root *os.Root, name string) int64 {
	data := readRootFile(root, name)
	if data == nil {
		return 0
	}
	s := strings.TrimSpace(string(data))
	bytes, err := strconv.ParseInt(s, 10, 64)
	if err != nil || bytes <= 0 {
		return 0
	}
	// Cgroup v1 uses a large sentinel (page-aligned near int64 max) for "no limit".
	if bytes >= cgroupV1NoLimit {
		return 0
	}
	return bytes / (1024 * 1024)
}
