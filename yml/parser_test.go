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
