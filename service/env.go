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
	"slices"
	"strings"
)

// ExpandRefs replaces {{file "..."}} and {{.VAR}} placeholders in a single pass
// over s: both kinds are located in the original string and each replacement is
// copied to the output without being scanned again. Inlined file content stays
// literal data and an env value cannot introduce a {{file}} read. missing, when
// non-nil, is called for a bare {{.VAR}} that is unset. {{mem}} / {{cpu}} forms
// are left untouched.
func ExpandRefs(s string, env map[string]string, missing func(name string)) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	type ref struct {
		loc  []int
		file bool
	}
	var refs []ref
	for _, loc := range fileRe.FindAllStringSubmatchIndex(s, -1) {
		refs = append(refs, ref{loc: loc, file: true})
	}
	for _, loc := range templateRe.FindAllStringSubmatchIndex(s, -1) {
		refs = append(refs, ref{loc: loc})
	}
	if refs == nil {
		return s, nil
	}
	slices.SortFunc(refs, func(a, b ref) int { return a.loc[0] - b.loc[0] })
	var b strings.Builder
	prev := 0
	for _, r := range refs {
		if r.loc[0] < prev {
			continue // overlaps the previous placeholder
		}
		b.WriteString(s[prev:r.loc[0]])
		if r.file {
			v, err := fileRefValue(s, r.loc)
			if err != nil {
				return "", err
			}
			b.WriteString(v)
		} else {
			name := s[r.loc[2]:r.loc[3]]
			v, ok := env[name]
			if (!ok || v == "") && r.loc[4] >= 0 {
				v = s[r.loc[4]:r.loc[5]]
			} else if !ok && missing != nil {
				missing(name)
			}
			b.WriteString(v)
		}
		prev = r.loc[1]
	}
	b.WriteString(s[prev:])
	return b.String(), nil
}

// ExpandEnvRefs replaces {{.VAR}} and {{.VAR:-default}} references in s against
// the given env map. For an unset/empty var, the bare form expands to "" and the
// default form expands to the literal default. It expands {{.VAR}} only; code
// that also accepts {{file}} must use ExpandRefs so neither kind's output is
// re-scanned by the other.
//
// Unlike expandTemplates, it does NOT warn on missing variables. A bare {{.VAR}}
// silently expanding to "" is a footgun, so callers MUST handle the empty case
// explicitly rather than let "" flow through as config (yml.parseProcess remaps
// an empty Startup to "disabled").
//
// {{mem}} / {{cpu}} forms are left untouched for later expansion in Start().
func ExpandEnvRefs(s string, env map[string]string) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	locs := templateRe.FindAllStringSubmatchIndex(s, -1)
	if locs == nil {
		return s
	}
	var b strings.Builder
	prev := 0
	for _, loc := range locs {
		b.WriteString(s[prev:loc[0]])
		name := s[loc[2]:loc[3]]
		val, ok := env[name]
		if (!ok || val == "") && loc[4] >= 0 {
			val = s[loc[4]:loc[5]]
		}
		b.WriteString(val)
		prev = loc[1]
	}
	b.WriteString(s[prev:])
	return b.String()
}
