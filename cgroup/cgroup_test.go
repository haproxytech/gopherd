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

package cgroup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelfPath(t *testing.T) {
	dir := t.TempDir()
	orig := ProcSelfCgroup
	t.Cleanup(func() { ProcSelfCgroup = orig })

	selfCg := filepath.Join(dir, "self_cgroup")
	os.WriteFile(selfCg, []byte(
		"12:cpuset:/kubepods/pod-abc\n"+
			"6:memory,hugetlb:/kubepods/pod-abc/container-xyz\n"+
			"0::/kubepods/pod-def\n",
	), 0o644)
	ProcSelfCgroup = selfCg

	// v2 path
	if got := SelfPath("0::"); got != "/kubepods/pod-def" {
		t.Errorf("SelfPath(v2) = %q, want /kubepods/pod-def", got)
	}
	// v1 memory controller
	if got := SelfPath("memory"); got != "/kubepods/pod-abc/container-xyz" {
		t.Errorf("SelfPath(memory) = %q, want /kubepods/pod-abc/container-xyz", got)
	}
	// v1 hugetlb (shares line with memory)
	if got := SelfPath("hugetlb"); got != "/kubepods/pod-abc/container-xyz" {
		t.Errorf("SelfPath(hugetlb) = %q, want /kubepods/pod-abc/container-xyz", got)
	}
	// missing controller
	if got := SelfPath("blkio"); got != "" {
		t.Errorf("SelfPath(blkio) = %q, want empty", got)
	}
}

func TestWalkUpLimit(t *testing.T) {
	dir := t.TempDir()

	// Create nested structure: dir/kubepods/pod-abc/container-xyz
	// Limit at pod level, not container level.
	podDir := filepath.Join(dir, "kubepods", "pod-abc")
	containerDir := filepath.Join(podDir, "container-xyz")
	os.MkdirAll(containerDir, 0o755)
	os.WriteFile(filepath.Join(podDir, "test.limit"), []byte("42\n"), 0o644)

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	parse := func(data []byte) int64 {
		s := string(data)
		if s == "" {
			return 0
		}
		var v int64
		for _, c := range s {
			if c >= '0' && c <= '9' {
				v = v*10 + int64(c-'0')
			}
		}
		return v
	}

	got := WalkUpLimit(root, "/kubepods/pod-abc/container-xyz", "test.limit", parse)
	if got != 42 {
		t.Errorf("WalkUpLimit() = %d, want 42", got)
	}
}

func TestReadRootFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("hello"), 0o644)

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	// Normal read.
	if got := ReadRootFile(root, "test.txt"); string(got) != "hello" {
		t.Errorf("ReadRootFile() = %q, want %q", got, "hello")
	}
	// Leading slash stripped.
	if got := ReadRootFile(root, "/test.txt"); string(got) != "hello" {
		t.Errorf("ReadRootFile(/test.txt) = %q, want %q", got, "hello")
	}
	// Missing file.
	if got := ReadRootFile(root, "missing.txt"); got != nil {
		t.Errorf("ReadRootFile(missing) = %v, want nil", got)
	}
	// Empty name.
	if got := ReadRootFile(root, ""); got != nil {
		t.Errorf("ReadRootFile('') = %v, want nil", got)
	}
}
