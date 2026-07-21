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

package logger

import (
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errSyslogInfoer is a fake syslogInfoer that always returns an error from Info.
type errSyslogInfoer struct{ err error }

func (e *errSyslogInfoer) Info(_ string) error { return e.err }
func (e *errSyslogInfoer) Close() error        { return nil }

// nopSyslogInfoer discards messages; used for allocation benchmarks.
type nopSyslogInfoer struct{}

func (nopSyslogInfoer) Info(_ string) error { return nil }
func (nopSyslogInfoer) Close() error        { return nil }

// BenchmarkSyslogTargetWriteClean exercises the per-line fast path (no
// control chars) and asserts zero allocations. PERFORMANCE.md claims this
// path is zero-alloc; this benchmark proves it.
func BenchmarkSyslogTargetWriteClean(b *testing.B) {
	sw := &syslogWriter{w: nopSyslogInfoer{}}
	p := []byte("2026-04-06T12:00:00.000Z [my-service] some clean log output line\n")
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_, _ = sw.Write(p)
	}
}

// BenchmarkSanitizeFast exercises sanitize() directly on a clean line.
func BenchmarkSanitizeFast(b *testing.B) {
	p := []byte("2026-04-06T12:00:00.000Z [my-service] some clean log output line\n")
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		_ = sanitize(p)
	}
}

// TestSyslogWriterIOContractOnError verifies that syslogWriter.Write returns
// n=0 (not n=len(p)) when Info returns an error. Returning n>0 with a non-nil
// error violates the io.Writer contract and confuses callers that inspect n.
func TestSyslogWriterIOContractOnError(t *testing.T) {
	t.Parallel()
	sw := &syslogWriter{w: &errSyslogInfoer{err: errors.New("syslog down")}}
	p := []byte("hello log line")
	n, err := sw.Write(p)
	if err == nil {
		t.Error("expected non-nil error from Write")
	}
	if n != 0 {
		t.Errorf("n=%d, want 0: io.Writer contract requires n<len(p) on error", n)
	}
}

func TestTargetAppliesToAll(t *testing.T) {
	t.Parallel()
	lt := &Target{services: map[string]bool{}}
	if !lt.AppliesTo("anything") {
		t.Error("empty filter should apply to all")
	}
}

func TestTargetAppliesToFiltered(t *testing.T) {
	t.Parallel()
	lt := &Target{services: map[string]bool{"app": true, "sidecar": true}}
	if !lt.AppliesTo("app") {
		t.Error("should apply to app")
	}
	if lt.AppliesTo("other") {
		t.Error("should not apply to other")
	}
}

func TestTargetCloseNilWriter(t *testing.T) {
	t.Parallel()
	lt := &Target{}
	lt.Close() // should not panic
}

func TestNewTargetUnsupportedType(t *testing.T) {
	t.Parallel()
	_, err := NewTarget("bad", TargetConfig{Type: "unknown", Location: "udp://localhost:514"})
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

func TestNewTargetInvalidSyslogLocation(t *testing.T) {
	t.Parallel()
	_, err := NewTarget("bad", TargetConfig{Type: "syslog", Location: "not-a-url"})
	if err == nil {
		t.Error("expected error for invalid location")
	}
}

func TestNewTargetSyslogEmptyLocation(t *testing.T) {
	t.Parallel()
	_, err := NewTarget("bad", TargetConfig{Type: "syslog", Location: ""})
	if err == nil {
		t.Error("expected error for empty location")
	}
}

// TestSanitizePreservesNewlines verifies that the sanitize slow path (triggered by
// a control character < 0x20) must preserve '\n' bytes in the output. Without the
// '\n' exception, multi-line log entries forwarded to syslog/file targets would
// have their newlines stripped, merging separate lines into one long line.
func TestSanitizePreservesNewlines(t *testing.T) {
	t.Parallel()
	// \x01 triggers the slow path; \n must survive.
	input := []byte("line1\x01line2\nline3\n")
	got := sanitize(input)
	if !strings.Contains(got, "\n") {
		t.Errorf("sanitize() stripped newlines: %q", got)
	}
	if strings.Contains(got, "\x01") {
		t.Errorf("sanitize() kept control char \\x01: %q", got)
	}
}

// errOnCloseWriter is a fake io.WriteCloser whose Close always returns an error.
type errOnCloseWriter struct{ err error }

func (e *errOnCloseWriter) Write(p []byte) (int, error) { return len(p), nil }
func (e *errOnCloseWriter) Close() error                { return e.err }

// TestTargetCloseLogsError covers O4: Target.Close() silently discards errors
// from the underlying writer's Close. The fix logs the error so operators can
// diagnose failed syslog or file handle closes.
func TestTargetCloseLogsError(t *testing.T) {
	// NOT parallel: modifies the global log output writer.
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	lt := &Target{
		name:   "test-target",
		Writer: &errOnCloseWriter{err: errors.New("disk full")},
	}
	lt.Close()

	if !strings.Contains(buf.String(), "disk full") {
		t.Errorf("Close() should log writer errors; got log: %q", buf.String())
	}
}

// TestOpenFileRejectsSymlinkedAncestor verifies that openFile refuses a log
// path whose intermediate directory is a symlink, not just the leaf. Lstat
// on the immediate parent and O_NOFOLLOW on the final open only cover the
// leaf, so a symlink higher up in the path would otherwise be traversed.
func TestOpenFileRejectsSymlinkedAncestor(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	// real/                   — the real data directory
	// link -> real            — symlink at an intermediate level
	// link/sub/out.log        — path whose ancestor "link" is a symlink
	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	if err := os.Mkdir(filepath.Join(realDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir real/sub: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	target := filepath.Join(link, "sub", "out.log")
	w, err := openFile("file://"+target, TargetConfig{Type: "file"})
	if err == nil {
		if c, ok := w.(io.Closer); ok {
			_ = c.Close()
		}
		t.Fatalf("expected error for symlinked ancestor, got nil; file created at %s", target)
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error %q does not mention symlink", err.Error())
	}
}

// TestOpenFileAcceptsRealAncestors ensures the ancestor check does not
// reject legitimate paths with only real directories.
func TestOpenFileAcceptsRealAncestors(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	sub := filepath.Join(base, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	target := filepath.Join(sub, "out.log")
	w, err := openFile("file://"+target, TargetConfig{Type: "file"})
	if err != nil {
		t.Fatalf("unexpected error on real-ancestor path: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// captureWriteCloser records everything written, for label-writer tests.
type captureWriteCloser struct{ b strings.Builder }

func (c *captureWriteCloser) Write(p []byte) (int, error) { return c.b.Write(p) }
func (c *captureWriteCloser) Close() error                { return nil }

// TestLabelWriterPrependsLabels verifies target labels are prepended as sorted
// logfmt key=value pairs, quoting values that contain whitespace.
func TestLabelWriterPrependsLabels(t *testing.T) {
	t.Parallel()
	cw := &captureWriteCloser{}
	lw := newLabelWriter(cw, map[string]string{"region": "us east", "env": "production"})
	line := []byte("[app] hello\n")
	n, err := lw.Write(line)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len(line) {
		t.Errorf("n = %d, want %d (bytes of caller input)", n, len(line))
	}
	got := cw.b.String()
	want := `env=production region="us east" [app] hello` + "\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
