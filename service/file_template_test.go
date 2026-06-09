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

func TestExpandFileRefs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("topsecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyFile := filepath.Join(dir, "empty")
	if err := os.WriteFile(emptyFile, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	multiline := filepath.Join(dir, "multi")
	if err := os.WriteFile(multiline, []byte("line1\nline2\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	leadingWS := filepath.Join(dir, "leading")
	if err := os.WriteFile(leadingWS, []byte("  value  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "absent")

	tests := []struct {
		name    string
		in      string
		wantOut string
	}{
		{"no placeholder", "plain", "plain"},
		{"basic expansion", `{{file "` + secret + `"}}`, "topsecret\n"},
		{"trim modifier", `{{file "` + secret + `" trim}}`, "topsecret"},
		{"trim preserves leading whitespace", `{{file "` + leadingWS + `" trim}}`, "  value"},
		{"trim multiline", `{{file "` + multiline + `" trim}}`, "line1\nline2"},
		{"default used for missing file", `{{file "` + missing + `":-fallback}}`, "fallback"},
		{"default unused when file exists", `{{file "` + secret + `" trim:-fallback}}`, "topsecret"},
		{"empty file expands to empty", `{{file "` + emptyFile + `"}}`, ""},
		{"embedded in larger string", `--token={{file "` + secret + `" trim}} --user=bob`, `--token=topsecret --user=bob`},
		{"multiple refs", `{{file "` + secret + `" trim}}:{{file "` + multiline + `" trim}}`, "topsecret:line1\nline2"},
		{"whitespace around tokens", `{{ file "` + secret + `" trim }}`, "topsecret"},
		{"empty default", `{{file "` + missing + `":-}}`, ""},
		{"default with colon-dash", `{{file "` + missing + `":-a:-b}}`, "a:-b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, err := ExpandFileRefs(tt.in)
			if err != nil {
				t.Fatalf("ExpandFileRefs(%q) returned error: %v", tt.in, err)
			}
			if out != tt.wantOut {
				t.Errorf("output: got %q, want %q", out, tt.wantOut)
			}
		})
	}
}

func TestExpandFileRefsErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	missing := filepath.Join(dir, "absent")
	withNul := filepath.Join(dir, "nul")
	if err := os.WriteFile(withNul, []byte("hello\x00world"), 0o600); err != nil {
		t.Fatal(err)
	}
	bigFile := filepath.Join(dir, "huge")
	if err := os.WriteFile(bigFile, make([]byte, maxFileSize+1), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		in      string
		wantSub string
	}{
		{"missing without default", `{{file "` + missing + `"}}`, "no such file"},
		{"relative path", `{{file "relative/path"}}`, "must be absolute"},
		{"directory", `{{file "` + dir + `"}}`, "not a regular file"},
		{"NUL byte", `{{file "` + withNul + `"}}`, "NUL byte"},
		{"size cap", `{{file "` + bigFile + `"}}`, "size cap"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ExpandFileRefs(tt.in)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("error %q does not contain %q", err, tt.wantSub)
			}
		})
	}
}

// A present-but-unreadable path (here: a directory) must NOT fall through to
// the default, even though :-default is present. Operator misconfiguration
// should fail loudly, not silently substitute.
func TestExpandFileRefsReadErrorEvenWithDefault(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	in := `{{file "` + dir + `":-fallback}}`
	if _, err := ExpandFileRefs(in); err == nil {
		t.Fatalf("expected error for unreadable path even with default, got nil")
	}
}

func TestExpandFileRefsRejectsSymlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "secret")
	if err := os.WriteFile(target, []byte("topsecret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// A symlink leaf must be rejected even when its target is a regular file,
	// and must not fall back to the default.
	if _, err := ExpandFileRefs(`{{file "` + link + `":-fallback}}`); err == nil {
		t.Fatalf("expected error for symlinked leaf, got nil")
	}
}

func TestExpandFileRefsRejectsSymlinkedAncestor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(realDir, "secret")
	if err := os.WriteFile(secret, []byte("topsecret"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	// Path reaches a regular file, but through a symlinked ancestor directory.
	if _, err := ExpandFileRefs(`{{file "` + filepath.Join(linkDir, "secret") + `"}}`); err == nil {
		t.Fatalf("expected error for symlinked ancestor, got nil")
	}
}

func BenchmarkExpandFileRefsLiteral(b *testing.B) {
	for b.Loop() {
		_, _ = ExpandFileRefs("no placeholders here")
	}
}

func BenchmarkExpandFileRefsRead(b *testing.B) {
	dir := b.TempDir()
	secret := filepath.Join(dir, "s")
	_ = os.WriteFile(secret, []byte("secretvalue\n"), 0o600)
	in := `{{file "` + secret + `" trim}}`
	b.ResetTimer()
	for b.Loop() {
		_, _ = ExpandFileRefs(in)
	}
}
