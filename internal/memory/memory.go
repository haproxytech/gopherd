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

	"github.com/haproxytech/gopherd/internal/cgroup"
)

// File paths are variables so tests can override them.
var (
	procMeminfo  = "/proc/meminfo"
	cgroupV2Root = "/sys/fs/cgroup"
	cgroupV1Root = "/sys/fs/cgroup/memory"
)

// cgroupV1NoLimit is the cgroup v1 "no limit" sentinel: the page-aligned value
// closest to int64 max on most kernels.
const cgroupV1NoLimit = 9223372036854771712

var (
	cachedMiB  int64
	cachedErr  error
	cachedOnce sync.Once
	// cacheMu serialises cachedOnce replacement by resetCache (tests) against
	// concurrent Available() calls. It is held across cachedOnce.Do, so the
	// first-population I/O runs exactly once while other callers block.
	cacheMu sync.Mutex
)

// Available returns the available memory in MiB: the minimum of system physical
// memory and any active cgroup memory limit. Cached after the first call, since
// limits do not change at runtime in typical container environments.
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

// cgroupMemMiB reads the cgroup memory limit, trying v2 then v1.
// Returns 0 if no limit is set.
func cgroupMemMiB() int64 {
	if mib := cgroupV2MemMiB(); mib > 0 {
		return mib
	}
	return cgroupV1MemMiB()
}

// cgroupV2MemMiB reads the memory limit from the cgroup v2 unified hierarchy,
// using the "0::" path from /proc/self/cgroup.
//
// os.OpenRoot confines reads to the cgroup filesystem, preventing path traversal
// even if /proc/self/cgroup contains malicious paths.
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

// cgroupV1MemMiB reads the memory limit from cgroup v1, using the "memory"
// controller path from /proc/self/cgroup.
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

// parseV1Limit parses a cgroup v1 memory.limit_in_bytes file. Returns 0 for the
// "no limit" sentinel, non-numeric content, or empty data.
func parseV1Limit(data []byte) int64 {
	s := strings.TrimSpace(string(data))
	bytes, err := strconv.ParseInt(s, 10, 64)
	if err != nil || bytes <= 0 {
		return 0
	}
	if bytes >= cgroupV1NoLimit {
		return 0
	}
	return bytes / (1024 * 1024)
}
