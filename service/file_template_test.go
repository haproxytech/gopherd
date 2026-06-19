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
	"syscall"
	"testing"
	"time"
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

func TestExpandFileRefsFollow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "secret")
	if err := os.WriteFile(target, []byte("topsecret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	// Relative symlink: follow confines to the file's directory via os.Root,
	// which treats absolute targets as escapes, so symlinks must be relative.
	if err := os.Symlink("secret", link); err != nil {
		t.Fatal(err)
	}

	// K8s secret-volume layout: key -> ..data/key, ..data -> ..<timestamp>/.
	mount := filepath.Join(dir, "mount")
	verDir := filepath.Join(mount, "..2026_06_10")
	if err := os.MkdirAll(verDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(verDir, "token"), []byte("k8svalue\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..2026_06_10", filepath.Join(mount, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", "token"), filepath.Join(mount, "token")); err != nil {
		t.Fatal(err)
	}
	k8sKey := filepath.Join(mount, "token")

	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink("gone", dangling); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		in      string
		wantOut string
	}{
		{"follow leaf symlink", `{{file "` + link + `" follow}}`, "topsecret\n"},
		{"follow with trim", `{{file "` + link + `" follow trim}}`, "topsecret"},
		{"trim follow order", `{{file "` + link + `" trim follow}}`, "topsecret"},
		{"follow k8s secret layout", `{{file "` + k8sKey + `" follow trim}}`, "k8svalue"},
		{"follow with default on existing", `{{file "` + link + `" follow trim:-fallback}}`, "topsecret"},
		{"follow dangling symlink uses default", `{{file "` + dangling + `" follow:-fallback}}`, "fallback"},
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

// follow opts out of symlink rejection only; a dangling symlink without a
// default is still a hard error, and a symlinked directory target is still
// not a regular file.
func TestExpandFileRefsFollowErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink("gone", dangling); err != nil {
		t.Fatal(err)
	}
	// Relative symlink to a directory within the file's directory: follow
	// resolves it but the not-a-regular-file check still rejects it.
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(dir, "dirlink")
	if err := os.Symlink("sub", dirLink); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		in      string
		wantSub string
	}{
		{"dangling without default", `{{file "` + dangling + `" follow}}`, "no such file"},
		{"symlink to directory", `{{file "` + dirLink + `" follow}}`, "not a regular file"},
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

// follow still confines resolution to the file's directory: a symlink escaping
// it (the swap-to-/etc/shadow attack) is refused, as is any absolute target.
func TestExpandFileRefsFollowConfinesToDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A "secret" that lives OUTSIDE the referenced file's directory.
	outside := filepath.Join(dir, "outside-secret")
	if err := os.WriteFile(outside, []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(dir, "inner")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	// Relative symlink escaping inner/ via ../, and an absolute symlink (even
	// one pointing back inside dir) — both must be refused under confinement.
	escRel := filepath.Join(inner, "escape-rel")
	if err := os.Symlink(filepath.Join("..", "outside-secret"), escRel); err != nil {
		t.Fatal(err)
	}
	absLink := filepath.Join(inner, "abs")
	if err := os.Symlink(outside, absLink); err != nil {
		t.Fatal(err)
	}

	tests := []struct{ name, in string }{
		{"relative ../ escape refused", `{{file "` + escRel + `" follow}}`},
		{"absolute symlink refused", `{{file "` + absLink + `" follow}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, err := ExpandFileRefs(tt.in)
			if err == nil {
				t.Fatalf("expected confinement error, got %q", out)
			}
			if strings.Contains(out, "TOPSECRET") {
				t.Fatalf("leaked secret from outside the directory: %q", out)
			}
		})
	}
}

// Both paths open with O_NONBLOCK so a FIFO target cannot block open() — and
// thus PID 1's config load — before the not-a-regular-file check rejects it.
func TestExpandFileRefsFifoDoesNotHang(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	link := filepath.Join(dir, "fifolink")
	if err := os.Symlink("fifo", link); err != nil {
		t.Fatal(err)
	}

	tests := []struct{ name, in string }{
		{"follow symlink to fifo", `{{file "` + link + `" follow}}`},
		{"direct fifo no follow", `{{file "` + fifo + `"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			done := make(chan struct{})
			var out string
			var err error
			go func() {
				out, err = ExpandFileRefs(tt.in)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("ExpandFileRefs hung opening a FIFO")
			}
			if err == nil {
				t.Fatalf("expected not-a-regular-file error, got %q", out)
			}
			if !strings.Contains(err.Error(), "not a regular file") {
				t.Errorf("error = %v, want 'not a regular file'", err)
			}
		})
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
