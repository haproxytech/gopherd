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

package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnmetConditionNoneSet(t *testing.T) {
	t.Parallel()
	p := Process{Name: "x"}
	if reason := p.UnmetCondition(); reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}
}

func TestUnmetConditionFileExistsMet(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p := Process{ConditionFileExists: path}
	if reason := p.UnmetCondition(); reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}
}

func TestUnmetConditionFileExistsUnmet(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "absent")
	p := Process{ConditionFileExists: path}
	reason := p.UnmetCondition()
	if !strings.Contains(reason, "condition-file-exists") || !strings.Contains(reason, path) {
		t.Errorf("reason = %q, want option name and path", reason)
	}
}

func TestUnmetConditionFileMissingMet(t *testing.T) {
	t.Parallel()
	p := Process{ConditionFileMissing: filepath.Join(t.TempDir(), "absent")}
	if reason := p.UnmetCondition(); reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}
}

func TestUnmetConditionFileMissingUnmet(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p := Process{ConditionFileMissing: path}
	reason := p.UnmetCondition()
	if !strings.Contains(reason, "condition-file-missing") || !strings.Contains(reason, path) {
		t.Errorf("reason = %q, want option name and path", reason)
	}
}

func TestUnmetConditionBothMet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	present := filepath.Join(dir, "present")
	if err := os.WriteFile(present, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p := Process{
		ConditionFileExists:  present,
		ConditionFileMissing: filepath.Join(dir, "absent"),
	}
	if reason := p.UnmetCondition(); reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}
}

func TestUnmetConditionBothOneUnmet(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	present := filepath.Join(dir, "present")
	if err := os.WriteFile(present, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p := Process{
		ConditionFileExists:  present,
		ConditionFileMissing: present,
	}
	reason := p.UnmetCondition()
	if !strings.Contains(reason, "condition-file-missing") {
		t.Errorf("reason = %q, want condition-file-missing violation", reason)
	}
}

// A dangling symlink is "missing": follow semantics match k8s ..data mounts.
func TestUnmetConditionDanglingSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "gone"), link); err != nil {
		t.Fatal(err)
	}
	p := Process{ConditionFileExists: link}
	if reason := p.UnmetCondition(); reason == "" {
		t.Error("dangling symlink should not satisfy condition-file-exists")
	}
	p = Process{ConditionFileMissing: link}
	if reason := p.UnmetCondition(); reason != "" {
		t.Errorf("reason = %q; dangling symlink should satisfy condition-file-missing", reason)
	}
}

// A live symlink is followed to its target, like a k8s configmap key.
func TestUnmetConditionLiveSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	p := Process{ConditionFileExists: link}
	if reason := p.UnmetCondition(); reason != "" {
		t.Errorf("reason = %q, want empty", reason)
	}
}

// ENOTDIR (path through a regular file) counts as missing, not an error.
func TestUnmetConditionPathThroughFile(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	p := Process{ConditionFileMissing: filepath.Join(file, "child")}
	if reason := p.UnmetCondition(); reason != "" {
		t.Errorf("reason = %q, want empty (ENOTDIR is missing)", reason)
	}
}

// Stat errors other than not-exist (here ELOOP) leave the condition unmet,
// with the real error in the reason so it never masquerades as "missing".
func TestUnmetConditionStatError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}
	p := Process{ConditionFileExists: a}
	reason := p.UnmetCondition()
	if reason == "" {
		t.Fatal("symlink loop should leave the condition unmet")
	}
	if !strings.Contains(reason, "symbolic links") && !strings.Contains(reason, "too many") {
		t.Errorf("reason = %q, want the stat error included", reason)
	}
	// Same for condition-file-missing: an unreadable path is not proof of absence.
	p = Process{ConditionFileMissing: a}
	if reason := p.UnmetCondition(); reason == "" {
		t.Error("symlink loop should leave condition-file-missing unmet")
	}
}
