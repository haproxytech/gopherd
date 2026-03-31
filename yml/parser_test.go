package yml

import (
	"testing"
)

func TestParseScalar(t *testing.T) {
	n, err := Parse([]byte(`name: hello`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Get("name").String() != "hello" {
		t.Errorf("got %q", n.Get("name").String())
	}
}

func TestParseQuotedScalar(t *testing.T) {
	n, err := Parse([]byte(`name: "hello world"`))
	if err != nil {
		t.Fatal(err)
	}
	if n.Get("name").String() != "hello world" {
		t.Errorf("got %q", n.Get("name").String())
	}
}

func TestParseBool(t *testing.T) {
	n, err := Parse([]byte(`enabled: true`))
	if err != nil {
		t.Fatal(err)
	}
	if !n.Get("enabled").Bool() {
		t.Error("expected true")
	}
}

func TestParseInt(t *testing.T) {
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
	n, err := Parse([]byte("control:\n  socket: /run/test.sock\n"))
	if err != nil {
		t.Fatal(err)
	}
	if n.Get("control").Get("socket").String() != "/run/test.sock" {
		t.Errorf("got %q", n.Get("control").Get("socket").String())
	}
}

func TestParseSequenceOfMappings(t *testing.T) {
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
	_, err := Parse([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
}

func TestParseNilNode(t *testing.T) {
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

func TestParseIntPtr(t *testing.T) {
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
