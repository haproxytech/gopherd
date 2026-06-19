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
	"fmt"
	"io"
	"log"
	"log/syslog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// TargetConfig defines a log forwarding target.
type TargetConfig struct {
	Labels   map[string]string // custom metadata
	Type     string            // "syslog" or "file"
	Location string            // e.g. "udp://logs.example.com:514" or "/var/log/app.log"
	// MaxSize is a human-readable byte size (e.g. "10MiB") at which a file
	// target rotates. Empty = no rotation. File targets only; ignored for syslog.
	MaxSize  string
	Services []string // filter: only these service names
	// MaxFiles is the number of rotated files to keep. Values <= 0 default to
	// defaultMaxFiles when MaxSize is set.
	MaxFiles int
	// Compress enables gzip compression of rotated files. Only meaningful for
	// file targets with MaxSize set.
	Compress bool
}

// Target wraps a log forwarding destination.
type Target struct {
	Writer   io.WriteCloser
	services map[string]bool
	name     string
	cfg      TargetConfig
}

// NewTarget creates a new log target from config.
func NewTarget(name string, cfg TargetConfig) (*Target, error) {
	svcSet := make(map[string]bool)
	for _, s := range cfg.Services {
		svcSet[s] = true
	}

	lt := &Target{
		name:     name,
		cfg:      cfg,
		services: svcSet,
	}

	switch cfg.Type {
	case "syslog":
		w, err := openSyslog(cfg.Location)
		if err != nil {
			return nil, fmt.Errorf("log-target %s: %w", name, err)
		}
		lt.Writer = w
	case "file":
		w, err := openFile(cfg.Location, cfg)
		if err != nil {
			return nil, fmt.Errorf("log-target %s: %w", name, err)
		}
		lt.Writer = w
	default:
		return nil, fmt.Errorf("log-target %s: unsupported type %q (supported: syslog, file)", name, cfg.Type)
	}

	return lt, nil
}

// AppliesTo returns whether this target should receive logs from the given service.
func (lt *Target) AppliesTo(serviceName string) bool {
	if len(lt.services) == 0 {
		return true // no filter = all services
	}
	return lt.services[serviceName]
}

// Close closes the target writer.
func (lt *Target) Close() {
	if lt.Writer != nil {
		if err := lt.Writer.Close(); err != nil {
			log.Printf("log-target %s: close: %v", lt.name, err)
		}
	}
}

// syslogInfoer is the subset of *syslog.Writer methods needed by syslogWriter,
// extracted as an interface to allow test doubles.
type syslogInfoer interface {
	Info(m string) error
	Close() error
}

// syslogWriter wraps syslog.Writer to implement io.WriteCloser with line-level writes.
type syslogWriter struct {
	w syslogInfoer
}

func openSyslog(location string) (io.WriteCloser, error) {
	u, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("parse syslog location %q: %w", location, err)
	}

	network := u.Scheme // "udp" or "tcp"
	addr := u.Host

	if network == "" || addr == "" {
		return nil, fmt.Errorf("syslog location must be like udp://host:port or tcp://host:port, got %q", location)
	}

	w, err := syslog.Dial(network, addr, syslog.LOG_INFO|syslog.LOG_DAEMON, "gopherd")
	if err != nil {
		return nil, fmt.Errorf("dial syslog %s://%s: %w", network, addr, err)
	}

	return &syslogWriter{w: w}, nil
}

func (sw *syslogWriter) Write(p []byte) (int, error) {
	if err := sw.w.Info(sanitize(p)); err != nil {
		return 0, err
	}
	return len(p), nil
}

// sanitize strips control characters (except newline and tab) to block
// services from injecting ANSI escapes or carriage returns into log entries.
// Fast path: zero-alloc unsafe.String aliasing p. Slow path: builds a clean
// copy only from the first bad byte onward.
//
// When no control chars are present the result aliases p, so callers must
// consume it synchronously before p is mutated. syslog.Writer.Info satisfies
// this; it retains nothing past return.
func sanitize(p []byte) string {
	firstBad := -1
	for i, b := range p {
		if b < 0x20 && b != '\n' && b != '\t' {
			firstBad = i
			break
		}
	}
	if firstBad < 0 {
		if len(p) == 0 {
			return ""
		}
		return unsafe.String(unsafe.SliceData(p), len(p))
	}
	buf := make([]byte, firstBad, len(p))
	copy(buf, p[:firstBad])
	for i := firstBad; i < len(p); i++ {
		if p[i] >= 0x20 || p[i] == '\n' || p[i] == '\t' {
			buf = append(buf, p[i])
		}
	}
	return unsafe.String(unsafe.SliceData(buf), len(buf))
}

func (sw *syslogWriter) Close() error {
	return sw.w.Close()
}

// openFile opens a log file for append-only writing. The path must be absolute,
// and every ancestor must be a real directory (not a symlink) so gopherd running
// as root cannot be tricked into redirecting writes; O_NOFOLLOW guards the leaf.
//
// A TOCTOU window remains between the ancestor walk and OpenFile. Closing it
// needs openat2(2) with RESOLVE_NO_SYMLINKS, absent from stdlib syscall (the
// project has a zero-external-dependency policy). Exploiting the window requires
// write access to a root-owned directory, a higher privilege than this defends.
//
// cfg supplies the rotation fields. With MaxSize empty the writer tracks size
// but never rotates.
func openFile(location string, cfg TargetConfig) (io.WriteCloser, error) {
	path := strings.TrimPrefix(location, "file://")
	if path == "" {
		return nil, fmt.Errorf("file log target requires a path")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("file log target path must be absolute: %s", path)
	}
	path = filepath.Clean(path)

	if err := checkAncestorsNotSymlinked(path); err != nil {
		return nil, err
	}

	maxSize, err := parseByteSize(cfg.MaxSize)
	if err != nil {
		return nil, fmt.Errorf("file log target %s: max-size: %w", path, err)
	}
	maxFiles := cfg.MaxFiles
	if maxSize > 0 && maxFiles <= 0 {
		maxFiles = defaultMaxFiles
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", path, err)
	}
	// Service output may carry secrets: reject a pre-seeded file owned by
	// another uid or world-accessible. Checked via the fd to avoid TOCTOU.
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("stat log file %s: %w", path, err)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		euid := uint32(os.Geteuid())
		if st.Uid != 0 && st.Uid != euid {
			_ = f.Close()
			return nil, fmt.Errorf("log file %s is owned by uid %d (expected root or uid %d); refusing to append", path, st.Uid, euid)
		}
		if perm := info.Mode().Perm(); perm&0o006 != 0 {
			_ = f.Close()
			return nil, fmt.Errorf("log file %s is world-accessible (mode %04o); refusing to append (service output may contain secrets)", path, perm)
		}
	}
	// Seed size from the existing file so appends honour the rotation
	// threshold immediately, not only after the first gopherd-written byte.
	initialSize := info.Size()
	return &rotatingFileWriter{
		f:        f,
		path:     path,
		size:     initialSize,
		maxSize:  maxSize,
		maxFiles: maxFiles,
		compress: cfg.Compress,
	}, nil
}

// checkAncestorsNotSymlinked walks every ancestor of path from "/" down to
// the immediate parent and rejects any that is a symlink or not a directory.
// path must be absolute and already filepath.Clean'd.
func checkAncestorsNotSymlinked(path string) error {
	parent := filepath.Dir(path)
	// filepath.Clean guarantees no trailing separator and no "." / ".." segments.
	cur := "/"
	rel := strings.TrimPrefix(parent, "/")
	if rel == "" {
		// path is directly under "/" — nothing to walk.
		return nil
	}
	for comp := range strings.SplitSeq(rel, "/") {
		cur = filepath.Join(cur, comp)
		info, err := os.Lstat(cur)
		if err != nil {
			return fmt.Errorf("log path ancestor %s: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("log path ancestor %s is a symlink", cur)
		}
		if !info.IsDir() {
			return fmt.Errorf("log path ancestor %s is not a directory", cur)
		}
	}
	return nil
}
