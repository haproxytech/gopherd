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

package yml

import (
	"os"
	"path/filepath"
	"testing"
)

// addCorpusFromExamples seeds the fuzzer with the project's real config files.
// Real configs cover the YAML subset gopherd uses far better than synthetic seeds.
func addCorpusFromExamples(f *testing.F) {
	f.Helper()
	matches, err := filepath.Glob("../example/*.yml")
	if err != nil {
		return
	}
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f.Add(data)
	}
}

// FuzzParse fuzzes the low-level YAML parser. The contract: Parse must
// either return a non-nil Node or a non-nil error, and must never panic
// regardless of input — gopherd runs as PID 1 and a parser panic during
// hot-reload would take down the whole container.
func FuzzParse(f *testing.F) {
	seeds := [][]byte{
		nil,
		[]byte(""),
		[]byte("\n"),
		[]byte("\r\n"),
		[]byte("\r"),
		[]byte("\xef\xbb\xbfkey: value"), // UTF-8 BOM
		[]byte("key: value"),
		[]byte("key:"),
		[]byte("key: "),
		[]byte(":"),
		[]byte("  : value"),
		[]byte("\t: value"),
		[]byte("key: 'unterminated"),
		[]byte(`key: "unterminated`),
		[]byte(`key: "esc \" inside"`),
		[]byte("a: [1, 2, 3]"),
		[]byte("a: [\"x\", 'y', z]"),
		[]byte("a:\n  - 1\n  - 2"),
		[]byte("a:\n  b:\n    c: d"),
		[]byte("a: b\na: c"), // duplicate key
		[]byte("# only a comment"),
		[]byte("key: value # trailing"),
		[]byte("key: '#not a comment'"),
		[]byte("a:\n - x\n  - y"),         // inconsistent indent
		[]byte("a: b\n   c: d\n  e: f"),   // inconsistent sibling indent
		[]byte("url: http://host:8080/x"), // colon-not-followed-by-space
	}
	for _, s := range seeds {
		f.Add(s)
	}
	addCorpusFromExamples(f)

	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := Parse(data)
		if err == nil && n == nil {
			t.Fatalf("Parse returned nil node and nil error for %q", data)
		}
		if err != nil {
			return
		}
		// Exercise the accessor surface so allocator/indexing bugs in
		// Get/String/Strings/Entries surface alongside parser bugs.
		walk(n)
	})
}

// FuzzUnmarshal fuzzes the full config decoder (Parse + semantic validation).
// Same panic-freedom contract as FuzzParse.
func FuzzUnmarshal(f *testing.F) {
	addCorpusFromExamples(f)
	f.Add([]byte("prefix: '['"))
	f.Add([]byte("processes:\n  svc:\n    command: /bin/true"))
	f.Add([]byte("init-stop-signal: [SIGTERM, SIGKILL]")) // rejected
	f.Add([]byte("shutdown-order: bogus"))
	f.Add([]byte("processes:\n  svc:\n    command: /bin/true\n    depends-on: [missing]"))

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = Unmarshal(data)
	})
}

func walk(n *Node) {
	if n == nil {
		return
	}
	switch n.kind {
	case kindScalar:
		_ = n.String()
		_, _ = n.Int()
		_, _ = n.Float()
		_ = n.Bool()
	case kindMapping:
		for _, e := range n.Entries() {
			walk(e.Val)
		}
		_ = n.StringMap()
	case kindSequence:
		for _, it := range n.Items() {
			walk(it)
		}
		_ = n.Strings()
	}
}
