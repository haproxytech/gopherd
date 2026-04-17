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

import "strings"

// ExpandEnvRefs replaces {{.VAR}} and {{.VAR:-default}} references in s
// against the given env map. Used for config-time fields that do not flow
// through expandTemplates (currently: the Startup field). If the named env
// var is unset or empty, the form without a default expands to "" and the
// form with a default expands to the literal default text.
//
// Only env-var placeholders are recognized here; {{mem}} / {{cpu}} forms are
// left untouched so they can still be expanded later during Start().
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
