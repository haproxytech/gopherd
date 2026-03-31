package version

import (
	"testing"
)

func TestSetPopulatesFields(t *testing.T) {
	// Reset to defaults
	Repo = ""
	Version = "dev"
	Tag = "dev"
	CommitDate = ""

	err := Set()
	if err != nil {
		t.Fatalf("Set() returned error: %v", err)
	}

	if Repo == "" {
		t.Error("expected Repo to be populated")
	}
	// Version should be modified from the bare default — it gets a commit suffix appended.
	// During `go test` it becomes "dev.unknown" (no VCS info).
	if Version == "dev" {
		t.Error("expected Version to be changed from bare 'dev'")
	}
	// Tag may remain "dev" when run via `go test` (no VCS info), so just check it's not empty.
	if Tag == "" {
		t.Error("expected Tag to be non-empty")
	}
}

func TestSetIdempotent(t *testing.T) {
	err := Set()
	if err != nil {
		t.Fatalf("first Set() error: %v", err)
	}
	v1 := Version

	err = Set()
	if err != nil {
		t.Fatalf("second Set() error: %v", err)
	}
	if Version != v1 {
		t.Errorf("Version changed between calls: %q vs %q", v1, Version)
	}
}
