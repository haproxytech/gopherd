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
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/haproxytech/gopherd/cgroup"
)

// File paths are variables so tests can override them.
var (
	procMeminfo  = "/proc/meminfo"
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
	// cacheMu serialises two distinct operations:
	// 1. resetCache (tests only) — replaces cachedOnce with a fresh sync.Once,
	//    which would race with a concurrent Available() call.
	// 2. Available() callers during first population — because cacheMu is held
	//    for the duration of cachedOnce.Do, concurrent callers block here until
	//    the first population (which reads /proc/meminfo and cgroup paths)
	//    completes. This is intentional: the I/O runs only once.
	cacheMu sync.Mutex
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
		return (kb + 512) / 1024, nil
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
	cgPath := cgroup.SelfPath("0::")
	if cgPath == "" {
		cgPath = "/"
	}

	root, err := os.OpenRoot(cgroupV2Root)
	if err != nil {
		return 0
	}
	defer root.Close()

	return cgroup.WalkUpLimit(root, cgPath, "memory.max", parseV2Limit)
}

// cgroupV1MemMiB reads the memory limit from cgroup v1.
// In v1, the memory controller has its own hierarchy. The container's
// cgroup path is the entry with controller "memory" in /proc/self/cgroup.
func cgroupV1MemMiB() int64 {
	cgPath := cgroup.SelfPath("memory")
	if cgPath == "" {
		cgPath = "/"
	}

	root, err := os.OpenRoot(cgroupV1Root)
	if err != nil {
		return 0
	}
	defer root.Close()

	return cgroup.WalkUpLimit(root, cgPath, "memory.limit_in_bytes", parseV1Limit)
}

// parseV2Limit parses the contents of a cgroup v2 memory.max file.
// Returns 0 for "max" (unlimited), non-numeric content, or empty data.
func parseV2Limit(data []byte) int64 {
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

// parseV1Limit parses the contents of a cgroup v1 memory.limit_in_bytes file.
// Returns 0 for the "no limit" sentinel value, non-numeric content, or empty data.
func parseV1Limit(data []byte) int64 {
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
