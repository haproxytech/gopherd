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

package cpu

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/haproxytech/gopherd/cgroup"
)

func setupFakeFS(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resetCache()
	t.Cleanup(func() {
		cgroup.ProcSelfCgroup = "/proc/self/cgroup"
		cgroupV2Root = "/sys/fs/cgroup"
		cgroupV1CPU = "/sys/fs/cgroup/cpu"
		cgroupV1Alt = "/sys/fs/cgroup/cpu,cpuacct"
		cgroupV1Set = "/sys/fs/cgroup/cpuset"
		resetCache()
	})
	return dir
}

func TestSystemCPUs(t *testing.T) {
	// Without any cgroup limits, Available() should return runtime.NumCPU().
	dir := setupFakeFS(t)
	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	// Point all cgroup roots to nonexistent dirs (no limits).
	cgroupV2Root = filepath.Join(dir, "nonexistent")
	cgroupV1CPU = filepath.Join(dir, "nonexistent")
	cgroupV1Alt = filepath.Join(dir, "nonexistent")
	cgroupV1Set = filepath.Join(dir, "nonexistent")

	got := Available()
	want := runtime.NumCPU()
	if got != want {
		t.Errorf("Available() = %d, want %d (runtime.NumCPU)", got, want)
	}
}

func TestCgroupV2CFS(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	// 200000 / 100000 = 2 CPUs
	os.WriteFile(filepath.Join(cgRoot, "cpu.max"), []byte("200000 100000\n"), 0o644)
	cgroupV2Root = cgRoot

	// Disable v1 and cpuset.
	cgroupV1CPU = filepath.Join(dir, "nonexistent")
	cgroupV1Alt = filepath.Join(dir, "nonexistent")
	cgroupV1Set = filepath.Join(dir, "nonexistent")

	got := cgroupV2CFSCPUs()
	if got != 2 {
		t.Errorf("cgroupV2CFSCPUs() = %d, want 2", got)
	}
}

func TestCgroupV2CFS_Unlimited(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	os.WriteFile(filepath.Join(cgRoot, "cpu.max"), []byte("max 100000\n"), 0o644)
	cgroupV2Root = cgRoot

	got := cgroupV2CFSCPUs()
	if got != 0 {
		t.Errorf("cgroupV2CFSCPUs() with 'max' = %d, want 0", got)
	}
}

func TestCgroupV2CFS_WalkUp(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/kubepods/pod-abc/container-xyz\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgRoot := filepath.Join(dir, "cg2")
	// Limit at pod level.
	podDir := filepath.Join(cgRoot, "kubepods", "pod-abc")
	containerDir := filepath.Join(podDir, "container-xyz")
	os.MkdirAll(containerDir, 0o755)
	// 400000 / 100000 = 4 CPUs at pod level.
	os.WriteFile(filepath.Join(podDir, "cpu.max"), []byte("400000 100000\n"), 0o644)
	cgroupV2Root = cgRoot

	got := cgroupV2CFSCPUs()
	if got != 4 {
		t.Errorf("cgroupV2CFSCPUs() walk-up = %d, want 4", got)
	}
}

func TestCgroupV2CFS_FractionalCPU(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	// 150000 / 100000 = 1.5, ceil = 2 CPUs
	os.WriteFile(filepath.Join(cgRoot, "cpu.max"), []byte("150000 100000\n"), 0o644)
	cgroupV2Root = cgRoot

	got := cgroupV2CFSCPUs()
	if got != 2 {
		t.Errorf("cgroupV2CFSCPUs() fractional = %d, want 2", got)
	}
}

func TestCgroupV1CFS(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("4:cpu,cpuacct:/kubepods/pod-abc\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	// Disable v2.
	cgroupV2Root = filepath.Join(dir, "nonexistent")

	// Primary cpu mount doesn't exist, so it falls through to cpu,cpuacct.
	cgroupV1CPU = filepath.Join(dir, "nonexistent")

	cgV1 := filepath.Join(dir, "cpuacct")
	podDir := filepath.Join(cgV1, "kubepods", "pod-abc")
	os.MkdirAll(podDir, 0o755)
	os.WriteFile(filepath.Join(podDir, "cpu.cfs_quota_us"), []byte("300000\n"), 0o644)
	os.WriteFile(filepath.Join(podDir, "cpu.cfs_period_us"), []byte("100000\n"), 0o644)
	cgroupV1Alt = cgV1

	cgroupV1Set = filepath.Join(dir, "nonexistent")

	got := cgroupV1CFSCPUs()
	if got != 3 {
		t.Errorf("cgroupV1CFSCPUs() = %d, want 3", got)
	}
}

func TestCgroupV1CFS_Unlimited(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("4:cpu:/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgroupV2Root = filepath.Join(dir, "nonexistent")

	cgV1 := filepath.Join(dir, "cpu")
	os.MkdirAll(cgV1, 0o755)
	os.WriteFile(filepath.Join(cgV1, "cpu.cfs_quota_us"), []byte("-1\n"), 0o644)
	os.WriteFile(filepath.Join(cgV1, "cpu.cfs_period_us"), []byte("100000\n"), 0o644)
	cgroupV1CPU = cgV1
	cgroupV1Alt = filepath.Join(dir, "nonexistent")
	cgroupV1Set = filepath.Join(dir, "nonexistent")

	got := cgroupV1CFSCPUs()
	// -1 quota means unlimited → 0
	if got != 0 {
		t.Errorf("cgroupV1CFSCPUs() with unlimited = %d, want 0", got)
	}
}

func TestCgroupV2Cpuset(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	// CPUs 0-3,5,7 = 6 CPUs
	os.WriteFile(filepath.Join(cgRoot, "cpuset.cpus.effective"), []byte("0-3,5,7\n"), 0o644)
	cgroupV2Root = cgRoot

	got := cgroupV2CpusetCPUs()
	if got != 6 {
		t.Errorf("cgroupV2CpusetCPUs() = %d, want 6", got)
	}
}

func TestCgroupV2Cpuset_Individual(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	// Individual CPUs only: 1,3,5 = 3 CPUs
	os.WriteFile(filepath.Join(cgRoot, "cpuset.cpus.effective"), []byte("1,3,5\n"), 0o644)
	cgroupV2Root = cgRoot

	got := cgroupV2CpusetCPUs()
	if got != 3 {
		t.Errorf("cgroupV2CpusetCPUs() = %d, want 3", got)
	}
}

func TestCgroupV2Cpuset_Empty(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	os.WriteFile(filepath.Join(cgRoot, "cpuset.cpus.effective"), []byte("\n"), 0o644)
	cgroupV2Root = cgRoot

	got := cgroupV2CpusetCPUs()
	if got != 0 {
		t.Errorf("cgroupV2CpusetCPUs() empty = %d, want 0", got)
	}
}

func TestCgroupV1Cpuset(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("12:cpuset:/kubepods/pod-abc\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgroupV2Root = filepath.Join(dir, "nonexistent")

	cgSet := filepath.Join(dir, "cpuset")
	podDir := filepath.Join(cgSet, "kubepods", "pod-abc")
	os.MkdirAll(podDir, 0o755)
	// 0-1 = 2 CPUs
	os.WriteFile(filepath.Join(podDir, "cpuset.cpus"), []byte("0-1\n"), 0o644)
	cgroupV1Set = cgSet

	got := cgroupV1CpusetCPUs()
	if got != 2 {
		t.Errorf("cgroupV1CpusetCPUs() = %d, want 2", got)
	}
}

func TestAvailable_MinOfAll(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n12:cpuset:/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	// CFS: 4 CPUs
	os.WriteFile(filepath.Join(cgRoot, "cpu.max"), []byte("400000 100000\n"), 0o644)
	// Cpuset: 2 CPUs
	os.WriteFile(filepath.Join(cgRoot, "cpuset.cpus.effective"), []byte("0-1\n"), 0o644)
	cgroupV2Root = cgRoot

	cgroupV1CPU = filepath.Join(dir, "nonexistent")
	cgroupV1Alt = filepath.Join(dir, "nonexistent")
	cgroupV1Set = filepath.Join(dir, "nonexistent")

	got := Available()
	// min(runtime.NumCPU(), 4, 2) — if system has >= 2 CPUs, result is 2.
	// If system has 1 CPU, result is 1.
	if runtime.NumCPU() >= 2 {
		if got != 2 {
			t.Errorf("Available() = %d, want 2 (min of system, CFS=4, cpuset=2)", got)
		}
	} else {
		if got != 1 {
			t.Errorf("Available() = %d, want 1 (system has 1 CPU)", got)
		}
	}
}

func TestCgroupV2PrecedesV1(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/\n4:cpu:/\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	// v2: 2 CPUs
	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	os.WriteFile(filepath.Join(cgRoot, "cpu.max"), []byte("200000 100000\n"), 0o644)
	cgroupV2Root = cgRoot

	// v1: 4 CPUs — should NOT be used.
	cgV1 := filepath.Join(dir, "cpu")
	os.MkdirAll(cgV1, 0o755)
	os.WriteFile(filepath.Join(cgV1, "cpu.cfs_quota_us"), []byte("400000\n"), 0o644)
	os.WriteFile(filepath.Join(cgV1, "cpu.cfs_period_us"), []byte("100000\n"), 0o644)
	cgroupV1CPU = cgV1
	cgroupV1Alt = filepath.Join(dir, "nonexistent")
	cgroupV1Set = filepath.Join(dir, "nonexistent")

	got := cgroupCFSCPUs()
	if got != 2 {
		t.Errorf("cgroupCFSCPUs() = %d, want 2 (v2 should precede v1)", got)
	}
}

func TestPathTraversalBlocked(t *testing.T) {
	dir := setupFakeFS(t)

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte("0::/../../../etc\n"), 0o644)
	cgroup.ProcSelfCgroup = selfCg

	cgRoot := filepath.Join(dir, "cg2")
	os.MkdirAll(cgRoot, 0o755)
	cgroupV2Root = cgRoot

	// Place a file outside the root that traversal would try to reach.
	os.MkdirAll(filepath.Join(dir, "etc"), 0o755)
	os.WriteFile(filepath.Join(dir, "etc", "cpu.max"), []byte("200000 100000\n"), 0o644)

	got := cgroupV2CFSCPUs()
	if got != 0 {
		t.Errorf("cgroupV2CFSCPUs() with traversal path = %d, want 0", got)
	}
}

func TestParseCPUList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  int64
	}{
		{"0-3", 4},
		{"0-3,5,7", 6},
		{"1,3,5", 3},
		{"0", 1},
		{"0-0", 1},
		{"0-7", 8},
		{"", 0},
		{"  ", 0},
		{"abc", 0},
		{"0-3,abc,5", 5}, // skips invalid, counts valid
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := parseCPUList([]byte(tt.input))
			if got != tt.want {
				t.Errorf("parseCPUList(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseCPUMax(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  int64
	}{
		{"200000 100000", 2},
		{"100000 100000", 1},
		{"150000 100000", 2}, // ceil(1.5) = 2
		{"50000 100000", 1},  // ceil(0.5) = 1
		{"max 100000", 0},
		{"", 0},
		{"invalid", 0},
		{"200000", 0}, // missing period
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()
			got := parseCPUMax([]byte(tt.input))
			if got != tt.want {
				t.Errorf("parseCPUMax(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
