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
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// defaultMaxFiles is the number of rotated files kept when the user enables
// rotation without specifying max-files.
const defaultMaxFiles = 5

// parseByteSize parses human-readable byte sizes:
// "10MiB", "100MB", "512KiB", "1GB", "42" (bare number = bytes).
// Empty input returns (0, nil) so callers can treat unset as "no rotation".
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	end := 0
	sawDot := false
	for end < len(s) {
		c := s[end]
		if c >= '0' && c <= '9' {
			end++
			continue
		}
		if c == '.' && !sawDot {
			sawDot = true
			end++
			continue
		}
		break
	}
	if end == 0 {
		return 0, fmt.Errorf("parse byte size %q: missing number", s)
	}
	num, err := strconv.ParseFloat(s[:end], 64)
	if err != nil {
		return 0, fmt.Errorf("parse byte size %q: %w", s, err)
	}
	unit := strings.TrimSpace(s[end:])
	var mul float64
	switch unit {
	case "", "B":
		mul = 1
	case "KB":
		mul = 1000
	case "KiB":
		mul = 1024
	case "MB":
		mul = 1_000_000
	case "MiB":
		mul = 1024 * 1024
	case "GB":
		mul = 1_000_000_000
	case "GiB":
		mul = 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("parse byte size %q: unknown unit %q (use B, KB, KiB, MB, MiB, GB, GiB)", s, unit)
	}
	v := num * mul
	if v <= 0 {
		return 0, fmt.Errorf("parse byte size %q: must be positive", s)
	}
	// Guard the float->int64 narrowing: an out-of-range float (huge value or
	// +Inf) converts implementation-defined — amd64 wraps negative, silently
	// disabling rotation. float64(math.MaxInt64) is exactly 2^63, so reject >=.
	if v >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("parse byte size %q: too large (max %d bytes)", s, int64(math.MaxInt64))
	}

	return int64(v), nil
}

// rotatingFileWriter is a file-backed log writer with optional size-triggered
// rotation. Writes pass through sanitize() before hitting disk. When maxSize
// is zero, rotation is disabled and the writer behaves like a plain append.
//
// Multiple services can share a single log target, so their PrefixWriters
// (each with its own mutex) can invoke Write concurrently. fw.mu serialises
// those calls, which is required for the size counter and for the rotate()
// critical section. Without rotation the serialisation is redundant but
// cheap compared to the syscall already in the write path.
//
// Rotation (including gzip compression when enabled) is synchronous so
// ordering is predictable and we do not need a background worker. For the
// usual file sizes (MiB range) gzip latency is tens of milliseconds.
type rotatingFileWriter struct {
	f        *os.File
	path     string
	size     int64
	maxSize  int64 // 0 disables rotation
	maxFiles int
	compress bool
	mu       sync.Mutex
}

func (fw *rotatingFileWriter) Write(p []byte) (int, error) {
	clean := sanitize(p)

	fw.mu.Lock()
	defer fw.mu.Unlock()

	// Rotate before writing when this call would cross the threshold.
	// Skip rotation on the very first write of an empty file so an oversized
	// single line does not trigger a pointless rotation of a 0-byte file.
	if fw.maxSize > 0 && fw.size > 0 && fw.size+int64(len(clean)) > fw.maxSize {
		if err := fw.rotate(); err != nil {
			log.Printf("log rotate %s: %v", fw.path, err)
			// Fall through: prefer continuing to write to the (possibly
			// still-open) file over losing the line.
		}
	}

	if fw.f == nil {
		// Previous rotate failed to reopen. Returning an error lets
		// PrefixWriter and its other extras continue without us, but
		// signals the operator via logs.
		return 0, fmt.Errorf("log file %s not open", fw.path)
	}
	n, err := io.WriteString(fw.f, clean)
	fw.size += int64(n)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (fw *rotatingFileWriter) Close() error {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if fw.f == nil {
		return nil
	}
	err := fw.f.Close()
	fw.f = nil
	return err
}

// rotate closes the current file, shifts rotated suffixes up by one, and
// reopens a fresh file at fw.path. Caller must hold fw.mu.
//
// Naming: rotated files are <path>.1, <path>.2, ... <path>.N. When compress
// is enabled they become <path>.1.gz, <path>.2.gz, .... Both naming schemes
// are tolerated during shifts so a config flip of the compress flag leaves
// no files orphaned forever.
func (fw *rotatingFileWriter) rotate() error {
	// Re-validate the directory chain before rename/remove/reopen: openFile
	// checked once, but an ancestor could be swapped for a symlink before this
	// late rotation, redirecting these root operations. Returning early leaves
	// fw.f open so Write keeps logging. Restores openFile's guarantee; the same
	// residual check-to-syscall TOCTOU window remains.
	if err := checkAncestorsNotSymlinked(fw.path); err != nil {
		return fmt.Errorf("rotate %s: %w", fw.path, err)
	}
	if err := fw.f.Close(); err != nil {
		log.Printf("log rotate %s: close: %v", fw.path, err)
	}
	fw.f = nil

	plain := func(i int) string { return fmt.Sprintf("%s.%d", fw.path, i) }
	gz := func(i int) string { return fmt.Sprintf("%s.%d.gz", fw.path, i) }

	// Drop the oldest slot in both naming schemes.
	_ = os.Remove(plain(fw.maxFiles))
	_ = os.Remove(gz(fw.maxFiles))

	// Shift .N-1 → .N, down to .1 → .2. Handle both suffix styles so a
	// previous run with a different compress setting does not leak files.
	for i := fw.maxFiles - 1; i >= 1; i-- {
		for _, src := range []string{gz(i), plain(i)} {
			if _, err := os.Lstat(src); err != nil {
				continue
			}
			dst := plain(i + 1)
			if strings.HasSuffix(src, ".gz") {
				dst = gz(i + 1)
			}
			if err := os.Rename(src, dst); err != nil {
				log.Printf("log rotate %s: rename %s -> %s: %v", fw.path, src, dst, err)
			}
		}
	}

	// Move the live file to .1, then optionally gzip into .1.gz.
	rotated := plain(1)
	if err := os.Rename(fw.path, rotated); err != nil {
		return fmt.Errorf("rename %s: %w", fw.path, err)
	}
	if fw.compress {
		if err := gzipFile(rotated, gz(1)); err != nil {
			log.Printf("log rotate %s: gzip %s: %v", fw.path, rotated, err)
			// Leave the uncompressed .1 in place so no data is lost.
		} else {
			_ = os.Remove(rotated)
		}
	}

	f, err := os.OpenFile(fw.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o640)
	if err != nil {
		return fmt.Errorf("reopen %s: %w", fw.path, err)
	}
	fw.f = f
	fw.size = 0
	return nil
}

func gzipFile(src, dst string) (err error) {
	in, err := os.Open(src) //nolint:gosec // src built from validated log path
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o640)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()

	gw := gzip.NewWriter(out)
	if _, err = io.Copy(gw, in); err != nil {
		_ = gw.Close()
		return err
	}
	return gw.Close()
}
