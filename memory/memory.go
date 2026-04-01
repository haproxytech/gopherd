// Package memory detects available system memory from /proc/meminfo and
// cgroup limits (v1 and v2). All values are in MiB.
package memory

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// Available returns the available memory in MiB, defined as the minimum of
// system physical memory and any active cgroup memory limit.
func Available() (int64, error) {
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
	return 0, sc.Err()
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
func cgroupV2MemMiB() int64 {
	cgPath := selfCgroupPath("0::")
	if cgPath == "" {
		// If no v2 entry, try root (cgroup namespace makes it "/").
		cgPath = "/"
	}

	// Walk up from the leaf cgroup to root, checking for memory.max at each
	// level. In K8s with cgroup namespaces, the limit is typically at "/".
	// Without namespaces, it's at the pod or container level.
	rel := cgPath
	for {
		limit := readV2Limit(filepath.Join(cgroupV2Root, rel, "memory.max"))
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

	rel := cgPath
	for {
		limit := readV1Limit(filepath.Join(cgroupV1Root, rel, "memory.limit_in_bytes"))
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
func selfCgroupPath(prefix string) string {
	data, err := os.ReadFile(procSelfCg)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if prefix == "0::" {
			// Cgroup v2: line starts with "0::".
			if strings.HasPrefix(line, "0::") {
				return strings.TrimPrefix(line, "0::")
			}
			continue
		}
		// Cgroup v1: format is "N:controller[,controller]:path".
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		for _, ctrl := range strings.Split(parts[1], ",") {
			if ctrl == prefix {
				return parts[2]
			}
		}
	}
	return ""
}

// readV2Limit reads a cgroup v2 memory.max file.
// Returns 0 (meaning no limit found) for "max", non-numeric content, or
// unreadable files. Callers treat 0 as "no cgroup constraint" and fall
// back to system memory.
func readV2Limit(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
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

// readV1Limit reads a cgroup v1 memory.limit_in_bytes file.
// Returns 0 (meaning no limit found) for the "no limit" sentinel value,
// non-numeric content, or unreadable files. Callers treat 0 as "no cgroup
// constraint" and fall back to system memory.
func readV1Limit(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
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
