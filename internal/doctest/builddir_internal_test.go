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

package doctest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuildDirNameIsPerTree pins what the artifact path is for: naming the tree
// the binary was built from. buildTo renames onto filepath.Join(dir, name), so
// two runs that agree on dir agree on the final path, and the second build
// becomes the binary the first one execs. Nothing reports it — the tests just
// run foreign code and pass or fail for unrelated reasons.
func TestBuildDirNameIsPerTree(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	treeA := filepath.Join(base, "tree-a")
	treeB := filepath.Join(base, "tree-b")
	for _, d := range []string{treeA, treeB} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	dirA, dirB := buildDirName(treeA), buildDirName(treeB)
	if dirA == dirB {
		t.Errorf("two source trees share the artifact dir %s; a build from one "+
			"overwrites the other's binary and the tests silently exec the "+
			"wrong one", dirA)
	}

	// Stable for one tree: concurrent processes of a checkout are meant to
	// share the path (identical bytes), so a per-call nonce would "fix" the
	// collision by leaking a directory per test process.
	if again := buildDirName(treeA); again != dirA {
		t.Errorf("buildDirName not stable for one root: %s then %s", dirA, again)
	}

	// The uid stays in the name: the directory is mode 0700, so another user on
	// the host must not be handed a path they cannot write to.
	uid := fmt.Sprintf("%d", os.Getuid())
	if !strings.Contains(filepath.Base(dirA), uid) {
		t.Errorf("artifact dir %q does not carry the uid %s", dirA, uid)
	}
	if parent := filepath.Dir(dirA); parent != strings.TrimSuffix(os.TempDir(), "/") {
		t.Errorf("artifact dir %q is not under TempDir %q", dirA, os.TempDir())
	}

	// Two names for one tree must not build twice: a symlinked checkout (a
	// "current" link in CI) would pay for a second build and gain nothing.
	link := filepath.Join(base, "link-to-a")
	if err := os.Symlink(treeA, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
	if got := buildDirName(link); got != dirA {
		t.Errorf("symlink to the same tree got a separate artifact dir:\n"+
			" %s via %s\n %s via %s", got, link, dirA, treeA)
	}
}

// TestBuildDirCreatesTheDirPrivately pins that buildDir creates the path it
// returns, since os.CreateTemp in buildTo does not create parents, and that no
// other user can read it: it holds executables they could run or replace.
func TestBuildDirCreatesTheDirPrivately(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "some-tree")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dir, err := buildDir(root)
	if err != nil {
		t.Fatalf("buildDir: %v", err)
	}
	// No cleanup: this is the shared per-(uid, tree) dir and a concurrent test
	// process of the same tree may be using it.
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("buildDir returned %s but did not create it: %v", dir, err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("artifact dir %s has mode %#o; it holds executables and must "+
			"not be readable or writable by other users", dir, perm)
	}
}
