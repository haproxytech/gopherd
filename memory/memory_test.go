package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func setupFakeFS(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		procMeminfo = "/proc/meminfo"
		procSelfCg = "/proc/self/cgroup"
		cgroupV2Root = "/sys/fs/cgroup"
		cgroupV1Root = "/sys/fs/cgroup/memory"
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

func TestCgroupV2_WithNamespace(t *testing.T) {
	// Cgroup v2 with cgroup namespace: path is "/" and memory.max is at root.
	dir := setupFakeFS(t)

	// /proc/self/cgroup says "0::/"
	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n"), 0o644)
	procSelfCg = selfCg

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
	procSelfCg = selfCg

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
	procSelfCg = selfCg

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
	procSelfCg = selfCg

	// Disable v2.
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
	procSelfCg = selfCg

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
	procSelfCg = selfCg

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

func TestSelfCgroupPath(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte(
		"12:cpuset:/kubepods/pod-abc\n"+
			"6:memory,hugetlb:/kubepods/pod-abc/container-xyz\n"+
			"0::/kubepods/pod-def\n",
	), 0o644)
	procSelfCg = selfCg

	// v2 path
	if got := selfCgroupPath("0::"); got != "/kubepods/pod-def" {
		t.Errorf("selfCgroupPath(v2) = %q, want /kubepods/pod-def", got)
	}
	// v1 memory controller
	if got := selfCgroupPath("memory"); got != "/kubepods/pod-abc/container-xyz" {
		t.Errorf("selfCgroupPath(memory) = %q, want /kubepods/pod-abc/container-xyz", got)
	}
	// v1 hugetlb (shares line with memory)
	if got := selfCgroupPath("hugetlb"); got != "/kubepods/pod-abc/container-xyz" {
		t.Errorf("selfCgroupPath(hugetlb) = %q, want /kubepods/pod-abc/container-xyz", got)
	}
	// missing controller
	if got := selfCgroupPath("blkio"); got != "" {
		t.Errorf("selfCgroupPath(blkio) = %q, want empty", got)
	}
}

func TestSelfCgroupPath_RejectsPathTraversal(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte(
		"0::/../../../etc/shadow\n"+
			"6:memory:/../../../etc/passwd\n",
	), 0o644)
	procSelfCg = selfCg

	if got := selfCgroupPath("0::"); got != "" {
		t.Errorf("selfCgroupPath(v2 traversal) = %q, want empty", got)
	}
	if got := selfCgroupPath("memory"); got != "" {
		t.Errorf("selfCgroupPath(v1 traversal) = %q, want empty", got)
	}
}
