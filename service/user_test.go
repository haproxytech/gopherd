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
	"testing"
)

func TestResolveCredentialNil(t *testing.T) {
	cred, err := ResolveCredential("", "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred != nil {
		t.Error("expected nil credential")
	}
}

func TestResolveCredentialByID(t *testing.T) {
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

func TestResolveCredentialGroupOnly(t *testing.T) {
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
