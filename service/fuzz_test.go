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
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// FuzzExpandEnvRefs feeds arbitrary template text through the env-var
// expander against a small fixed env. The contract is "always returns,
// never panics" — config-load runs this once per service field and a
// crash here means no children ever start.
func FuzzExpandEnvRefs(f *testing.F) {
	for _, s := range []string{
		"",
		"plain text",
		"{{.FOO}}",
		"{{.FOO:-default}}",
		"{{.MISSING}}",
		"{{.MISSING:-fallback}}",
		"{{.FOO}} and {{.BAR:-x}}",
		"{{.}}",
		"{{}}",
		"{{",
		"}}",
		"{{.FOO:-}}",
		"{{.FOO:-{{.BAR}}}}",
		"{{.NESTED:-a:b:c}}",
		"{{cpu 50%}}",              // non-env template must pass through untouched
		"{{file \"/etc/secret\"}}", // ditto
		"{{.A}}{{.B}}{{.C:-x}}{{.D}}",
		strings.Repeat("{{.X}}", 64),
	} {
		f.Add(s)
	}

	env := map[string]string{
		"FOO":   "fooval",
		"EMPTY": "",
		"WEIRD": "has space and = sign",
	}

	f.Fuzz(func(t *testing.T, s string) {
		_ = ExpandEnvRefs(s, env)
		// Idempotence on inputs with no template syntax: a string that
		// contains no "{{" must round-trip unchanged. Cheap regression
		// against an over-eager rewrite.
		if !strings.Contains(s, "{{") {
			if got := ExpandEnvRefs(s, env); got != s {
				t.Fatalf("non-template input mutated: %q -> %q", s, got)
			}
		}
	})
}

// FuzzExpandFileRefs fuzzes the {{file "..."}} expansion. The fuzzer-controlled
// string is rewritten so any "/sandbox/" prefix points into a temp dir that
// contains a handful of known fixture files — this lets the fuzzer exercise
// the present/missing/trim/default branches without letting it read
// arbitrary host paths.
func FuzzExpandFileRefs(f *testing.F) {
	for _, s := range []string{
		"",
		"no template",
		`{{file "/sandbox/secret"}}`,
		`{{file "/sandbox/secret" trim}}`,
		`{{file "/sandbox/missing":-fallback}}`,
		`{{file "/sandbox/missing" trim:-fallback}}`,
		`{{file "/sandbox/empty"}}`,
		`{{file "relative/path"}}`,     // must reject (not absolute)
		`{{file "/sandbox/secret":-}}`, // empty default
		`prefix {{file "/sandbox/secret"}} suffix`,
		`{{file ""}}`,
		`{{file "/sandbox/dir"}}`, // not a regular file
	} {
		f.Add(s)
	}

	dir := f.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte("topsecret\n"), 0o600); err != nil {
		f.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty"), nil, 0o600); err != nil {
		f.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir"), 0o700); err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(_ *testing.T, s string) {
		// Sandbox: any "/sandbox/" reference is redirected into the temp dir,
		// so the fuzzer can't coax the expander into reading host files.
		s = strings.ReplaceAll(s, "/sandbox/", dir+"/")
		_, _ = ExpandFileRefs(s)
	})
}

// FuzzParseSignal feeds arbitrary names to ParseSignal. Bug class of interest:
// a panicking input would crash config-load. Successful returns must be
// valid syscall.Signal values; the function should never return a (sig, nil)
// pair where sig is zero.
func FuzzParseSignal(f *testing.F) {
	for _, s := range []string{
		"",
		"SIGTERM",
		"TERM",
		"sigterm",
		"SIGKILL",
		"KILL",
		"SIGUSR1",
		"USR2",
		"SIGRTMIN",
		"SIGRTMIN+1",
		"15",
		"0",
		"-1",
		"INVALID",
		"SIG",
		"sig sig sig",
		strings.Repeat("X", 256),
	} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, name string) {
		sig, err := ParseSignal(name)
		if err == nil && sig == syscall.Signal(0) {
			t.Fatalf("ParseSignal(%q) returned signal 0 with no error", name)
		}
	})
}
