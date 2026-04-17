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
)

// TargetConfig defines a log forwarding target.
type TargetConfig struct {
	Labels   map[string]string // custom metadata
	Type     string            // "syslog"
	Location string            // e.g. "udp://logs.example.com:514"
	Services []string          // filter: only these service names
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
		w, err := openFile(cfg.Location)
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

// syslogInfoer is the subset of *syslog.Writer methods needed by syslogWriter.
// Using an interface allows test doubles without a real syslog connection.
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

// sanitize strips control characters (except newline and tab) from log
// output before forwarding to syslog or file targets. This prevents services
// from injecting ANSI escape sequences or carriage returns into log entries.
// Fast path: if no control characters are present, converts p to string once
// without allocation in the common case. Slow path builds a clean copy only
// from the first bad byte onward, avoiding a redundant string(p) conversion
// at the call site.
func sanitize(p []byte) string {
	// Quick scan: most log lines are clean ASCII.
	firstBad := -1
	for i, b := range p {
		if b < 0x20 && b != '\n' && b != '\t' {
			firstBad = i
			break
		}
	}
	if firstBad < 0 {
		return string(p)
	}
	// Slow path: build result only from the first bad byte onward.
	buf := make([]byte, firstBad, len(p))
	copy(buf, p[:firstBad])
	for i := firstBad; i < len(p); i++ {
		if p[i] >= 0x20 || p[i] == '\n' || p[i] == '\t' {
			buf = append(buf, p[i])
		}
	}
	return string(buf)
}

func (sw *syslogWriter) Close() error {
	return sw.w.Close()
}

// fileWriter wraps *os.File to apply the same control-character sanitisation
// as syslogWriter. This prevents ANSI escape sequences and other control bytes
// from services being written verbatim to log files.
type fileWriter struct {
	f *os.File
}

func (fw *fileWriter) Write(p []byte) (int, error) {
	clean := sanitize(p)
	// io.WriteString uses *os.File's WriteString method directly, avoiding
	// the []byte(clean) allocation that fw.f.Write([]byte(clean)) would require.
	if _, err := io.WriteString(fw.f, clean); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (fw *fileWriter) Close() error {
	return fw.f.Close()
}

// openFile opens a log file for append-only writing. The path must be
// absolute to prevent relative path confusion. The parent directory must
// already exist and must be a real directory — not a symlink — so gopherd
// running as root does not silently materialise directories with broad
// permissions (0o755 traversable) on operator-unexpected paths (M5).
// O_NOFOLLOW prevents the kernel from following symlinks at the final path
// component, closing the TOCTOU gap between Lstat and OpenFile.
func openFile(location string) (io.WriteCloser, error) {
	path := strings.TrimPrefix(location, "file://")
	if path == "" {
		return nil, fmt.Errorf("file log target requires a path")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("file log target path must be absolute: %s", path)
	}

	dir := filepath.Dir(path)
	// Lstat, not Stat, so a symlinked parent is rejected rather than followed.
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("log directory %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("log directory %s is a symlink", dir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("log directory %s is not a directory", dir)
	}

	// O_NOFOLLOW causes the open to fail if the final path component is a
	// symlink, preventing TOCTOU attacks without a separate Lstat check.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", path, err)
	}
	return &fileWriter{f: f}, nil
}
