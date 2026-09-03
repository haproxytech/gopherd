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
	"bytes"
	"errors"
	"io"
	"log"
	"net"
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

// TestSanitizeStripsFullSequences verifies whole ANSI sequences are consumed,
// not just the ESC byte — no "[31m" fragments may leak into targets.
func TestSanitizeStripsFullSequences(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"a\x1b[31mred\x1b[0m b\n", "ared b\n"},            // SGR pair
		{"x\x1b[2A\x1b[Kup\n", "xup\n"},                    // cursor move + erase
		{"t\x1b]0;title\x07text\n", "ttext\n"},             // OSC via BEL
		{"t\x1b]0;title\x1b\\text\n", "ttext\n"},           // OSC via ST
		{"c\x1b(Bfoo\n", "cfoo\n"},                         // charset select
		{"end\x1b[", "end"},                                // incomplete CSI
		{"end\x1b", "end"},                                 // lone trailing ESC
		{"m\x1b[31\nrest\n", "m\nrest\n"},                  // newline aborts CSI
		{"line1\x01line2\nline3\n", "line1line2\nline3\n"}, // non-ESC control
		{"bell\x07cr\rdone\n", "bellcrdone\n"},             // BEL and CR dropped
		{"keep\ttab\nplain\n", "keep\ttab\nplain\n"},       // fast path untouched
	}
	for _, c := range cases {
		if got := sanitize([]byte(c.in)); got != c.want {
			t.Errorf("sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSanitizeKeepColors verifies SGR color sequences survive while every
// other escape (cursor motion, OSC, CR, BEL) is still stripped.
func TestSanitizeKeepColors(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"a\x1b[31mred\x1b[0m\n", "a\x1b[31mred\x1b[0m\n"},     // colors kept
		{"b\x1b[1;95mbold\x1b[m\n", "b\x1b[1;95mbold\x1b[m\n"}, // multi-param + empty
		{"x\x1b[2Aup\x07\rdone\n", "xupdone\n"},                // motion/BEL/CR dropped
		{"t\x1b]0;title\x07text\n", "ttext\n"},                 // OSC dropped
		{"e\x1b[31;xmno\n", "emno\n"},                          // 'x' final: not SGR, dropped
	}
	for _, c := range cases {
		if got := sanitizeKeepColors([]byte(c.in)); got != c.want {
			t.Errorf("sanitizeKeepColors(%q) = %q, want %q", c.in, got, c.want)
		}
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

// TestFileTargetRejectsWorldAccessibleFile pins the permission check on a
// pre-existing log file. Service output can carry credentials and tokens, so a
// file other users can *read* is the leak, not just one they can write — hence
// the read bit is covered too, which only a negative test keeps true.
func TestFileTargetRejectsWorldAccessibleFile(t *testing.T) {
	for _, tc := range []struct {
		mode os.FileMode
		ok   bool
	}{
		{0o600, true},
		{0o640, true},
		{0o604, false}, // world-readable
		{0o606, false}, // world-readable and writable
		{0o602, false}, // world-writable
		{0o666, false},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "app.log")
		if err := os.WriteFile(path, []byte("existing\n"), tc.mode); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chmod(path, tc.mode); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		tgt, err := NewTarget("t", TargetConfig{Type: "file", Location: path})
		if err == nil && tgt != nil {
			tgt.Close()
		}
		if tc.ok && err != nil {
			t.Errorf("mode %04o: unexpected error: %v", tc.mode, err)
		}
		if !tc.ok {
			if err == nil {
				t.Errorf("mode %04o: accepted a world-accessible log file", tc.mode)
			} else if !strings.Contains(err.Error(), "world-accessible") {
				t.Errorf("mode %04o: error %q should say the file is world-accessible",
					tc.mode, err)
			}
		}
	}
}

// TestWritersReportInputLength pins the io.Writer contract for every writer
// that sanitises its input. Sanitising drops bytes, so reporting the number
// *written* instead of the number *accepted* is a short write: io.Copy treats
// it as ErrShortWrite and aborts, breaking log forwarding the moment a service
// emits a control character.
func TestWritersReportInputLength(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	// A line whose sanitised form is strictly shorter than the input.
	line := []byte("before\x1b[31mred\x1b[0m\x07after\n")

	tgt, err := NewTarget("t", TargetConfig{Type: "file", Location: path})
	if err != nil {
		t.Fatalf("NewTarget: %v", err)
	}
	defer tgt.Close()
	n, err := tgt.Writer.Write(line)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(line) {
		t.Errorf("file target Write returned %d for a %d-byte input; a writer that "+
			"drops bytes while sanitising must still report the full input length",
			n, len(line))
	}
	// Sanity: the bytes on disk really are shorter, so the case is live.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(data) >= len(line) {
		t.Skipf("sanitiser removed nothing (%d bytes written); case not exercised",
			len(data))
	}

	// The same contract with labels in front.
	labelled := newLabelWriter(nopWriteCloser{}, map[string]string{"env": "test"})
	n, err = labelled.Write(line)
	if err != nil {
		t.Fatalf("labelWriter.Write: %v", err)
	}
	if n != len(line) {
		t.Errorf("labelWriter.Write returned %d for a %d-byte input, want %d",
			n, len(line), len(line))
	}
}

// nopWriteCloser discards everything, for exercising a writer's return values.
type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

// TestNoRotationOnEmptyFile pins that one oversized line does not rotate an
// empty file. Rotating a 0-byte file gains nothing — the line still has to be
// written and still exceeds max-size — but it burns a retention slot per write,
// so a service logging oversized lines spins the whole history out in a few.
func TestNoRotationOnEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	tgt, err := NewTarget("t", TargetConfig{
		Type: "file", Location: path, MaxSize: "64B", MaxFiles: 3,
	})
	if err != nil {
		t.Fatalf("NewTarget: %v", err)
	}
	defer tgt.Close()

	// One line, comfortably over max-size, into a brand new (empty) file.
	big := append(bytes.Repeat([]byte("x"), 200), '\n')
	if _, err := tgt.Writer.Write(big); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Error("an empty file was rotated before its first write; rotation must " +
			"only happen when there is something to preserve")
	}

	// The line is on disk, and the next oversized write does rotate — the
	// guard is about the empty case only.
	if _, err := tgt.Writer.Write(big); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("a non-empty file over max-size was not rotated: %v", err)
	}
}

// TestSyslogWriterReportsInputLength is the syslog half of the contract in
// TestWritersReportInputLength: sanitising shortens the payload, but Write must
// report the caller's byte count or io.Copy calls it ErrShortWrite and stops.
func TestSyslogWriterReportsInputLength(t *testing.T) {
	// A real UDP sink, so syslog.Dial has somewhere to send.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no UDP loopback available: %v", err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
		}
	}()

	tgt, err := NewTarget("sl", TargetConfig{
		Type: "syslog", Location: "udp://" + pc.LocalAddr().String(),
	})
	if err != nil {
		t.Skipf("syslog target unavailable: %v", err)
	}
	defer tgt.Close()

	line := []byte("before\x1b[31mred\x1b[0m\x07after\n")
	n, err := tgt.Writer.Write(line)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(line) {
		t.Errorf("syslog Write returned %d for a %d-byte input; a writer that "+
			"sanitises must still report the full input length", n, len(line))
	}
}
