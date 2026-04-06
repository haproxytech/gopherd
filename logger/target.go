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
		lt.Writer.Close()
	}
}

// syslogWriter wraps syslog.Writer to implement io.WriteCloser with line-level writes.
type syslogWriter struct {
	w *syslog.Writer
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
	return len(p), sw.w.Info(sanitize(string(p)))
}

// sanitize strips control characters (except newline and tab) from log
// output before forwarding to syslog. This prevents services from injecting
// ANSI escape sequences or carriage returns into syslog entries.
// Fast path: if no control characters are present, returns the input
// without allocation.
func sanitize(s string) string {
	// Quick scan: most log lines are clean.
	needsSanitize := false
	for i := range len(s) {
		if s[i] < 0x20 && s[i] != '\n' && s[i] != '\t' {
			needsSanitize = true
			break
		}
	}
	if !needsSanitize {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

func (sw *syslogWriter) Close() error {
	return sw.w.Close()
}

// openFile opens a log file for append-only writing, creating parent
// directories and the file itself if they don't exist. The path must be
// absolute to prevent relative path confusion. O_NOFOLLOW prevents the
// kernel from following symlinks at the final path component, closing the
// TOCTOU gap between Lstat and OpenFile.
func openFile(location string) (io.WriteCloser, error) {
	path := strings.TrimPrefix(location, "file://")
	if path == "" {
		return nil, fmt.Errorf("file log target requires a path")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("file log target path must be absolute: %s", path)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}

	// O_NOFOLLOW causes the open to fail if the final path component is a
	// symlink, preventing TOCTOU attacks without a separate Lstat check.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", path, err)
	}
	return f, nil
}
