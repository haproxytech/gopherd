// Package yml provides a minimal YAML parser for gopherd configuration.
// It supports the subset of YAML used by gopherd: scalars, maps, lists of maps,
// inline lists, and nested indentation. No external dependencies.
package yml

import (
	"fmt"
	"strconv"
	"strings"
)

// Node represents a parsed YAML value.
type Node struct {
	scalar   string
	mapping  []MapEntry
	sequence []*Node
	kind     nodeKind
}

// MapEntry is a key-value pair in a mapping node.
type MapEntry struct {
	Val *Node
	Key string
}

type nodeKind int

const (
	kindScalar nodeKind = iota
	kindMapping
	kindSequence
)

// Parse parses YAML text into a node tree.
func Parse(data []byte) (*Node, error) {
	lines := splitLines(data)
	n, _, err := parseBlock(lines, 0, -1)
	if err != nil {
		return nil, err
	}
	return n, nil
}

type rawLine struct {
	text   string
	indent int
	num    int
}

func splitLines(data []byte) []rawLine {
	var lines []rawLine
	for i, raw := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimRight(raw, " \t\r")
		if trimmed == "" || strings.TrimSpace(trimmed) == "" {
			continue
		}
		content := strings.TrimSpace(trimmed)
		if strings.HasPrefix(content, "#") {
			continue
		}
		content = stripInlineComment(content)
		indent := len(trimmed) - len(strings.TrimLeft(trimmed, " "))
		lines = append(lines, rawLine{indent: indent, text: content, num: i + 1})
	}
	return lines
}

func stripInlineComment(s string) string {
	inSingle := false
	inDouble := false
	for i, c := range s {
		switch c {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && i > 0 && s[i-1] == ' ' {
				return strings.TrimRight(s[:i], " ")
			}
		}
	}
	return s
}

func parseBlock(lines []rawLine, pos, minIndent int) (*Node, int, error) {
	if pos >= len(lines) {
		return &Node{kind: kindScalar, scalar: ""}, pos, nil
	}

	line := lines[pos]
	if strings.HasPrefix(line.text, "- ") {
		return parseSequence(lines, pos, line.indent)
	}

	return parseMapping(lines, pos, minIndent)
}

func parseMapping(lines []rawLine, pos, minIndent int) (*Node, int, error) {
	m := &Node{kind: kindMapping}

	for pos < len(lines) {
		line := lines[pos]
		if minIndent >= 0 && line.indent <= minIndent {
			break
		}
		if len(m.mapping) == 0 {
			minIndent = line.indent - 1
		} else if line.indent < lines[pos-1].indent && line.indent <= minIndent {
			break
		}

		colonIdx := findColon(line.text)
		if colonIdx < 0 {
			return nil, pos, fmt.Errorf("line %d: expected 'key: value', got %q", line.num, line.text)
		}

		key := strings.TrimSpace(line.text[:colonIdx])
		rest := strings.TrimSpace(line.text[colonIdx+1:])

		if rest != "" {
			val := parseScalar(rest)
			m.mapping = append(m.mapping, MapEntry{Key: key, Val: val})
			pos++
		} else {
			pos++
			if pos >= len(lines) || lines[pos].indent <= line.indent {
				m.mapping = append(m.mapping, MapEntry{Key: key, Val: &Node{kind: kindScalar, scalar: ""}})
				continue
			}
			child, nextPos, err := parseBlock(lines, pos, line.indent)
			if err != nil {
				return nil, nextPos, err
			}
			m.mapping = append(m.mapping, MapEntry{Key: key, Val: child})
			pos = nextPos
		}
	}

	return m, pos, nil
}

func parseSequence(lines []rawLine, pos, seqIndent int) (*Node, int, error) {
	seq := &Node{kind: kindSequence}

	for pos < len(lines) {
		line := lines[pos]
		if line.indent != seqIndent || !strings.HasPrefix(line.text, "- ") {
			break
		}

		itemText := strings.TrimPrefix(line.text, "- ")
		itemIndent := seqIndent + 2

		itemLines := []rawLine{{indent: itemIndent, text: itemText, num: line.num}}
		pos++
		for pos < len(lines) {
			next := lines[pos]
			if next.indent <= seqIndent {
				break
			}
			itemLines = append(itemLines, next)
			pos++
		}

		item, _, err := parseBlock(itemLines, 0, itemIndent-1)
		if err != nil {
			return nil, pos, err
		}
		seq.sequence = append(seq.sequence, item)
	}

	return seq, pos, nil
}

func parseScalar(s string) *Node {
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := s[1 : len(s)-1]
		items := splitCSV(inner)
		seq := &Node{kind: kindSequence}
		for _, item := range items {
			item = strings.TrimSpace(item)
			item = unquote(item)
			seq.sequence = append(seq.sequence, &Node{kind: kindScalar, scalar: item})
		}
		return seq
	}
	return &Node{kind: kindScalar, scalar: unquote(strings.TrimSpace(s))}
}

func splitCSV(s string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)
	for i := range len(s) {
		c := s[i]
		if inQuote {
			current.WriteByte(c)
			if c == quoteChar {
				inQuote = false
			}
			continue
		}
		if c == '"' || c == '\'' {
			inQuote = true
			quoteChar = c
			current.WriteByte(c)
			continue
		}
		if c == ',' {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(c)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func findColon(s string) int {
	for i := range len(s) {
		if s[i] == ':' {
			if i+1 >= len(s) || s[i+1] == ' ' {
				return i
			}
		}
	}
	return -1
}

// Get returns a child node by key.
func (n *Node) Get(key string) *Node {
	if n == nil || n.kind != kindMapping {
		return nil
	}
	for _, e := range n.mapping {
		if e.Key == key {
			return e.Val
		}
	}
	return nil
}

// String returns the scalar value, or empty string if not a scalar.
func (n *Node) String() string {
	if n == nil || n.kind != kindScalar {
		return ""
	}
	return n.scalar
}

// Int returns the integer scalar value.
func (n *Node) Int() (int, bool) {
	s := n.String()
	if s == "" {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	return v, err == nil
}

// Float returns the float scalar value.
func (n *Node) Float() (float64, bool) {
	s := n.String()
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// Bool returns the boolean scalar value (true/yes).
func (n *Node) Bool() bool {
	s := strings.ToLower(n.String())
	return s == "true" || s == "yes"
}

// Strings returns a string slice from a sequence node.
func (n *Node) Strings() []string {
	if n == nil || n.kind != kindSequence {
		return nil
	}
	out := make([]string, 0, len(n.sequence))
	for _, item := range n.sequence {
		if item.kind == kindScalar {
			out = append(out, item.scalar)
		}
	}
	return out
}

// StringMap returns a map[string]string from a mapping node.
func (n *Node) StringMap() map[string]string {
	if n == nil || n.kind != kindMapping {
		return nil
	}
	m := make(map[string]string)
	for _, e := range n.mapping {
		if e.Val.kind == kindScalar {
			m[e.Key] = e.Val.scalar
		}
	}
	return m
}

// Entries returns the mapping entries (for iterating maps of objects).
func (n *Node) Entries() []MapEntry {
	if n == nil || n.kind != kindMapping {
		return nil
	}
	return n.mapping
}

// Items returns the sequence items (for iterating lists).
func (n *Node) Items() []*Node {
	if n == nil || n.kind != kindSequence {
		return nil
	}
	return n.sequence
}

// IntPtr returns a pointer to int, or nil if not present.
func (n *Node) IntPtr() *int {
	v, ok := n.Int()
	if !ok {
		return nil
	}
	return &v
}
