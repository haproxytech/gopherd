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
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseByteSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"  ", 0, false},
		{"10", 10, false},
		{"10B", 10, false},
		{"10KB", 10_000, false},
		{"10KiB", 10 * 1024, false},
		{"10MB", 10_000_000, false},
		{"10MiB", 10 * 1024 * 1024, false},
		{"1GB", 1_000_000_000, false},
		{"1GiB", 1024 * 1024 * 1024, false},
		{"  10 MiB  ", 10 * 1024 * 1024, false}, // whitespace around unit
		{"1.5MiB", int64(1.5 * 1024 * 1024), false},
		{"bogus", 0, true},
		{"10TiB", 0, true}, // unsupported unit
		{"0MiB", 0, true},  // non-positive
		{"-5MiB", 0, true}, // leading '-' not accepted
	}
	for _, tc := range tests {
		got, err := parseByteSize(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseByteSize(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("parseByteSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// openRotating returns a rotatingFileWriter backed by a real file in dir.
// Helper to keep tests terse.
func openRotating(t *testing.T, dir, maxSize string, maxFiles int, compress bool) (*rotatingFileWriter, string) {
	t.Helper()
	path := filepath.Join(dir, "app.log")
	w, err := openFile("file://"+path, TargetConfig{
		Type:     "file",
		Location: path,
		MaxSize:  maxSize,
		MaxFiles: maxFiles,
		Compress: compress,
	})
	if err != nil {
		t.Fatalf("openFile: %v", err)
	}
	rfw, ok := w.(*rotatingFileWriter)
	if !ok {
		t.Fatalf("openFile returned %T, want *rotatingFileWriter", w)
	}
	return rfw, path
}

func TestRotatingFileWriterNoRotationWhenMaxSizeUnset(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fw, path := openRotating(t, dir, "", 0, false)
	defer fw.Close()

	// Write 1 MiB of data; with no max-size, no rotation should happen.
	line := strings.Repeat("x", 1024) + "\n"
	for range 1024 {
		if _, err := fw.Write([]byte(line)); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// The .1 suffix file must not exist.
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("unexpected rotated file %s (err=%v)", path+".1", err)
	}
}

func TestRotatingFileWriterRotatesAtThreshold(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// 100-byte threshold, 3 rotated files kept.
	fw, path := openRotating(t, dir, "100B", 3, false)
	defer fw.Close()

	// Write in 30-byte chunks so we cross the threshold a few times.
	chunk := []byte(strings.Repeat("a", 29) + "\n") // 30 bytes
	for range 12 {                                  // 360 bytes total → 3+ rotations
		if _, err := fw.Write(chunk); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	// After 360 bytes with 100-byte threshold, we expect .1, .2, .3 to exist
	// and no .4 (because maxFiles=3).
	for _, suffix := range []string{".1", ".2", ".3"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Errorf("expected rotated file %s, got err %v", path+suffix, err)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Errorf("expected no .4 file beyond maxFiles=3, err=%v", err)
	}
}

func TestRotatingFileWriterShiftsSuffixes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fw, path := openRotating(t, dir, "50B", 3, false)
	defer fw.Close()

	// Each 25-byte write either stays under 50B or crosses it exactly.
	// Timeline (maxSize=50):
	//   W1,W2 "AAA..." fill to 50 (no rotation).
	//   W3 "BBB..." triggers rotation 1: .1 = "AA..AA..", current = "BB..".
	//   W4 "BBB..." fills current to 50 (no rotation).
	//   W5 "CCC..." triggers rotation 2: .1 = "BB..BB..", .2 = "AA..AA..",
	//      current = "CC..".
	// After this, .1 must contain B data and .2 must contain A data.
	aPayload := []byte("AAA:" + strings.Repeat("a", 20) + "\n") // 25 bytes
	bPayload := []byte("BBB:" + strings.Repeat("b", 20) + "\n")
	cPayload := []byte("CCC:" + strings.Repeat("c", 20) + "\n")
	fw.Write(aPayload)
	fw.Write(aPayload)
	fw.Write(bPayload) // triggers rotation 1
	fw.Write(bPayload)
	fw.Write(cPayload) // triggers rotation 2

	got1, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read .1: %v", err)
	}
	if !strings.Contains(string(got1), "BBB:") || strings.Contains(string(got1), "AAA:") {
		t.Errorf(".1 should contain only B data after two rotations, got %q", got1)
	}
	got2, err := os.ReadFile(path + ".2")
	if err != nil {
		t.Fatalf("read .2: %v", err)
	}
	if !strings.Contains(string(got2), "AAA:") || strings.Contains(string(got2), "BBB:") {
		t.Errorf(".2 should contain only A data (shifted from .1), got %q", got2)
	}
}

func TestRotatingFileWriterCompressesRotated(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fw, path := openRotating(t, dir, "50B", 3, true)
	defer fw.Close()

	// Produce one rotation.
	payload := []byte(strings.Repeat("a", 29) + "\n") // 30 bytes
	for range 3 {
		fw.Write(payload)
	}

	// The compressed file should exist; the uncompressed .1 should not.
	if _, err := os.Stat(path + ".1.gz"); err != nil {
		t.Fatalf("expected %s, err=%v", path+".1.gz", err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("uncompressed .1 should have been removed, err=%v", err)
	}

	// Decompress and verify content.
	f, err := os.Open(path + ".1.gz") //nolint:gosec // test-only path
	if err != nil {
		t.Fatalf("open gz: %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	content, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gz: %v", err)
	}
	if !strings.Contains(string(content), "aaaa") {
		t.Errorf("decompressed content unexpected: %q", content)
	}
}

func TestRotatingFileWriterSeedsSizeFromExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	// Pre-seed the file with 40 bytes.
	if err := os.WriteFile(path, []byte(strings.Repeat("z", 40)), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}
	w, err := openFile("file://"+path, TargetConfig{
		Type:     "file",
		Location: path,
		MaxSize:  "50B",
		MaxFiles: 3,
	})
	if err != nil {
		t.Fatalf("openFile: %v", err)
	}
	defer w.Close()

	// Writing 15 more bytes should exceed 50 and trigger rotation immediately
	// — proof that the initial size was picked up from the existing file.
	if _, err := w.Write([]byte(strings.Repeat("a", 14) + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("expected rotation triggered by pre-existing size; .1 missing: %v", err)
	}
}

// TestOpenFileRejectsWorldReadable verifies that a pre-existing log file with
// world-accessible permissions is refused: service output may contain secrets,
// so appending to a file other uids can read (or a sibling pre-seeded) must
// fail rather than silently leak.
func TestOpenFileRejectsWorldReadable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "leaky.log")
	// Pre-create world-readable (and -writable) before gopherd opens it.
	if err := os.WriteFile(path, []byte("pre"), 0o666); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// WriteFile honours umask, so force the bits explicitly.
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	_, err := openFile("file://"+path, TargetConfig{Type: "file", Location: path})
	if err == nil {
		t.Fatal("expected openFile to reject a world-accessible pre-existing file")
	}
	if !strings.Contains(err.Error(), "world-accessible") {
		t.Errorf("error %q does not mention world-accessible", err.Error())
	}
}

// TestOpenFileAcceptsOwnedRestricted is the positive control: a pre-existing
// file owned by us with non-world-accessible mode opens fine.
func TestOpenFileAcceptsOwnedRestricted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.log")
	if err := os.WriteFile(path, []byte("pre"), 0o640); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	w, err := openFile("file://"+path, TargetConfig{Type: "file", Location: path})
	if err != nil {
		t.Fatalf("openFile rejected an owned 0640 file: %v", err)
	}
	w.Close()
}

func TestRotatingFileWriterDefaultMaxFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// max-files omitted → defaultMaxFiles.
	fw, _ := openRotating(t, dir, "50B", 0, false)
	defer fw.Close()
	if fw.maxFiles != defaultMaxFiles {
		t.Errorf("maxFiles = %d, want %d (default)", fw.maxFiles, defaultMaxFiles)
	}
}

func TestRotatingFileWriterInvalidMaxSize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	_, err := openFile("file://"+path, TargetConfig{
		Type:    "file",
		MaxSize: "bogus",
	})
	if err == nil {
		t.Error("expected error for invalid max-size")
	}
}

func TestRotatingFileWriterSanitizes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fw, path := openRotating(t, dir, "", 0, false)
	defer fw.Close()
	// \x1b is an ANSI escape byte and must be stripped by sanitize().
	if _, err := fw.Write([]byte("hello\x1b[31mred\x1b[0m\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(got), "\x1b") {
		t.Errorf("log file contains raw escape: %q", got)
	}
}

func TestRotatingFileWriterReloadConfigCloseAndReopen(t *testing.T) {
	t.Parallel()
	// Emulates the reload path in daemon.go: old target is closed, new target
	// opened at the same path. The new writer must pick up the existing size
	// and continue rotating correctly.
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	w1, err := openFile("file://"+path, TargetConfig{Type: "file", MaxSize: "100B", MaxFiles: 3})
	if err != nil {
		t.Fatalf("openFile 1: %v", err)
	}
	// Write 60 bytes (no rotation yet).
	w1.Write([]byte(strings.Repeat("a", 59) + "\n"))
	if err := w1.Close(); err != nil {
		t.Fatalf("close 1: %v", err)
	}

	w2, err := openFile("file://"+path, TargetConfig{Type: "file", MaxSize: "100B", MaxFiles: 3})
	if err != nil {
		t.Fatalf("openFile 2: %v", err)
	}
	defer w2.Close()

	// Writing 50 more bytes should trigger rotation because the seeded size
	// is 60, not 0.
	w2.Write([]byte(strings.Repeat("b", 49) + "\n"))
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("post-reload writer did not rotate (seed size lost?): %v", err)
	}
}
