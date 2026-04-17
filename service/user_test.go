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
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// allowNonRootExec makes the test binary and its parent directories
// world-accessible so a re-exec as a non-root user can reach it.
// Only needed (and only works) when running as root.
func allowNonRootExec(t *testing.T) {
	t.Helper()
	bin := os.Args[0]
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatalf("chmod binary: %v", err)
	}
	for dir := filepath.Dir(bin); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatalf("chmod %s: %v", dir, err)
		}
	}
}

func TestResolveCredentialNil(t *testing.T) {
	t.Parallel()
	cred, err := ResolveCredential("", "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred != nil {
		t.Error("expected nil credential")
	}
}

func TestResolveCredentialByID(t *testing.T) {
	t.Parallel()
	uid := 1000
	gid := 1000
	cred, err := ResolveCredential("", "", &uid, &gid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred == nil {
		t.Fatal("expected non-nil credential")
	}
	if cred.Uid != 1000 || cred.Gid != 1000 {
		t.Errorf("uid=%d gid=%d, want 1000/1000", cred.Uid, cred.Gid)
	}
}

func TestResolveCredentialRejectsNegativeIDs(t *testing.T) {
	t.Parallel()
	validID := 1000
	negID := -1
	negUID := -42
	negGID := -7

	tests := []struct {
		userID  *int
		groupID *int
		name    string
		wantSub string
	}{
		{&negID, &validID, "negative user-id", "user-id must be >= 0"},
		{&validID, &negID, "negative group-id", "group-id must be >= 0"},
		{&negUID, &negGID, "negative both", "user-id must be >= 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cred, err := ResolveCredential("", "", tc.userID, tc.groupID)
			if err == nil {
				t.Fatalf("expected error for negative id, got cred=%+v", cred)
			}
			if cred != nil {
				t.Errorf("expected nil credential on error, got %+v", cred)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestResolveCredentialGroupOnly(t *testing.T) {
	t.Parallel()
	gid := 1000
	cred, err := ResolveCredential("", "", nil, &gid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred == nil {
		t.Fatal("expected non-nil credential")
	}
	if cred.Gid != 1000 {
		t.Errorf("gid=%d, want 1000", cred.Gid)
	}
}

// TestResolveCredentialGroupOnlyUID covers M-37: when only a group is specified
// the UID must be inherited from the current process, not left at 0 (root).
func TestResolveCredentialGroupOnlyUID(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		// Re-run this test as non-root so os.Getuid() != 0.
		allowNonRootExec(t)
		cmd := exec.Command(os.Args[0], "-test.run=^TestResolveCredentialGroupOnlyUID$", "-test.v")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: 1000, Gid: 1000},
		}
		out, err := cmd.CombinedOutput()
		t.Logf("non-root re-exec:\n%s", out)
		if err != nil {
			t.Fatalf("non-root re-exec failed: %v", err)
		}
		return
	}
	gid := 1000
	cred, err := ResolveCredential("", "", nil, &gid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred == nil {
		t.Fatal("expected non-nil credential")
	}
	want := uint32(os.Getuid())
	if cred.Uid != want {
		t.Errorf("uid=%d, want %d (current process uid)", cred.Uid, want)
	}
}

// TestResolveCredentialUserIDOnlyGID covers N1: when only numeric user-id is
// specified (no group-id or group name), the GID must not be zero (root group).
// It must be inherited from the current process GID, symmetric with the
// group-only case which inherits the current process UID.
func TestResolveCredentialUserIDOnlyGID(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		// Re-run this test as non-root so os.Getgid() != 0.
		allowNonRootExec(t)
		cmd := exec.Command(os.Args[0], "-test.run=^TestResolveCredentialUserIDOnlyGID$", "-test.v")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: 1000, Gid: 1000},
		}
		out, err := cmd.CombinedOutput()
		t.Logf("non-root re-exec:\n%s", out)
		if err != nil {
			t.Fatalf("non-root re-exec failed: %v", err)
		}
		return
	}
	uid := os.Getuid()
	cred, err := ResolveCredential("", "", &uid, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred == nil {
		t.Fatal("expected non-nil credential")
	}
	want := uint32(os.Getgid())
	if cred.Gid != want {
		t.Errorf("gid=%d, want %d (current process gid); numeric user-id without group-id must not default to root group (0)", cred.Gid, want)
	}
}

// TestResolveCredentialGroupOnlySupplementaryGroups covers M-36: when only a
// group is specified the supplementary groups must be restricted to just that
// GID, not nil (which would inherit the parent's full supplementary group list).
func TestResolveCredentialGroupOnlySupplementaryGroups(t *testing.T) {
	t.Parallel()
	gid := 1000
	cred, err := ResolveCredential("", "", nil, &gid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred == nil {
		t.Fatal("expected non-nil credential")
	}
	if len(cred.Groups) != 1 || cred.Groups[0] != 1000 {
		t.Errorf("Groups=%v, want [1000] (must restrict supplementary groups to target GID)", cred.Groups)
	}
}
