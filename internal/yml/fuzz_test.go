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
	"strings"
	"testing"
)

// parseSeeds are YAML fragments hitting the parser's fragile paths: BOM, CR/
// CRLF, quote state machines, indentation math, duplicate keys, depth guard.
// Shared by the fuzzer and the convergence oracle (TestEncodeRoundTrip).
var parseSeeds = func() [][]byte {
	strs := []string{
		"",
		"\n",
		"\r\n",
		"\r",
		"   ",
		"\xEF\xBB\xBFname: bom", // UTF-8 BOM prefix
		"name: hello",
		"name: \"hello world\"",
		"name: 'single quoted'",
		"key:",
		"key: ",
		":",
		"  : value",
		"\t: value",
		"a: 1\r\nb: 2\r\n", // CRLF line endings
		"a: 1\rb: 2\r",     // bare-CR (classic Mac) line endings
		"list: [a, b, c]",
		"list: [\"a, b\", 'c,d', e]", // commas inside quotes must not split
		"value: http://host:8080/x",  // "://" must not match findColon
		"key: value # trailing comment",
		"# whole-line comment\nname: x",
		"key: '#not a comment'",
		"key: 'unterminated",
		"key: \"unterminated",
		"esc: \"a\\nb\\t\\\"c\\\\d\"",  // double-quote escape sequences
		"trailingbackslash: \"abc\\\"", // backslash at end of quoted string
		"parent:\n  child: 1\n  other: 2",
		"items:\n  - command: a\n  - command: b",
		"a:\n  - 1\n  - 2",
		"a:\n  b:\n    c: d",
		"a: b\na: c",            // duplicate key
		"nokey",                 // mapping line with no colon
		"a:\n - x\n  - y",       // inconsistent indent
		"a: b\n   c: d\n  e: f", // inconsistent sibling indent
		"deep:\n  - a:\n      - b:\n          - c: 1",
		"s: |\n  line1\n  line2\nnext: 1",  // literal block scalar (clip)
		"s: |-\n  x\n\n\ns: |+\n  y\n\n\n", // strip/keep chomping, trailing blanks
		"s: |\nnext: 1",                    // empty block scalar
		"s: |2\n  x",                       // unsupported indentation indicator
		"s: >\n  folded",                   // folded scalar (rejected)
		"l:\n  - |\n    item\n  - name: a\n    s: |\n      body\n    k: v",
		"processes:\n  - command: /bin/true\n    args: [--flag, value]\n    stop-signal: SIGTERM",
	}
	seeds := make([][]byte, 0, len(strs)+2)
	for _, s := range strs {
		seeds = append(seeds, []byte(s))
	}
	// Near the maxParseDepth (64) guard: a chain just under and just over the
	// limit, so the depth-exceeded branch is actually exercised (mutation from
	// shallow seeds rarely synthesizes 60+ levels on its own).
	seeds = append(seeds, []byte(nestedMapping(60)), []byte(nestedMapping(70)))
	return seeds
}()

// nestedMapping builds `k:` nested depth levels deep with a scalar leaf.
func nestedMapping(depth int) string {
	var b strings.Builder
	for i := range depth {
		b.WriteString(strings.Repeat(" ", i*2) + "k:\n")
	}
	b.WriteString(strings.Repeat(" ", depth*2) + "v: 1")
	return b.String()
}

// exampleConfigs loads the repo's real config files as seeds (better coverage
// than synthetic ones). go test's CWD is this package, so ../../documentation
// hits the repo-root dir. Empty result fails: a stale glob after a docs move
// would otherwise silently drop all real-config seeds.
func exampleConfigs(tb testing.TB) [][]byte {
	tb.Helper()
	matches, err := filepath.Glob("../../documentation/*/example.yml")
	if err != nil {
		return nil
	}
	var out [][]byte
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		out = append(out, data)
	}
	if len(out) == 0 {
		tb.Fatalf("no example configs matched ../../documentation/*/example.yml")
	}
	return out
}

// walk invokes every accessor on every node to surface panics the parser
// plants in the tree — e.g. a node whose kind and contents disagree.
func walk(n *Node) {
	if n == nil {
		return
	}
	_ = n.String()
	_, _ = n.Int()
	_, _ = n.Float()
	_ = n.Bool()
	_ = n.BoolPtr()
	_ = n.IntPtr()
	_ = n.Strings()
	_ = n.StringMap()
	_ = n.Get("anything")
	for _, e := range n.Entries() {
		walk(e.Val)
	}
	for _, item := range n.Items() {
		walk(item)
	}
}

// FuzzParse asserts the parser's invariants: never panic (a panic is a PID 1
// crash), no node returned alongside an error, every returned tree is safe to
// traverse, and parsing converges (deterministic, CR/CRLF/BOM-invariant, and
// round-trip idempotent via assertConvergence).
func FuzzParse(f *testing.F) {
	for _, s := range parseSeeds {
		f.Add(s)
	}
	for _, data := range exampleConfigs(f) {
		f.Add(data)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		n, err := Parse(data)
		if err != nil {
			if n != nil {
				t.Errorf("Parse returned both a node and an error: %v", err)
			}
			return
		}
		if n == nil {
			t.Fatalf("Parse returned nil node and nil error for %q", data)
		}
		walk(n)
		assertConvergence(t, data)
	})
}

// FuzzUnmarshal fuzzes the full config decoder (Parse + validation). It touches
// no filesystem or network, so the only outcomes are a *Config or an error —
// never a panic.
func FuzzUnmarshal(f *testing.F) {
	for _, s := range parseSeeds {
		f.Add(s)
	}
	for _, data := range exampleConfigs(f) {
		f.Add(data)
	}
	f.Add([]byte("prefix: '['"))
	f.Add([]byte("init-stop-signal: [SIGTERM, SIGKILL]"))
	f.Add([]byte("processes:\n  - command: /bin/true\n    condition-file-exists: /e\n    condition-file-missing: relative"))
	f.Add([]byte("shutdown-order: bogus"))
	f.Add([]byte("processes:\n  svc:\n    command: /bin/true\n    depends-on: [missing]"))
	f.Add([]byte(strings.Join([]string{
		"prefix: app",
		"shutdown-order: reverse-dep",
		"init-stop-signal: [SIGTERM, SIGINT]",
		"control:",
		"  socket: /tmp/x.sock",
		"  socket-mode: \"0660\"",
		"processes:",
		"  - command: /bin/true",
		"    stop-signal: SIGTERM",
		"    backoff-factor: 1.5",
		"    exit-code-map:",
		"      SIGTERM: 0",
		"    signal-rewrite:",
		"      SIGUSR1: SIGUSR2",
	}, "\n")))

	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := Unmarshal(data)
		if err != nil {
			return // malformed/invalid config is an expected outcome
		}
		if cfg == nil {
			t.Fatal("Unmarshal returned nil config and nil error")
		}
		// A loaded config must have >=1 process, each with a command.
		if len(cfg.Processes) == 0 {
			t.Error("Unmarshal succeeded but produced zero processes")
		}
		for i, p := range cfg.Processes {
			if p.Command == "" {
				t.Errorf("process %d has empty command after successful Unmarshal", i)
			}
		}
		// ShutdownSignals runs on every signal PID 1 receives; must not panic.
		_ = cfg.ShutdownSignals()
	})
}
