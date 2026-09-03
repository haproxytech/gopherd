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
	"strings"
	"testing"
)

func TestParseScalar(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`name: hello`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Get("name").String() != "hello" {
		t.Errorf("got %q", n.Get("name").String())
	}
}

func TestParseQuotedScalar(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`name: "hello world"`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Get("name").String() != "hello world" {
		t.Errorf("got %q", n.Get("name").String())
	}
}

func TestParseBool(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`enabled: true`))
	if err != nil {
		t.Fatal(err)
	}
	if !n.Get("enabled").Bool() {
		t.Error("expected true")
	}
}

func TestParseInt(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`port: 8080`))
	if err != nil {
		t.Fatal(err)
	}
	v, ok := n.Get("port").Int()
	if !ok || v != 8080 {
		t.Errorf("got %d, %v", v, ok)
	}
}

func TestParseFloat(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`factor: 2.5`))
	if err != nil {
		t.Fatal(err)
	}
	v, ok := n.Get("factor").Float()
	if !ok || v != 2.5 {
		t.Errorf("got %f, %v", v, ok)
	}
}

func TestParseInlineList(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`items: [a, b, c]`))
	if err != nil {
		t.Fatal(err)
	}
	items := n.Get("items").Strings()
	if len(items) != 3 || items[0] != "a" || items[2] != "c" {
		t.Errorf("got %v", items)
	}
}

func TestParseInlineListQuoted(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`args: ["--config", "/etc/app.conf"]`))
	if err != nil {
		t.Fatal(err)
	}
	args := n.Get("args").Strings()
	if len(args) != 2 || args[0] != "--config" || args[1] != "/etc/app.conf" {
		t.Errorf("got %v", args)
	}
}

func TestParseNestedMapping(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte("control:\n  socket: /run/test.sock\n"))
	if err != nil {
		t.Fatal(err)
	}
	if n.Get("control").Get("socket").String() != "/run/test.sock" {
		t.Errorf("got %q", n.Get("control").Get("socket").String())
	}
}

func TestParseSequenceOfMappings(t *testing.T) {
	t.Parallel()
	yml := `
processes:
  - name: app
    command: /bin/app
  - name: sidecar
    command: /bin/sidecar
`
	n, err := Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	items := n.Get("processes").Items()
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Get("name").String() != "app" {
		t.Errorf("got %q", items[0].Get("name").String())
	}
	if items[1].Get("command").String() != "/bin/sidecar" {
		t.Errorf("got %q", items[1].Get("command").String())
	}
}

// Entries and Items must return defensive copies: a caller mutating the
// returned slice must not corrupt the parser's internal node tree.
func TestEntriesItemsReturnCopies(t *testing.T) {
	t.Parallel()

	seq, err := Parse([]byte("items: [a, b, c]"))
	if err != nil {
		t.Fatal(err)
	}
	items := seq.Get("items").Items()
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	items[0] = nil // mutate the returned slice
	if again := seq.Get("items").Items(); again[0] == nil {
		t.Error("Items() exposed the internal slice; mutation leaked")
	}

	m, err := Parse([]byte("checks:\n  a:\n    type: tcp\n  b:\n    type: http\n"))
	if err != nil {
		t.Fatal(err)
	}
	entries := m.Get("checks").Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	entries[0] = MapEntry{} // mutate the returned slice
	if again := m.Get("checks").Entries(); again[0].Key == "" {
		t.Error("Entries() exposed the internal slice; mutation leaked")
	}
}

func TestParseMapOfMappings(t *testing.T) {
	t.Parallel()
	yml := `
checks:
  health:
    http:
      url: http://localhost/healthz
    period: 5s
  tcp-check:
    tcp:
      host: localhost
      port: 5432
`
	n, err := Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	entries := n.Get("checks").Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(entries))
	}
	health := n.Get("checks").Get("health")
	if health.Get("http").Get("url").String() != "http://localhost/healthz" {
		t.Errorf("got %q", health.Get("http").Get("url").String())
	}
	if health.Get("period").String() != "5s" {
		t.Errorf("got %q", health.Get("period").String())
	}
}

func TestParseComments(t *testing.T) {
	t.Parallel()
	yml := `
# top comment
name: app  # inline comment
# another comment
port: 8080
`
	n, err := Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if n.Get("name").String() != "app" {
		t.Errorf("got %q", n.Get("name").String())
	}
	v, _ := n.Get("port").Int()
	if v != 8080 {
		t.Errorf("got %d", v)
	}
}

func TestParseURLWithColon(t *testing.T) {
	t.Parallel()
	yml := `url: http://localhost:8080/health`
	n, err := Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	if n.Get("url").String() != "http://localhost:8080/health" {
		t.Errorf("got %q", n.Get("url").String())
	}
}

func TestParseStringMap(t *testing.T) {
	t.Parallel()
	yml := `
environment:
  FOO: bar
  DB_HOST: localhost
`
	n, err := Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	m := n.Get("environment").StringMap()
	if m["FOO"] != "bar" || m["DB_HOST"] != "localhost" {
		t.Errorf("got %v", m)
	}
}

func TestParseNestedInSequence(t *testing.T) {
	t.Parallel()
	yml := `
processes:
  - name: app
    environment:
      FOO: bar
    on-check-failure:
      health: restart
`
	n, err := Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	item := n.Get("processes").Items()[0]
	env := item.Get("environment").StringMap()
	if env["FOO"] != "bar" {
		t.Errorf("env = %v", env)
	}
	ocf := item.Get("on-check-failure").StringMap()
	if ocf["health"] != "restart" {
		t.Errorf("on-check-failure = %v", ocf)
	}
}

func TestParseEmpty(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseNilNode(t *testing.T) {
	t.Parallel()
	var n *Node
	if n.Get("x") != nil {
		t.Error("expected nil")
	}
	if n.String() != "" {
		t.Error("expected empty")
	}
	if n.Bool() {
		t.Error("expected false")
	}
	if n.Strings() != nil {
		t.Error("expected nil")
	}
	if n.StringMap() != nil {
		t.Error("expected nil")
	}
	if n.Entries() != nil {
		t.Error("expected nil")
	}
	if n.Items() != nil {
		t.Error("expected nil")
	}
	if n.IntPtr() != nil {
		t.Error("expected nil")
	}
}

// TestParseBoolYes verifies that Bool() recognises "yes" as true, not only "true".
func TestParseBoolYes(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`enabled: yes`))
	if err != nil {
		t.Fatal(err)
	}
	if !n.Get("enabled").Bool() {
		t.Error("expected Bool() == true for 'yes'")
	}
}

// TestHashInValueWithoutSpace verifies that a '#' not preceded by a space must
// not be treated as an inline comment (e.g. URL fragments, colour codes).
func TestHashInValueWithoutSpace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		yaml string
		key  string
		want string
	}{
		{`color: "#ff0000"`, "color", "#ff0000"},   // double-quoted → no stripping at all
		{`tag: '#important'`, "tag", "#important"}, // single-quoted
		{`label: foo#bar`, "label", "foo#bar"},     // unquoted, no preceding space
	}
	for _, tt := range tests {
		n, err := Parse([]byte(tt.yaml))
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.yaml, err)
		}
		if got := n.Get(tt.key).String(); got != tt.want {
			t.Errorf("Parse(%q).Get(%q) = %q, want %q", tt.yaml, tt.key, got, tt.want)
		}
	}
}

// TestUnquoteSingleChar verifies that unquote does not strip a single-character
// scalar whose sole byte happens to equal a quote character.
func TestUnquoteSingleChar(t *testing.T) {
	t.Parallel()
	// A value of a lone single-quote character (len=1 after colon).
	n, err := Parse([]byte("sep: '"))
	if err != nil {
		t.Fatal(err)
	}
	got := n.Get("sep").String()
	// Must preserve the raw character, not return empty string.
	if got == "" {
		t.Error("single-char value was incorrectly stripped to empty string")
	}
}

func TestParseIntPtr(t *testing.T) {
	t.Parallel()
	n, _ := Parse([]byte(`uid: 1000`))
	p := n.Get("uid").IntPtr()
	if p == nil || *p != 1000 {
		t.Errorf("got %v", p)
	}
	// Missing key
	p = n.Get("missing").IntPtr()
	if p != nil {
		t.Error("expected nil for missing")
	}
}

func TestParseBlockSequenceOfScalars(t *testing.T) {
	t.Parallel()
	yml := `
args:
  - -i
  - "-p"
  - '8080'
  - --writable
`
	n, err := Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	args := n.Get("args").Strings()
	want := []string{"-i", "-p", "8080", "--writable"}
	if len(args) != len(want) {
		t.Fatalf("got %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want[i])
		}
	}
}

// Quoted items containing ': ' and commas (e.g. JSON blobs) must stay one
// scalar, not misparse as a mapping.
func TestParseBlockSequenceQuotedJSON(t *testing.T) {
	t.Parallel()
	yml := `
args:
  - -t
  - 'theme={"foreground": "#d0d0d0", "background": "#1c1c1c"}'
`
	n, err := Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	args := n.Get("args").Strings()
	if len(args) != 2 {
		t.Fatalf("got %v", args)
	}
	if args[1] != `theme={"foreground": "#d0d0d0", "background": "#1c1c1c"}` {
		t.Errorf("got %q", args[1])
	}
}

// A colon'd unquoted item is still a mapping — existing sequence-of-mappings
// behavior must not regress.
func TestParseBlockSequenceMixedScalarAndMapping(t *testing.T) {
	t.Parallel()
	yml := `
items:
  - plain
  - name: app
    port: 80
`
	n, err := Parse([]byte(yml))
	if err != nil {
		t.Fatal(err)
	}
	items := n.Get("items").Items()
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].String() != "plain" {
		t.Errorf("items[0] = %q", items[0].String())
	}
	if items[1].Get("name").String() != "app" {
		t.Errorf("items[1].name = %q", items[1].Get("name").String())
	}
}

// URL-ish items have a colon not followed by a space: scalar, not mapping.
func TestParseBlockSequenceURLItem(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte("urls:\n  - http://localhost:8080/health\n"))
	if err != nil {
		t.Fatal(err)
	}
	urls := n.Get("urls").Strings()
	if len(urls) != 1 || urls[0] != "http://localhost:8080/health" {
		t.Errorf("got %v", urls)
	}
}

// TestParseRejectsDeepNesting verifies that pathologically nested structures
// are rejected before recursion can exhaust the PID 1 stack. Without a depth
// cap, a config nested thousands of levels would push a parseBlock frame per
// level and eventually crash the daemon.
func TestParseRejectsDeepNesting(t *testing.T) {
	t.Parallel()
	const depth = 1000 // well beyond maxParseDepth
	var b strings.Builder
	for i := range depth {
		b.WriteString(strings.Repeat("  ", i))
		b.WriteString("a:\n")
	}
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteString("leaf: 1\n")
	_, err := Parse([]byte(b.String()))
	if err == nil {
		t.Fatal("expected error for deeply nested YAML, got nil")
	}
	if !strings.Contains(err.Error(), "nesting exceeds maximum depth") {
		t.Errorf("error %q does not mention nesting depth limit", err.Error())
	}
}

func TestParseInlineMap(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`exit-code-map: {SIGKILL: 0, SIGTERM: 0}`))
	if err != nil {
		t.Fatal(err)
	}
	got := n.Get("exit-code-map").StringMap()
	if got["SIGKILL"] != "0" || got["SIGTERM"] != "0" || len(got) != 2 {
		t.Errorf("StringMap() = %v", got)
	}
}

func TestParseInlineMapEmpty(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`env: {}`))
	if err != nil {
		t.Fatal(err)
	}
	got := n.Get("env").StringMap()
	if got == nil || len(got) != 0 {
		t.Errorf("StringMap() = %v, want empty non-nil map", got)
	}
}

// Values may contain commas and colons when quoted; splitting must respect
// quotes so a URL or comma-bearing value stays one entry.
func TestParseInlineMapQuotedValue(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`env: {LIST: "a, b", URL: "http://h:80"}`))
	if err != nil {
		t.Fatal(err)
	}
	got := n.Get("env").StringMap()
	if got["LIST"] != "a, b" {
		t.Errorf("LIST = %q, want %q", got["LIST"], "a, b")
	}
	if got["URL"] != "http://h:80" {
		t.Errorf("URL = %q", got["URL"])
	}
}

// A nested inline map must not be split at the commas inside its braces.
func TestParseInlineMapNested(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`a: {b: {c: 1, d: 2}, e: 3}`))
	if err != nil {
		t.Fatal(err)
	}
	inner := n.Get("a").Get("b").StringMap()
	if inner["c"] != "1" || inner["d"] != "2" || len(inner) != 2 {
		t.Errorf("inner = %v", inner)
	}
	if n.Get("a").Get("e").String() != "3" {
		t.Errorf("e = %q", n.Get("a").Get("e").String())
	}
}

// An inline list nested in an inline map must survive comma splitting too.
func TestParseInlineMapWithInlineList(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte(`a: {args: [x, y], n: 1}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Get("a").Get("args").Strings(); len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("args = %v", got)
	}
	if n.Get("a").Get("n").String() != "1" {
		t.Errorf("n = %q", n.Get("a").Get("n").String())
	}
}

func TestParseInlineMapMissingColon(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte(`a: {foo, bar}`))
	if err == nil {
		t.Fatal("expected error for inline map entry without a colon")
	}
	if !strings.Contains(err.Error(), "key: value") {
		t.Errorf("error %q does not explain the expected form", err.Error())
	}
}

func TestParseInlineMapDuplicateKey(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte(`a: {x: 1, x: 2}`))
	if err == nil {
		t.Fatal("expected error for duplicate key in inline map")
	}
	if !strings.Contains(err.Error(), "duplicate key") {
		t.Errorf("error %q does not mention a duplicate key", err.Error())
	}
}

// A PID 1 process must not blow its stack on an adversarial config, so
// inline-map recursion is capped like block nesting is.
func TestParseInlineMapRejectsDeepNesting(t *testing.T) {
	t.Parallel()
	const depth = 500 // well beyond maxParseDepth
	payload := "a: " + strings.Repeat("{b: ", depth) + "1" + strings.Repeat("}", depth)
	_, err := Parse([]byte(payload))
	if err == nil {
		t.Fatal("expected error for deeply nested inline map, got nil")
	}
	if !strings.Contains(err.Error(), "nesting exceeds maximum depth") {
		t.Errorf("error %q does not mention nesting depth limit", err.Error())
	}
}

// A sequence item that is itself an inline map must parse as a mapping, not
// be mangled by the block-mapping path.
func TestParseInlineMapAsSequenceItem(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte("list:\n  - {name: a, cmd: x}\n  - {name: b, cmd: y}\n"))
	if err != nil {
		t.Fatal(err)
	}
	items := n.Get("list").Items()
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].Get("name").String() != "a" || items[0].Get("cmd").String() != "x" {
		t.Errorf("item 0 = %v", items[0].StringMap())
	}
	if items[1].Get("name").String() != "b" {
		t.Errorf("item 1 = %v", items[1].StringMap())
	}
}

func TestParseLiteralBlockScalar(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"cmd: |",
		"  echo hello",
		"  echo world",
		"next: 1",
	}, "\n")
	n, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Get("cmd").String(); got != "echo hello\necho world\n" {
		t.Errorf("cmd = %q", got)
	}
	if v, ok := n.Get("next").Int(); !ok || v != 1 {
		t.Errorf("next = %d, %v; sibling after block scalar lost", v, ok)
	}
}

func TestParseLiteralBlockScalarStrip(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte("cmd: |-\n  echo hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Get("cmd").String(); got != "echo hello" {
		t.Errorf("cmd = %q", got)
	}
}

func TestParseLiteralBlockScalarKeep(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"cmd: |+",
		"  echo hello",
		"",
		"next: 1",
	}, "\n")
	n, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Get("cmd").String(); got != "echo hello\n\n" {
		t.Errorf("cmd = %q", got)
	}
	if v, ok := n.Get("next").Int(); !ok || v != 1 {
		t.Errorf("next = %d, %v", v, ok)
	}
}

func TestParseLiteralBlockScalarInteriorBlankLine(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"script: |",
		"  line one",
		"",
		"  line three",
	}, "\n")
	n, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Get("script").String(); got != "line one\n\nline three\n" {
		t.Errorf("script = %q", got)
	}
}

func TestParseLiteralBlockScalarExtraIndent(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"script: |",
		"  if true; then",
		"    echo indented",
		"  fi",
	}, "\n")
	n, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Get("script").String(); got != "if true; then\n  echo indented\nfi\n" {
		t.Errorf("script = %q", got)
	}
}

func TestParseLiteralBlockScalarHashNotComment(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"script: |",
		"  #!/bin/sh",
		"  echo x # not stripped",
	}, "\n")
	n, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Get("script").String(); got != "#!/bin/sh\necho x # not stripped\n" {
		t.Errorf("script = %q", got)
	}
}

func TestParseLiteralBlockScalarCommentAfterIndicator(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte("cmd: | # trailing comment\n  echo hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Get("cmd").String(); got != "echo hello\n" {
		t.Errorf("cmd = %q", got)
	}
}

func TestParseLiteralBlockScalarInSequenceMapping(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"processes:",
		"  - name: app",
		"    script: |",
		"      echo one",
		"      echo two",
		"    on-failure: shutdown",
		"  - name: other",
	}, "\n")
	n, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	items := n.Get("processes").Items()
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if got := items[0].Get("script").String(); got != "echo one\necho two\n" {
		t.Errorf("script = %q", got)
	}
	if got := items[0].Get("on-failure").String(); got != "shutdown" {
		t.Errorf("on-failure = %q; sibling after block scalar lost", got)
	}
	if got := items[1].Get("name").String(); got != "other" {
		t.Errorf("second item name = %q", got)
	}
}

func TestParseLiteralBlockScalarSequenceItem(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"scripts:",
		"  - |",
		"    echo a",
		"  - |-",
		"    echo b",
	}, "\n")
	n, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	items := n.Get("scripts").Items()
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if got := items[0].String(); got != "echo a\n" {
		t.Errorf("item 0 = %q", got)
	}
	if got := items[1].String(); got != "echo b" {
		t.Errorf("item 1 = %q", got)
	}
}

func TestParseLiteralBlockScalarNestedMapping(t *testing.T) {
	t.Parallel()
	src := strings.Join([]string{
		"outer:",
		"  inner: |",
		"    text",
		"  sibling: x",
	}, "\n")
	n, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Get("outer").Get("inner").String(); got != "text\n" {
		t.Errorf("inner = %q", got)
	}
	if got := n.Get("outer").Get("sibling").String(); got != "x" {
		t.Errorf("sibling = %q", got)
	}
}

func TestParseLiteralBlockScalarEmpty(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte("cmd: |\nnext: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Get("cmd").String(); got != "" {
		t.Errorf("cmd = %q, want empty", got)
	}
	if v, ok := n.Get("next").Int(); !ok || v != 1 {
		t.Errorf("next = %d, %v", v, ok)
	}
}

func TestParseLiteralBlockScalarAtEOF(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte("cmd: |\n  echo hello"))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Get("cmd").String(); got != "echo hello\n" {
		t.Errorf("cmd = %q", got)
	}
}

func TestParseQuotedPipeStaysScalar(t *testing.T) {
	t.Parallel()
	n, err := Parse([]byte("sep: \"|\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Get("sep").String(); got != "|" {
		t.Errorf("sep = %q", got)
	}
}

func TestParseFoldedBlockScalarRejected(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("cmd: >\n  echo hello\n"))
	if err == nil {
		t.Fatal("expected error for folded block scalar, got nil")
	}
	if !strings.Contains(err.Error(), "folded") || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error %q should mention folded scalars and line 1", err.Error())
	}
}

func TestParseUnknownBlockIndicatorRejected(t *testing.T) {
	t.Parallel()
	_, err := Parse([]byte("cmd: |2\n  echo hello\n"))
	if err == nil {
		t.Fatal("expected error for unsupported block scalar indicator, got nil")
	}
	if !strings.Contains(err.Error(), "|2") || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error %q should mention the indicator and line 1", err.Error())
	}
}

// TestParseRejectsDuplicateMappingKey pins that a repeated key is an error, not
// a silent pick. `Get` returns the first match, so a duplicated key would keep
// the *first* value — the opposite of every other YAML implementation, and of
// what an operator editing the file expects.
func TestParseRejectsDuplicateMappingKey(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		"prefix: a\nprefix: b\n",
		"processes:\n  - name: app\n    command: /bin/a\n    command: /bin/b\n",
		"control:\n  socket: /a\n  socket: /b\n",
	} {
		_, err := Parse([]byte(src))
		if err == nil {
			t.Errorf("duplicate key accepted in:\n%s", src)
			continue
		}
		if !strings.Contains(err.Error(), "duplicate key") {
			t.Errorf("error %q does not mention a duplicate key (source:\n%s)", err, src)
		}
	}
}

// TestParseRejectsTabIndentation pins the tab rejection. Indentation is counted
// in spaces, so a tab-indented line measures as indent 0 and attaches to the
// document root: a nested process key becomes a top-level key and is dropped,
// silently.
func TestParseRejectsTabIndentation(t *testing.T) {
	t.Parallel()
	for _, src := range []string{
		"processes:\n\t- name: app\n",
		"control:\n\tsocket: /run/x.sock\n",
	} {
		_, err := Parse([]byte(src))
		if err == nil {
			t.Errorf("tab indentation accepted in %q", src)
			continue
		}
		if !strings.Contains(err.Error(), "tab") {
			t.Errorf("error %q should name the tab character (source %q)", err, src)
		}
	}
}

// TestParseSiblingIndentMustMatchExactly pins that a block's keys are exactly
// those at its own indent. An over-indented key belongs to the previous key's
// block, not this one; tolerating a near-enough indent would let a two-space
// slip move a setting into a different mapping with no error.
//
// It also documents the consequence: an over-indented key under a scalar value
// is dropped silently, which is what the exact comparison produces.
func TestParseSiblingIndentMustMatchExactly(t *testing.T) {
	t.Parallel()
	// `socket` is indented two spaces deeper than its sibling `socket-mode`.
	src := "control:\n  socket-mode: \"0660\"\n    socket: /run/x.sock\n"
	n, err := Parse([]byte(src))
	if err != nil {
		return // rejecting it outright is also acceptable
	}
	control := n.Get("control")
	if got := control.Get("socket").String(); got != "" {
		t.Errorf("over-indented key became a sibling (control.socket = %q); only "+
			"keys at the block's own indent are its members", got)
	}
	var keys []string
	for _, e := range control.Entries() {
		keys = append(keys, e.Key)
	}
	if len(keys) != 1 || keys[0] != "socket-mode" {
		t.Errorf("control keys = %v, want exactly [socket-mode]", keys)
	}
}

// TestLiteralBlockChomping pins the exact bytes each block-scalar indicator
// produces: `|` clips to one trailing newline, `|-` strips all, `|+` keeps
// them. The payloads are embedded scripts and PEM blobs, which care about
// their final newline, so the assertions are byte-exact.
func TestLiteralBlockChomping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"clip", "script: |\n  one\n  two\n", "one\ntwo\n"},
		{"strip", "script: |-\n  one\n  two\n", "one\ntwo"},
		{"keep", "script: |+\n  one\n  two\n", "one\ntwo\n"},
		// A single line exercises the same three rules without the join.
		{"clip single", "script: |\n  only\n", "only\n"},
		{"strip single", "script: |-\n  only\n", "only"},
		// Blank lines inside the block are content, not separators to drop.
		{"blank line kept", "script: |\n  one\n\n  two\n", "one\n\ntwo\n"},
		// Indentation relative to the first content line must survive.
		{
			"relative indent", "script: |\n  outer\n    inner\n  outer2\n",
			"outer\n  inner\nouter2\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			n, err := Parse([]byte(tc.src))
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.src, err)
			}
			if got := n.Get("script").String(); got != tc.want {
				t.Errorf("Parse(%q) script = %q, want %q", tc.src, got, tc.want)
			}
		})
	}
}

// TestParseRejectsUnplaceableLine pins that no line is ever silently discarded.
//
// parseBlock reports how far it got so its caller can continue in the enclosing
// block. Two call sites have none — the top level and a list item's own block —
// so there a leftover line is one the parser could not place, and ignoring the
// position loses everything after it. That is how a two-space slip used to
// delete whole config sections without a word.
//
// Both paths are covered: the top-level one truncates the rest of the document,
// the list-item one the rest of that item.
func TestParseRejectsUnplaceableLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		// line number the error must identify
		line string
	}{
		{
			// Over-indented key under a scalar value: everything after it,
			// including whole top-level sections, used to vanish.
			name: "top level truncated by an over-indented key",
			src: "control:\n" +
				"  socket: /run/x.sock\n" +
				"    socket-mode: \"0660\"\n" +
				"log-targets:\n" +
				"  audit:\n" +
				"    type: file\n",
			line: "line 3",
		},
		{
			name: "over-indented top-level key",
			src:  "prefix: a\n  stray: b\n",
			line: "line 2",
		},
		{
			// Inside a list item: the rest of the item used to be dropped, so a
			// service silently lost its args, env, or user.
			name: "list item key over-indented under a scalar",
			src: "processes:\n" +
				"  - name: app\n" +
				"    command: sleep\n" +
				"      args: [\"300\"]\n",
			line: "line 4",
		},
		{
			name: "list item key under-indented",
			src: "processes:\n" +
				"  - name: app\n" +
				"   command: sleep\n",
			line: "line 3",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(tc.src))
			if err == nil {
				t.Fatalf("a line the parser cannot place was accepted, so the rest "+
					"of the input was discarded silently:\n%s", tc.src)
			}
			if !strings.Contains(err.Error(), tc.line) {
				t.Errorf("error %q should identify %s", err, tc.line)
			}
			if !strings.Contains(err.Error(), "indent") {
				t.Errorf("error %q should say the problem is indentation", err)
			}
		})
	}
}

// TestParseAcceptsLegitimateNesting is the counterweight to
// TestParseRejectsUnplaceableLine: the consumed-position check must not reject
// the shapes the format actually uses. Blocks under a valueless key, nested
// lists, block scalars and inline maps all leave the parser legitimately
// mid-input at some depth.
func TestParseAcceptsLegitimateNesting(t *testing.T) {
	t.Parallel()
	src := `prefix: "[%s] "

control:
  socket: /run/gopherd.sock
  socket-mode: "0660"

processes:
  - name: app
    command: /bin/app
    args: ["--flag", "value"]
    environment:
      FOO: bar
      BAZ: qux
    after: [db]
    exit-code-map: {SIGTERM: 0}
  - name: db
    command: /bin/db
    startup: oneshot

checks:
  http:
    http:
      url: http://localhost:8080/health
    period: 5s

log-targets:
  audit:
    type: file
    location: /var/log/audit.log
    services: [app]
    labels:
      env: prod
`
	n, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("legitimate nesting rejected: %v", err)
	}
	// Spot-check one value at each depth the document reaches.
	if got := n.Get("control").Get("socket-mode").String(); got != "0660" {
		t.Errorf("control.socket-mode = %q", got)
	}
	procs := n.Get("processes").Items()
	if len(procs) != 2 {
		t.Fatalf("parsed %d processes, want 2", len(procs))
	}
	if got := procs[0].Get("environment").Get("BAZ").String(); got != "qux" {
		t.Errorf("processes[0].environment.BAZ = %q", got)
	}
	if got := procs[1].Get("startup").String(); got != "oneshot" {
		t.Errorf("processes[1].startup = %q", got)
	}
	if got := n.Get("log-targets").Get("audit").Get("labels").Get("env").String(); got != "prod" {
		t.Errorf("log-targets.audit.labels.env = %q", got)
	}
}
