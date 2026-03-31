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
