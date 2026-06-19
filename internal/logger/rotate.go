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

// defaultMaxFiles is used when rotation is enabled without an explicit max-files.
const defaultMaxFiles = 5

// parseByteSize parses human-readable byte sizes (e.g. "10MiB", "100MB", "42").
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
	// Guard the float->int64 narrowing: out-of-range values convert
	// implementation-defined (amd64 wraps negative, silently disabling
	// rotation). float64(math.MaxInt64) is exactly 2^63, so reject >=.
	if v >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("parse byte size %q: too large (max %d bytes)", s, int64(math.MaxInt64))
	}

	return int64(v), nil
}

// rotatingFileWriter is a file-backed log writer with optional size-triggered
// rotation. Writes pass through sanitize() before hitting disk. maxSize zero
// disables rotation.
//
// Multiple services can share a target, so their PrefixWriters can invoke
// Write concurrently; fw.mu serialises those calls to protect the size counter
// and the rotate() critical section.
//
// Rotation (including gzip) is synchronous to keep ordering predictable without
// a background worker. For MiB-range files gzip latency is tens of ms.
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

	// Rotate before writing when this call would cross the threshold. The
	// size > 0 check avoids rotating a 0-byte file for one oversized line.
	if fw.maxSize > 0 && fw.size > 0 && fw.size+int64(len(clean)) > fw.maxSize {
		if err := fw.rotate(); err != nil {
			log.Printf("log rotate %s: %v", fw.path, err)
			// Fall through: prefer writing to the still-open file over
			// losing the line.
		}
	}

	if fw.f == nil {
		// Previous rotate failed to reopen; signal the operator via logs.
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
// Rotated files are <path>.1 .. <path>.N (or .gz when compress is enabled).
// Both naming schemes are handled during shifts so flipping the compress flag
// leaves no files orphaned.
func (fw *rotatingFileWriter) rotate() error {
	// Re-validate the directory chain: an ancestor could be swapped for a
	// symlink between openFile and this rotation, redirecting these root
	// operations. Returning early leaves fw.f open so Write keeps logging.
	// The same residual check-to-syscall TOCTOU window remains.
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

	// Shift .N-1 → .N, down to .1 → .2, handling both suffix styles.
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
