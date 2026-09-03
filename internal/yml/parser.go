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

// Package yml provides a minimal YAML parser for gopherd configuration.
// It supports the subset of YAML used by gopherd: scalars, literal block
// scalars, maps, block lists of maps or scalars, inline lists, and nested
// indentation. No external dependencies.
package yml

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// maxParseDepth caps parseBlock recursion so a malformed or adversarial
// config cannot exhaust the PID 1 process stack via deep nesting.
const maxParseDepth = 64

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
	// Strip UTF-8 BOM (Windows editors) so the first key does not parse with
	// a \ufeff prefix.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	// Normalize bare-\r line endings (classic Mac) to \n; \r\n is handled by
	// the TrimRight in splitLines. Single pass to avoid the two reallocations
	// the previous approach did on CRLF files.
	if bytes.IndexByte(data, '\r') >= 0 {
		out := make([]byte, 0, len(data))
		for i := 0; i < len(data); i++ {
			if data[i] == '\r' {
				out = append(out, '\n')
				if i+1 < len(data) && data[i+1] == '\n' {
					i++ // \r\n pair
				}
				continue
			}
			out = append(out, data[i])
		}
		data = out
	}
	lines, err := splitLines(data)
	if err != nil {
		return nil, err
	}
	n, pos, err := parseBlock(lines, 0, -1, 0)
	if err != nil {
		return nil, err
	}
	// At the top level there is no enclosing block for parseBlock to stop for,
	// so a leftover line is one it could not place. Returning the node anyway
	// would discard the rest of the file, whole sections included, in silence.
	if pos < len(lines) {
		return nil, fmt.Errorf("line %d: unexpected indentation for %q; it is "+
			"indented too deeply to be a top-level key and does not belong to the "+
			"block above it", lines[pos].num, lines[pos].text)
	}
	return n, nil
}

type rawLine struct {
	literal *string
	text    string
	indent  int
	num     int
}

func splitLines(data []byte) ([]rawLine, error) {
	raw := strings.Split(string(data), "\n")
	// A trailing \n makes Split emit one final "" that is an EOF artifact,
	// not an empty line; drop it so block scalar chomping counts breaks right.
	if len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
	}
	var lines []rawLine
	for i := 0; i < len(raw); i++ {
		num := i + 1
		trimmed := strings.TrimRight(raw[i], " \t\r")
		if trimmed == "" || strings.TrimSpace(trimmed) == "" {
			continue
		}
		// Reject tab indentation: indent is counted in spaces only, so a
		// leading \t would parse as indent=0 and attach at the wrong depth.
		// YAML 1.2 forbids tabs for indentation anyway.
		if raw[i][0] == '\t' {
			return nil, fmt.Errorf("line %d: tab character used for indentation; YAML requires spaces", num)
		}
		content := strings.TrimSpace(trimmed)
		if strings.HasPrefix(content, "#") {
			continue
		}
		content = stripInlineComment(content)
		indent := len(trimmed) - len(strings.TrimLeft(trimmed, " "))
		line := rawLine{indent: indent, text: content, num: num}
		if ind, ok := blockIndicator(content); ok {
			literal, next, err := collectLiteralBlock(raw, i+1, indent, ind)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", num, err)
			}
			line.literal = &literal
			i = next - 1
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// blockIndicator extracts the value position of a line — after "key: ", after
// any "- " item markers, or the whole line — and reports whether it starts a
// block scalar ('|' or '>'). Quoted values never match: they cannot start
// with a bare indicator character.
func blockIndicator(content string) (string, bool) {
	s := content
	for strings.HasPrefix(s, "- ") {
		s = s[2:]
	}
	if idx := findColon(s); idx >= 0 {
		s = strings.TrimSpace(s[idx+1:])
	}
	if s == "" || (s[0] != '|' && s[0] != '>') {
		return "", false
	}
	return s, true
}

// collectLiteralBlock gathers the indented body of a literal block scalar and
// returns it with the index of the first line after the block. The indicator
// selects chomping: "|" clips to one trailing newline, "|-" strips all, "|+"
// keeps every break. Folded scalars (">") are rejected. Body lines are raw:
// '#' is content, blank lines are kept, and indentation past the first content
// line is preserved.
func collectLiteralBlock(raw []string, start, headerIndent int, indicator string) (string, int, error) {
	switch indicator {
	case "|", "|-", "|+":
	case ">", ">-", ">+":
		return "", 0, fmt.Errorf("folded block scalars (%q) are not supported; use | for a literal block scalar", indicator)
	default:
		return "", 0, fmt.Errorf("unsupported block scalar indicator %q (only |, |-, and |+ are supported)", indicator)
	}

	blockIndent := -1
	var body []string
	i := start
	for ; i < len(raw); i++ {
		line := raw[i]
		if strings.TrimSpace(line) == "" {
			body = append(body, "")
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if blockIndent < 0 {
			// First content line fixes the block's indentation.
			if indent <= headerIndent {
				break
			}
			blockIndent = indent
		} else if indent < blockIndent {
			break
		}
		body = append(body, line[blockIndent:])
	}

	text := strings.Join(body, "\n")
	switch indicator {
	case "|-":
		text = strings.TrimRight(text, "\n")
	case "|+":
		if len(body) > 0 {
			text += "\n"
		}
	default: // "|": clip to exactly one trailing newline
		text = strings.TrimRight(text, "\n")
		if text != "" {
			text += "\n"
		}
	}
	return text, i, nil
}

func stripInlineComment(s string) string {
	inSingle := false
	inDouble := false
	escaped := false
	for i, c := range s {
		if escaped {
			escaped = false
			continue
		}
		switch c {
		case '\\':
			// In double-quoted strings a backslash escapes the next char
			// (e.g. \" does not close); single-quoted treats it as literal.
			if inDouble {
				escaped = true
			}
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

func parseBlock(lines []rawLine, pos, minIndent, depth int) (*Node, int, error) {
	if depth > maxParseDepth {
		lineNum := 0
		if pos < len(lines) {
			lineNum = lines[pos].num
		}
		return nil, pos, fmt.Errorf("line %d: YAML nesting exceeds maximum depth of %d", lineNum, maxParseDepth)
	}
	if pos >= len(lines) {
		return &Node{kind: kindScalar, scalar: ""}, pos, nil
	}

	line := lines[pos]
	if strings.HasPrefix(line.text, "- ") {
		return parseSequence(lines, pos, line.indent, depth)
	}

	return parseMapping(lines, pos, minIndent, depth)
}

func parseMapping(lines []rawLine, pos, minIndent, depth int) (*Node, int, error) {
	m := &Node{kind: kindMapping}
	// Detect duplicate keys at the same level so an ambiguous config fails
	// loudly instead of silently keeping first-wins.
	seenKeys := make(map[string]int)
	baseIndent := -1 // indent of the first key; all siblings must match

	for pos < len(lines) {
		line := lines[pos]
		if minIndent >= 0 && line.indent <= minIndent {
			break
		}
		if baseIndent < 0 {
			// First key sets the required indent for all siblings.
			baseIndent = line.indent
			minIndent = line.indent - 1
		} else if line.indent != baseIndent {
			// Different indent: this line belongs to a parent or child block.
			break
		}

		colonIdx := findColon(line.text)
		if colonIdx < 0 {
			return nil, pos, fmt.Errorf("line %d: expected 'key: value', got %q", line.num, line.text)
		}

		key := strings.TrimSpace(line.text[:colonIdx])
		if prev, dup := seenKeys[key]; dup {
			return nil, pos, fmt.Errorf("line %d: duplicate key %q (previously defined on line %d)", line.num, key, prev)
		}
		seenKeys[key] = line.num
		rest := strings.TrimSpace(line.text[colonIdx+1:])

		if line.literal != nil {
			m.mapping = append(m.mapping, MapEntry{Key: key, Val: &Node{kind: kindScalar, scalar: *line.literal}})
			pos++
			continue
		}
		if rest != "" {
			val, err := parseScalar(rest, depth)
			if err != nil {
				return nil, pos, fmt.Errorf("line %d: %w", line.num, err)
			}
			m.mapping = append(m.mapping, MapEntry{Key: key, Val: val})
			pos++
		} else {
			pos++
			if pos >= len(lines) || lines[pos].indent <= line.indent {
				m.mapping = append(m.mapping, MapEntry{Key: key, Val: &Node{kind: kindScalar, scalar: ""}})
				continue
			}
			child, nextPos, err := parseBlock(lines, pos, line.indent, depth+1)
			if err != nil {
				return nil, nextPos, err
			}
			m.mapping = append(m.mapping, MapEntry{Key: key, Val: child})
			pos = nextPos
		}
	}

	return m, pos, nil
}

func parseSequence(lines []rawLine, pos, seqIndent, depth int) (*Node, int, error) {
	seq := &Node{kind: kindSequence}

	for pos < len(lines) {
		line := lines[pos]
		if line.indent != seqIndent || !strings.HasPrefix(line.text, "- ") {
			break
		}

		itemText := strings.TrimPrefix(line.text, "- ")
		itemIndent := seqIndent + 2

		if line.literal != nil && !strings.HasPrefix(itemText, "- ") && findColon(itemText) < 0 {
			// "- |": the item itself is a literal block scalar.
			seq.sequence = append(seq.sequence, &Node{kind: kindScalar, scalar: *line.literal})
			pos++
			continue
		}

		itemLines := []rawLine{{indent: itemIndent, text: itemText, num: line.num, literal: line.literal}}
		pos++
		for pos < len(lines) {
			next := lines[pos]
			if next.indent <= seqIndent {
				break
			}
			itemLines = append(itemLines, next)
			pos++
		}

		if len(itemLines) == 1 && isInlineSeqItem(itemText) {
			item, err := parseScalar(itemText, depth)
			if err != nil {
				return nil, pos, fmt.Errorf("line %d: %w", line.num, err)
			}
			seq.sequence = append(seq.sequence, item)
			continue
		}
		item, used, err := parseBlock(itemLines, 0, itemIndent-1, depth+1)
		if err != nil {
			return nil, pos, err
		}
		// itemLines is the item's complete block, so an unconsumed line has no
		// enclosing block to belong to: the same silent discard, one item down.
		if used < len(itemLines) {
			bad := itemLines[used]
			return nil, pos, fmt.Errorf("line %d: unexpected indentation for %q; it "+
				"does not line up with the other keys of the list item starting on "+
				"line %d", bad.num, bad.text, line.num)
		}
		seq.sequence = append(seq.sequence, item)
	}

	return seq, pos, nil
}

// isInlineSeqItem reports whether a single-line sequence item can be handled
// by parseScalar: not a nested sequence, and either an inline map, colon-free,
// or fully quoted (so quoted values containing ": ", e.g. JSON blobs, stay one
// scalar).
func isInlineSeqItem(s string) bool {
	if strings.HasPrefix(s, "- ") {
		return false
	}
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		return true
	}
	if findColon(s) < 0 {
		return true
	}
	return len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0]
}

// parseScalar parses an inline value: an inline list ("[a, b]"), an inline
// map ("{a: 1, b: 2}"), or a plain scalar. depth bounds brace recursion so a
// malformed config cannot exhaust the PID 1 stack.
func parseScalar(s string, depth int) (*Node, error) {
	if depth > maxParseDepth {
		return nil, fmt.Errorf("YAML nesting exceeds maximum depth of %d", maxParseDepth)
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		inner := s[1 : len(s)-1]
		items := splitCSV(inner)
		seq := &Node{kind: kindSequence}
		for _, item := range items {
			item = strings.TrimSpace(item)
			item = unquote(item)
			seq.sequence = append(seq.sequence, &Node{kind: kindScalar, scalar: item})
		}
		return seq, nil
	}
	if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
		return parseInlineMap(s[1:len(s)-1], depth)
	}
	return &Node{kind: kindScalar, scalar: unquote(strings.TrimSpace(s))}, nil
}

// parseInlineMap parses the body of a flow mapping ("a: 1, b: 2"). Entries are
// validated like block keys, so a malformed entry or a duplicate key fails at
// load instead of silently yielding an empty map.
func parseInlineMap(inner string, depth int) (*Node, error) {
	m := &Node{kind: kindMapping}
	seen := make(map[string]bool)
	for _, entry := range splitCSV(inner) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		colonIdx := findColon(entry)
		if colonIdx < 0 {
			return nil, fmt.Errorf("expected 'key: value' in inline map, got %q", entry)
		}
		key := unquote(strings.TrimSpace(entry[:colonIdx]))
		if seen[key] {
			return nil, fmt.Errorf("duplicate key %q in inline map", key)
		}
		seen[key] = true
		val, err := parseScalar(strings.TrimSpace(entry[colonIdx+1:]), depth+1)
		if err != nil {
			return nil, err
		}
		m.mapping = append(m.mapping, MapEntry{Key: key, Val: val})
	}
	return m, nil
}

func splitCSV(s string) []string {
	var parts []string
	var current strings.Builder
	inQuote := false
	quoteChar := byte(0)
	escaped := false
	depth := 0 // nesting of [] and {}; commas only split at depth 0
	for i := range len(s) {
		c := s[i]
		if escaped {
			current.WriteByte(c)
			escaped = false
			continue
		}
		if inQuote {
			if c == '\\' && quoteChar == '"' {
				// Backslash escapes only in double-quoted segments
				// (single-quoted treats \ as literal), per YAML.
				current.WriteByte(c)
				escaped = true
				continue
			}
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
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		}
		if c == ',' && depth == 0 {
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
	if len(s) < 2 {
		return s
	}
	if s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	if s[0] == '"' && s[len(s)-1] == '"' {
		return unescapeDouble(s[1 : len(s)-1])
	}
	return s
}

// unescapeDouble processes YAML double-quoted string escape sequences.
// Handles the most common sequences: \n, \t, \r, \\, \", \'.
func unescapeDouble(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		switch s[i+1] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		case '\'':
			b.WriteByte('\'')
		default:
			// Unknown escape: preserve both characters.
			b.WriteByte('\\')
			b.WriteByte(s[i+1])
		}
		i += 2
	}
	return b.String()
}

// findColon returns the index of the first ':' followed by a space or at
// end-of-string. It ignores quoting; the only risk is a key containing ": ",
// which is not a valid YAML identifier nor used by any gopherd key. URL-like
// values (e.g. "http://host:port") are safe since "://" lacks the trailing
// space.
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

// BoolPtr returns a pointer to the bool value, or nil if absent, so callers
// can distinguish "not set" from "set to false".
func (n *Node) BoolPtr() *bool {
	if n == nil || n.String() == "" {
		return nil
	}
	v := n.Bool()
	return &v
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

// Entries returns a defensive copy of the mapping entries so mutation cannot
// corrupt the node tree.
func (n *Node) Entries() []MapEntry {
	if n == nil || n.kind != kindMapping {
		return nil
	}
	return slices.Clone(n.mapping)
}

// Items returns a defensive copy of the sequence items so mutation cannot
// corrupt the node tree.
func (n *Node) Items() []*Node {
	if n == nil || n.kind != kindSequence {
		return nil
	}
	return slices.Clone(n.sequence)
}

// IntPtr returns a pointer to int, or nil if not present.
func (n *Node) IntPtr() *int {
	v, ok := n.Int()
	if !ok {
		return nil
	}
	return &v
}
