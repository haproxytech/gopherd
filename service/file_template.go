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
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// fileRe matches {{file "/abs/path"}} with optional trim/follow modifiers and
// a ":-default" fallback. The default cannot contain "}" (closes the "}}").
var fileRe = regexp.MustCompile(`\{\{\s*file\s+"([^"]+)"((?:\s+(?:trim|follow))*)(?::-([^}]*))?\s*\}\}`)

// maxFileSize caps a single {{file}} read so a huge file fails loudly instead
// of OOMing PID 1.
const maxFileSize = 1 << 20

// ExpandFileRefs replaces {{file "/path"}} placeholders with file contents.
// Modifiers: "trim" right-trims trailing whitespace; "follow" permits symlinks
// (K8s ..data/ secret volumes); ":-default" is a fallback for a missing file.
//
// Runs before env expansion so file contents with "{{...}}" are not re-expanded.
func ExpandFileRefs(s string) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	locs := fileRe.FindAllStringSubmatchIndex(s, -1)
	if locs == nil {
		return s, nil
	}
	var b strings.Builder
	prev := 0
	for _, loc := range locs {
		b.WriteString(s[prev:loc[0]])
		path := s[loc[2]:loc[3]]
		mods := s[loc[4]:loc[5]]
		doTrim := strings.Contains(mods, "trim")
		follow := strings.Contains(mods, "follow")
		hasDefault := loc[6] >= 0
		var defaultVal string
		if hasDefault {
			defaultVal = s[loc[6]:loc[7]]
		}
		val, err := readFileTemplate(path, doTrim, follow, hasDefault, defaultVal)
		if err != nil {
			return "", err
		}
		b.WriteString(val)
		prev = loc[1]
	}
	b.WriteString(s[prev:])
	return b.String(), nil
}

// openConfined opens clean for reading with symlink protection, returning the
// raw error so callers add their own context. As root the contents may flow into
// a child's argv/env, so a symlink must not redirect the read to a root-only
// file. Without follow, a leaf (syscall.ELOOP) or ancestor symlink is rejected.
// With follow, os.Root confines resolution to clean's directory so a swapped
// symlink can't escape (e.g. to /etc/shadow); it maps absolute targets into the
// root, so follow requires relative symlinks. O_NONBLOCK stops a FIFO from
// blocking before the not-a-regular-file check. Shared by {{file}} and dotenv.
func openConfined(clean string, follow bool) (*os.File, error) {
	if follow {
		root, err := os.OpenRoot(filepath.Dir(clean))
		if err != nil {
			return nil, err
		}
		defer root.Close()

		return root.OpenFile(filepath.Base(clean), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	}
	if err := checkAncestorsNotSymlinked(clean); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(clean, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), clean), nil
}

// openFileTemplate opens a {{file}} reference, translating a symlinked leaf into
// a hint to add the follow modifier.
func openFileTemplate(clean string, follow bool) (*os.File, error) {
	f, err := openConfined(clean, follow)
	if err == syscall.ELOOP {
		return nil, fmt.Errorf("is a symlink; refusing to open (add the follow modifier to permit)")
	}
	return f, err
}

func readFileTemplate(path string, doTrim, follow, hasDefault bool, defaultVal string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("{{file %q}}: path must be absolute", path)
	}
	f, err := openFileTemplate(filepath.Clean(path), follow)
	if err != nil {
		// ENOENT (incl. a dangling follow symlink) with a default falls back;
		// any other error is fatal even with a default (unreadable != missing).
		if os.IsNotExist(err) && hasDefault {
			return defaultVal, nil
		}
		return "", fmt.Errorf("{{file %q}}: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("{{file %q}}: stat: %w", path, err)
	}
	// Reject non-regular files: a FIFO blocks io.ReadAll, a device returns junk.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("{{file %q}}: not a regular file (mode %s)", path, info.Mode())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return "", fmt.Errorf("{{file %q}}: %w", path, err)
	}
	if int64(len(data)) > maxFileSize {
		return "", fmt.Errorf("{{file %q}}: exceeds %d-byte size cap", path, maxFileSize)
	}
	// Reject NUL early: it cannot survive argv/env, with a clearer error.
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("{{file %q}}: contains NUL byte", path)
	}
	val := string(data)
	if doTrim {
		val = strings.TrimRight(val, " \t\r\n")
	}
	return val, nil
}
