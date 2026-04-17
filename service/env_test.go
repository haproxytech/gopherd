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

import "testing"

func TestExpandEnvRefs(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"SET":   "value",
		"EMPTY": "",
		"NAME":  "alice",
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no placeholders", "plain string", "plain string"},
		{"set var", "{{.SET}}", "value"},
		{"unset var no default", "{{.MISSING}}", ""},
		{"unset var with default", "{{.MISSING:-fallback}}", "fallback"},
		{"empty var with default", "{{.EMPTY:-fallback}}", "fallback"},
		{"set var with default unused", "{{.SET:-fallback}}", "value"},
		{"empty default", "{{.MISSING:-}}", ""},
		{"whitespace around name", "{{ .SET }}", "value"},
		{"whitespace before :-", "{{ .MISSING :-fallback}}", "fallback"},
		{"trailing whitespace in default preserved", "{{.MISSING:-foo }}", "foo "},
		{"multiple refs", "hi {{.NAME}}, {{.MISSING:-stranger}}", "hi alice, stranger"},
		{"bare braces not placeholder", "{{ not a ref }}", "{{ not a ref }}"},
		{"no closing braces left alone", "{{.SET", "{{.SET"},
		{"literal after default", "pre-{{.MISSING:-x}}-post", "pre-x-post"},
		{"default can contain colon and dash", "{{.MISSING:-a:-b}}", "a:-b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ExpandEnvRefs(tt.in, env)
			if got != tt.want {
				t.Errorf("ExpandEnvRefs(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExpandEnvRefsNilEnv(t *testing.T) {
	t.Parallel()

	// Nil env is legal — every ref is "unset".
	if got := ExpandEnvRefs("{{.X}}", nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := ExpandEnvRefs("{{.X:-def}}", nil); got != "def" {
		t.Errorf("got %q, want %q", got, "def")
	}
}

func BenchmarkExpandEnvRefsLiteral(b *testing.B) {
	// Common case: a literal startup value with no placeholders. Must stay
	// on the fast path (no regex, no allocation).
	env := map[string]string{"X": "value"}
	for b.Loop() {
		_ = ExpandEnvRefs("enabled", env)
	}
}

func BenchmarkExpandEnvRefsSetVar(b *testing.B) {
	env := map[string]string{"START_X": "oneshot"}
	for b.Loop() {
		_ = ExpandEnvRefs("{{.START_X}}", env)
	}
}

func BenchmarkExpandEnvRefsWithDefault(b *testing.B) {
	env := map[string]string{}
	for b.Loop() {
		_ = ExpandEnvRefs("{{.START_X:-enabled}}", env)
	}
}
