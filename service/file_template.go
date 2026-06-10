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

// fileRe matches {{file "/abs/path"}} with optional modifiers ("trim",
// "follow", in any order) and an optional ":-default" suffix.
// Submatches: (1) absolute path, (2) modifier words, (3) default text.
// The default may not contain "}" because the closing "}}" cannot appear inside.
var fileRe = regexp.MustCompile(`\{\{\s*file\s+"([^"]+)"((?:\s+(?:trim|follow))*)(?::-([^}]*))?\s*\}\}`)

// maxFileSize caps how many bytes ExpandFileRefs will pull from any single
// referenced file. Secrets and license keys are well under this; the cap
// exists to make {{file "/var/log/huge.log"}} fail loudly rather than OOM
// PID 1.
const maxFileSize = 1 << 20

// ExpandFileRefs replaces {{file "/path"}} placeholders in s with the file's
// contents. Optional modifiers: "trim" right-trims trailing whitespace/
// newline (common for Docker/K8s secret files); "follow" permits symlinks
// for this reference (K8s secret volumes deliver keys as symlinks into
// ..data/); ":-default" supplies a fallback when the file does not exist.
// A missing file with no default is a hard error. Read errors other than
// ENOENT (permission denied, dir-not-file, etc.) are hard errors even with
// a default — a present-but-unreadable file is an operator mistake, not a
// fallback case.
//
// Runs before env-var expansion so file contents containing "{{...}}" are not
// re-expanded.
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

// openFileTemplate opens clean for reading. Without follow it rejects
// symlinks (leaf and ancestors): running as root, the contents go into a
// child's argv/env, and a symlink could redirect the read to a root-only
// file. With follow the operator opts out of that hardening for this one
// reference (K8s secret volumes deliver keys as symlinks into ..data/).
func openFileTemplate(clean string, follow bool) (*os.File, error) {
	if follow {
		return os.Open(clean)
	}
	if err := checkAncestorsNotSymlinked(clean); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(clean, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if err == syscall.ELOOP {
			return nil, fmt.Errorf("is a symlink; refusing to open (add the follow modifier to permit)")
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), clean), nil
}

func readFileTemplate(path string, doTrim, follow, hasDefault bool, defaultVal string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("{{file %q}}: path must be absolute", path)
	}
	f, err := openFileTemplate(filepath.Clean(path), follow)
	if err != nil {
		// ENOENT (including a dangling symlink with follow) with a default
		// falls back to the literal default text. Any other open error
		// (EACCES, ELOOP, ...) is a hard error even with a default: a
		// present-but-unreadable file is an operator mistake, not a
		// fallback case.
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
	// A FIFO would block io.ReadAll forever; a device would return
	// unexpected bytes. Reject anything that is not a regular file.
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
	// NUL bytes cannot survive cmd.Env or argv; fail early with a clearer
	// message than the runtime's generic "invalid argument".
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("{{file %q}}: contains NUL byte", path)
	}
	val := string(data)
	if doTrim {
		val = strings.TrimRight(val, " \t\r\n")
	}
	return val, nil
}
