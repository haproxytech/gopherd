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
	"bytes"
	"strings"
	"testing"
)

// utf8BOM is the byte sequence Parse strips from the front of a document.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Parser-convergence oracle: a test-only encoder renders a parsed *Node back to
// YAML so the fuzzer can assert Parse(encode(Parse(x))) == Parse(x) (plus encode
// fixed-point). A divergence is a real parser non-idempotency bug. Keeping the
// encoder in test code preserves the production parser's minimal read-only API.
//
// The encoder is conservative: it returns ok=false for any tree it cannot
// guarantee round-trips (exotic key, empty mapping, non-mapping block item) so
// the fuzzer skips rather than blaming the parser for an encoder limitation.

// nodesEqual reports structural equality. Mappings are order-sensitive: the
// parser preserves insertion order in a slice, and so must the round-trip.
func nodesEqual(a, b *Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.kind != b.kind {
		return false
	}
	switch a.kind {
	case kindScalar:
		return a.scalar == b.scalar
	case kindMapping:
		if len(a.mapping) != len(b.mapping) {
			return false
		}
		for i := range a.mapping {
			if a.mapping[i].Key != b.mapping[i].Key || !nodesEqual(a.mapping[i].Val, b.mapping[i].Val) {
				return false
			}
		}
		return true
	case kindSequence:
		if len(a.sequence) != len(b.sequence) {
			return false
		}
		for i := range a.sequence {
			if !nodesEqual(a.sequence[i], b.sequence[i]) {
				return false
			}
		}
		return true
	}
	return false
}

// quoteScalar double-quotes s with escaping that exactly inverts the parser's
// unescapeDouble. Quoting always keeps it one line, dodges the "[...]" inline-
// list misdetection, and neutralizes commas/colons/'#' — so it round-trips.
func quoteScalar(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('"')
	return b.String()
}

// allScalar reports whether every item is a scalar — such sequences must render
// inline ([...]), since block sequences cannot hold scalars. Empty counts true.
func allScalar(n *Node) bool {
	for _, it := range n.sequence {
		if it.kind != kindScalar {
			return false
		}
	}
	return true
}

// safeKey reports whether `k: value` re-parses to one entry keyed exactly k.
// It asks the real parser, so keys with ": ", leading "- ", " #", or newlines
// are exactly identified and skipped — no guessing.
func safeKey(k string) bool {
	// A quote in the key desyncs stripInlineComment's quote-state on re-parse,
	// so a following quoted value's " #" can be misread. The sentinel probe
	// below can't see that value-dependent interaction, so reject quoted keys.
	if strings.ContainsAny(k, "\"'") {
		return false
	}
	n, err := Parse([]byte(k + `: "s3nt1nel"`))
	if err != nil || n == nil || n.kind != kindMapping || len(n.mapping) != 1 {
		return false
	}
	e := n.mapping[0]
	return e.Key == k && e.Val != nil && e.Val.kind == kindScalar && e.Val.scalar == "s3nt1nel"
}

// encodeTree renders a root node. The parser only ever produces a root that is
// a mapping, a block sequence of mappings, or the empty scalar.
func encodeTree(n *Node) (string, bool) {
	switch n.kind {
	case kindScalar:
		// Only the empty scalar is producible at the root (empty/comment-only
		// input); a non-empty top-level scalar cannot be parsed back.
		if n.scalar != "" {
			return "", false
		}
		return "", true
	case kindMapping:
		var b strings.Builder
		if !encodeMapping(&b, n, 0) {
			return "", false
		}
		return b.String(), true
	case kindSequence:
		if allScalar(n) {
			return "", false // a root scalar-list is not parseable
		}
		var b strings.Builder
		if !encodeBlockSeq(&b, n, 0) {
			return "", false
		}
		return b.String(), true
	}
	return "", false
}

// encodeMapping writes each entry as "<indent>key: value" lines.
func encodeMapping(b *strings.Builder, n *Node, indent int) bool {
	if len(n.mapping) == 0 {
		return false // not producible; skip defensively
	}
	pad := strings.Repeat(" ", indent)
	for _, e := range n.mapping {
		if !safeKey(e.Key) {
			return false
		}
		v := e.Val
		switch v.kind {
		case kindScalar:
			b.WriteString(pad + e.Key + ": " + quoteScalar(v.scalar) + "\n")
		case kindSequence:
			if allScalar(v) {
				b.WriteString(pad + e.Key + ": " + encodeInlineSeq(v) + "\n")
			} else {
				b.WriteString(pad + e.Key + ":\n")
				if !encodeBlockSeq(b, v, indent+2) {
					return false
				}
			}
		case kindMapping:
			b.WriteString(pad + e.Key + ":\n")
			if !encodeMapping(b, v, indent+2) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// encodeInlineSeq renders a scalar sequence as "[a, b, c]".
func encodeInlineSeq(n *Node) string {
	parts := make([]string, 0, len(n.sequence))
	for _, it := range n.sequence {
		parts = append(parts, quoteScalar(it.scalar))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// encodeBlockSeq renders a sequence of mappings as "- " items, with the dash at
// dashIndent and item content at dashIndent+2.
func encodeBlockSeq(b *strings.Builder, n *Node, dashIndent int) bool {
	if len(n.sequence) == 0 {
		return false // block sequences always have >=1 item; skip
	}
	dashPad := strings.Repeat(" ", dashIndent)
	for _, item := range n.sequence {
		if item.kind != kindMapping {
			return false // nested block sequences / scalar items: skip
		}
		var ib strings.Builder
		if !encodeMapping(&ib, item, dashIndent+2) {
			return false
		}
		lines := strings.Split(strings.TrimRight(ib.String(), "\n"), "\n")
		// Splice "- " into the first line in place of its indent.
		b.WriteString(dashPad + "- " + lines[0][dashIndent+2:] + "\n")
		for _, l := range lines[1:] {
			b.WriteString(l + "\n")
		}
	}
	return true
}

// TestEncodeRoundTrip validates the oracle itself on seeds + real configs:
// every accepted input must either decline (ok=false) or round-trip to an
// identical, fixed-point encoding. Guards against encoder-bug false positives.
func TestEncodeRoundTrip(t *testing.T) {
	t.Parallel()
	var inputs [][]byte
	inputs = append(inputs, parseSeeds...)
	inputs = append(inputs, exampleConfigs(t)...)

	for _, in := range inputs {
		n, err := Parse(in)
		if err != nil {
			continue
		}
		enc, ok := encodeTree(n)
		if !ok {
			continue
		}
		n2, err := Parse([]byte(enc))
		if err != nil {
			t.Fatalf("re-parse of encoded tree failed: %v\ninput=%q\nencoded=%q", err, in, enc)
		}
		if !nodesEqual(n, n2) {
			t.Fatalf("round-trip diverged\ninput=%q\nencoded=%q", in, enc)
		}
		enc2, ok2 := encodeTree(n2)
		if !ok2 || enc2 != enc {
			t.Fatalf("encode is not a fixed point\ninput=%q\nenc1=%q\nenc2=%q", in, enc, enc2)
		}
	}
}

// TestParseConvergenceProperties is a readable table-driven check of the
// determinism and CR/CRLF/BOM properties the fuzzer enforces.
func TestParseConvergenceProperties(t *testing.T) {
	t.Parallel()
	cases := []string{
		"name: hello",
		"parent:\n  child: 1\n  other: 2",
		"items:\n  - command: a\n  - command: b",
		"list: [a, b, c]",
		"a: 1\nb: 2\n",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			assertConvergence(t, []byte(in))
		})
	}
}

// assertConvergence runs the full convergence battery on one input: parse
// determinism, CRLF/BOM normalization equivalence, and encoder round-trip.
// Shared by the table test and the fuzzer.
func assertConvergence(t *testing.T, data []byte) {
	t.Helper()
	n, err := Parse(data)
	if err != nil {
		return
	}

	// Determinism: parsing the same bytes twice yields the same tree.
	if n2, err2 := Parse(data); err2 != nil || !nodesEqual(n, n2) {
		t.Fatalf("non-deterministic parse of %q", data)
	}

	// Normalization convergence: injecting CRLF or a BOM must not change the
	// tree. Only when the input has no CR already (else CRLF injection would
	// produce \r\r\n).
	if !strings.ContainsRune(string(data), '\r') {
		crlf := []byte(strings.ReplaceAll(string(data), "\n", "\r\n"))
		if nc, ec := Parse(crlf); ec != nil || !nodesEqual(n, nc) {
			t.Fatalf("CRLF variant diverged for %q (err=%v)", data, ec)
		}
	}
	// Skip when data already starts with a BOM: Parse strips exactly one, so a
	// second prepended BOM would (correctly) survive and change the tree.
	if !bytes.HasPrefix(data, utf8BOM) {
		bom := append(append([]byte{}, utf8BOM...), data...)
		if nb, eb := Parse(bom); eb != nil || !nodesEqual(n, nb) {
			t.Fatalf("BOM variant diverged for %q (err=%v)", data, eb)
		}
	}

	// Round-trip idempotency through the encoder (the real convergence test).
	enc, ok := encodeTree(n)
	if !ok {
		return
	}
	n3, err3 := Parse([]byte(enc))
	if err3 != nil {
		t.Fatalf("re-parse of encoded tree failed: %v\ninput=%q\nencoded=%q", err3, data, enc)
	}
	if !nodesEqual(n, n3) {
		t.Fatalf("round-trip diverged\ninput=%q\nencoded=%q", data, enc)
	}
	if enc2, ok2 := encodeTree(n3); !ok2 || enc2 != enc {
		t.Fatalf("encode is not a fixed point\ninput=%q", data)
	}
}
