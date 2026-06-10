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

// Package cpu detects available CPUs from system info and cgroup limits (v1 and v2).
// The effective count is the minimum of system online CPUs, CFS bandwidth quota,
// and cpuset pinning. All values are integer CPU counts (>= 1).
package cpu

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/haproxytech/gopherd/internal/cgroup"
)

// Cgroup root paths — variables so tests can override them.
var (
	cgroupV2Root = "/sys/fs/cgroup"
	cgroupV1CPU  = "/sys/fs/cgroup/cpu"
	cgroupV1Alt  = "/sys/fs/cgroup/cpu,cpuacct"
	cgroupV1Set  = "/sys/fs/cgroup/cpuset"
)

var (
	cachedCPUs int
	cachedOnce sync.Once
	cacheMu    sync.Mutex
)

// Available returns the number of available CPUs, defined as the minimum
// of system online CPUs, CFS bandwidth quota, and cpuset pinning.
// Always returns >= 1. The result is cached after the first call.
func Available() int {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cachedOnce.Do(func() {
		cachedCPUs = available()
	})
	return cachedCPUs
}

// resetCache allows tests to clear the cached result.
func resetCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cachedOnce = sync.Once{}
	cachedCPUs = 0
}

func available() int {
	sys := runtime.NumCPU()
	best := sys

	if cfs := cgroupCFSCPUs(); cfs > 0 && cfs < best {
		best = cfs
	}
	if cpuset := cgroupCpusetCPUs(); cpuset > 0 && cpuset < best {
		best = cpuset
	}

	if best < 1 {
		return 1
	}
	return best
}

// cgroupCFSCPUs returns the CPU count from CFS bandwidth quota.
// Tries cgroup v2 first, then v1.
func cgroupCFSCPUs() int {
	if cpus := cgroupV2CFSCPUs(); cpus > 0 {
		return cpus
	}
	return cgroupV1CFSCPUs()
}

// cgroupCpusetCPUs returns the CPU count from cpuset pinning.
// Tries cgroup v2 first, then v1.
func cgroupCpusetCPUs() int {
	if cpus := cgroupV2CpusetCPUs(); cpus > 0 {
		return cpus
	}
	return cgroupV1CpusetCPUs()
}

// cgroupV2CFSCPUs reads CFS quota from cgroup v2 unified hierarchy.
// cpu.max format: "$MAX $PERIOD" (microseconds). "max 100000" = unlimited.
func cgroupV2CFSCPUs() int {
	cgPath := cgroup.SelfPath("0::")
	if cgPath == "" {
		cgPath = "/"
	}

	root, err := os.OpenRoot(cgroupV2Root)
	if err != nil {
		return 0
	}
	defer root.Close()

	return int(cgroup.WalkUpLimit(root, cgPath, "cpu.max", parseCPUMax))
}

// parseCPUMax parses a cgroup v2 cpu.max file.
// Format: "$MAX $PERIOD" where MAX is microseconds or "max".
// Returns ceil(MAX/PERIOD) as CPU count, or 0 for unlimited.
func parseCPUMax(data []byte) int64 {
	s := strings.TrimSpace(string(data))
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return 0
	}
	if fields[0] == "max" {
		return 0
	}
	quota, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || quota <= 0 {
		return 0
	}
	period, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || period <= 0 {
		return 0
	}
	// ceil(quota / period)
	return (quota + period - 1) / period
}

// cgroupV1CFSCPUs reads CFS quota from cgroup v1.
// Reads cpu.cfs_quota_us and cpu.cfs_period_us. quota=-1 means unlimited.
func cgroupV1CFSCPUs() int {
	cgPath := cgroup.SelfPath("cpu")
	if cgPath == "" {
		cgPath = "/"
	}

	// Try the primary mount point, then the combined cpu,cpuacct mount.
	for _, rootPath := range []string{cgroupV1CPU, cgroupV1Alt} {
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			continue
		}
		cpus := v1CFSFromRoot(root, cgPath)
		root.Close()
		if cpus > 0 {
			return cpus
		}
	}
	return 0
}

// v1CFSFromRoot walks a cgroup v1 CPU root looking for quota and period files.
func v1CFSFromRoot(root *os.Root, cgPath string) int {
	quota := cgroup.WalkUpLimit(root, cgPath, "cpu.cfs_quota_us", parseV1Int)
	if quota <= 0 {
		return 0
	}
	period := cgroup.WalkUpLimit(root, cgPath, "cpu.cfs_period_us", parseV1Int)
	if period <= 0 {
		return 0
	}
	// ceil(quota / period)
	return int((quota + period - 1) / period)
}

// parseV1Int parses a cgroup v1 single-integer file.
// Returns the value, or 0 for -1 (unlimited) or parse errors.
func parseV1Int(data []byte) int64 {
	s := strings.TrimSpace(string(data))
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// cgroupV2CpusetCPUs reads cpuset from cgroup v2 unified hierarchy.
func cgroupV2CpusetCPUs() int {
	cgPath := cgroup.SelfPath("0::")
	if cgPath == "" {
		cgPath = "/"
	}

	root, err := os.OpenRoot(cgroupV2Root)
	if err != nil {
		return 0
	}
	defer root.Close()

	return int(cgroup.WalkUpLimit(root, cgPath, "cpuset.cpus.effective", parseCPUList))
}

// cgroupV1CpusetCPUs reads cpuset from cgroup v1.
func cgroupV1CpusetCPUs() int {
	cgPath := cgroup.SelfPath("cpuset")
	if cgPath == "" {
		cgPath = "/"
	}

	root, err := os.OpenRoot(cgroupV1Set)
	if err != nil {
		return 0
	}
	defer root.Close()

	return int(cgroup.WalkUpLimit(root, cgPath, "cpuset.cpus", parseCPUList))
}

// maxCPUCount bounds the value a poisoned cpuset (e.g. "0-9999999999") can
// inject into a {{cpu}} substitution. Generous for any real topology.
var maxCPUCount = int64(runtime.NumCPU()) * 64

// parseCPUList parses a CPU list in "0-3,5,7" format and returns the count,
// clamped to maxCPUCount. Returns 0 on empty or malformed input.
func parseCPUList(data []byte) int64 {
	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0
	}
	var count int64
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			loN, err1 := strconv.Atoi(lo)
			hiN, err2 := strconv.Atoi(hi)
			if err1 != nil || err2 != nil || hiN < loN {
				continue
			}
			count += int64(hiN) - int64(loN) + 1
		} else {
			if _, err := strconv.Atoi(part); err != nil {
				continue
			}
			count++
		}
		if count >= maxCPUCount {
			return maxCPUCount
		}
	}
	return count
}
