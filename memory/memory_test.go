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

package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/haproxytech/gopherd/cgroup"
)

func setupFakeFS(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resetCache()
	t.Cleanup(func() {
		procMeminfo = "/proc/meminfo"
		cgroup.ProcSelfCgroup = "/proc/self/cgroup"
		cgroupV2Root = "/sys/fs/cgroup"
		cgroupV1Root = "/sys/fs/cgroup/memory"
		resetCache()
	})
	return dir
}

func TestSystemMemMiB(t *testing.T) {
	dir := setupFakeFS(t)
	fake := filepath.Join(dir, "meminfo")
	os.WriteFile(fake, []byte("MemTotal:        8028324 kB\nMemFree:         1234567 kB\n"), 0o644)
	procMeminfo = fake

	got, err := systemMemMiB()
	if err != nil {
		t.Fatal(err)
	}
	// 8028324 / 1024 = 7840
	if got != 7840 {
		t.Errorf("systemMemMiB() = %d, want 7840", got)
	}
}

// TestSystemMemMiBRounding covers N7: systemMemMiB must round to nearest MiB
// rather than truncate. 1536 kB = 1.5 MiB; truncation gives 1, rounding gives 2.
func TestSystemMemMiBRounding(t *testing.T) {
	dir := setupFakeFS(t)
	fake := filepath.Join(dir, "meminfo")
	// 1536 kB = exactly 1.5 MiB — demonstrates truncation vs rounding.
	os.WriteFile(fake, []byte("MemTotal:        1536 kB\nMemFree: 512 kB\n"), 0o644)
	procMeminfo = fake

	got, err := systemMemMiB()
	if err != nil {
		t.Fatal(err)
	}
	// 1536 / 1024 = 1 (truncated — wrong); (1536 + 512) / 1024 = 2 (rounded — correct).
	if got != 2 {
		t.Errorf("systemMemMiB() = %d, want 2 (1536 kB = 1.5 MiB must round to nearest, not truncate)", got)
	}
}

func TestCgroupV2_WithNamespace(t *testing.T) {
	// Cgroup v2 with cgroup namespace: path is "/" and memory.max is at root.
	dir := setupFakeFS(t)

	// /proc/self/cgroup says "0::/"
	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	// memory.max at root of cgroup fs
	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	os.WriteFile(filepath.Join(cgRoot, "memory.max"), []byte("2147483648\n"), 0o644)
	cgroupV2Root = cgRoot

	got := cgroupMemMiB()
	// 2147483648 / 1024 / 1024 = 2048
	if got != 2048 {
		t.Errorf("cgroupMemMiB() = %d, want 2048", got)
	}
}

func TestCgroupV2_WithoutNamespace(t *testing.T) {
	// Cgroup v2 without namespace: path is nested, limit at pod level.
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/kubepods/besteffort/pod-abc/container-xyz\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgRoot := filepath.Join(dir, "cg2")
	// Root has no limit.
	os.MkdirAll(cgRoot, 0o755)
	os.WriteFile(filepath.Join(cgRoot, "memory.max"), []byte("max\n"), 0o644)
	// Pod level has the limit.
	podDir := filepath.Join(cgRoot, "kubepods", "besteffort", "pod-abc")
	os.MkdirAll(podDir, 0o755)
	os.WriteFile(filepath.Join(podDir, "memory.max"), []byte("1073741824\n"), 0o644)
	// Container level has no limit file.
	containerDir := filepath.Join(podDir, "container-xyz")
	os.MkdirAll(containerDir, 0o755)
	cgroupV2Root = cgRoot

	got := cgroupMemMiB()
	// 1073741824 / 1024 / 1024 = 1024
	if got != 1024 {
		t.Errorf("cgroupMemMiB() = %d, want 1024", got)
	}
}

func TestCgroupV2_Max(t *testing.T) {
	// Cgroup v2 with "max" (no limit) should return 0.
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	os.WriteFile(filepath.Join(cgRoot, "memory.max"), []byte("max\n"), 0o644)
	cgroupV2Root = cgRoot

	got := cgroupMemMiB()
	if got != 0 {
		t.Errorf("cgroupMemMiB() with 'max' = %d, want 0", got)
	}
}

func TestCgroupV1(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("6:memory:/kubepods/pod-abc\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	// Disable v2 by pointing to nonexistent dir.
	cgroupV2Root = filepath.Join(dir, "nonexistent")

	cgV1 := filepath.Join(dir, "cg1")
	podDir := filepath.Join(cgV1, "kubepods", "pod-abc")
	os.MkdirAll(podDir, 0o755)
	os.WriteFile(filepath.Join(podDir, "memory.limit_in_bytes"), []byte("4294967296\n"), 0o644)
	cgroupV1Root = cgV1

	got := cgroupMemMiB()
	// 4294967296 / 1024 / 1024 = 4096
	if got != 4096 {
		t.Errorf("cgroupMemMiB() = %d, want 4096", got)
	}
}

func TestCgroupV1_NoLimit(t *testing.T) {
	// Cgroup v1 "no limit" sentinel.
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("6:memory:/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgroupV2Root = filepath.Join(dir, "nonexistent")

	cgV1 := filepath.Join(dir, "cg1")
	os.MkdirAll(cgV1, 0o755)
	os.WriteFile(filepath.Join(cgV1, "memory.limit_in_bytes"), []byte("9223372036854771712\n"), 0o644)
	cgroupV1Root = cgV1

	got := cgroupMemMiB()
	if got != 0 {
		t.Errorf("cgroupMemMiB() with no-limit sentinel = %d, want 0", got)
	}
}

func TestAvailable_CgroupLowerThanSystem(t *testing.T) {
	dir := setupFakeFS(t)

	// System: 4 GiB
	meminfo := filepath.Join(dir, "meminfo")
	os.WriteFile(meminfo, []byte("MemTotal:        4194304 kB\n"), 0o644)
	procMeminfo = meminfo

	// Cgroup: 2 GiB
	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	os.WriteFile(filepath.Join(cgRoot, "memory.max"), []byte("2147483648\n"), 0o644)
	cgroupV2Root = cgRoot
	cgroupV1Root = filepath.Join(dir, "nonexistent")

	got, err := Available()
	if err != nil {
		t.Fatal(err)
	}
	// min(4096, 2048) = 2048
	if got != 2048 {
		t.Errorf("Available() = %d, want 2048", got)
	}
}

// TestCgroupV2PrecedesV1 covers M-11: cgroupMemMiB must try v2 before v1.
// If the order were reversed a container that only has a v2 limit would get
// the v1 path (returning 0) and fall back to system RAM.
func TestCgroupV2PrecedesV1(t *testing.T) {
	dir := setupFakeFS(t)

	// /proc/self/cgroup: both v2 and v1 entries present.
	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n6:memory:/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	// v2 root: limit 512 MiB.
	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	os.WriteFile(filepath.Join(cgRoot, "memory.max"), []byte("536870912\n"), 0o644) // 512 MiB
	cgroupV2Root = cgRoot

	// v1 root: different limit 1024 MiB.
	cgV1 := filepath.Join(dir, "cg1")
	os.MkdirAll(cgV1, 0o755)
	os.WriteFile(filepath.Join(cgV1, "memory.limit_in_bytes"), []byte("1073741824\n"), 0o644) // 1024 MiB
	cgroupV1Root = cgV1

	got := cgroupMemMiB()
	// v2 value (512) should win; v1 value (1024) must not be returned.
	if got != 512 {
		t.Errorf("cgroupMemMiB() = %d, want 512 (v2 should precede v1)", got)
	}
}

func TestCgroupV2_PathTraversalBlocked(t *testing.T) {
	// os.Root prevents path traversal at the kernel level.
	// Even if /proc/self/cgroup contains "..", the Open call fails.
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/../../../etc\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	// Create a cgroup root with a valid memory.max at the root level.
	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	cgroupV2Root = cgRoot

	// Place a file outside the root that the traversal would try to reach.
	os.MkdirAll(filepath.Join(dir, "etc"), 0o755)
	os.WriteFile(filepath.Join(dir, "etc", "memory.max"), []byte("999999999999\n"), 0o644)

	// Should return 0 — os.Root blocks the traversal.
	got := cgroupV2MemMiB()
	if got != 0 {
		t.Errorf("cgroupV2MemMiB() with traversal path = %d, want 0", got)
	}
}
