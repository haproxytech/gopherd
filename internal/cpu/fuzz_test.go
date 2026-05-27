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

package cpu

import (
	"testing"
)

// FuzzEval fuzzes the {{cpu ...}} expression parser. Contract: never panic;
// successful returns must be >= 1 (the implementation clamps to a minimum
// of one CPU).
func FuzzEval(f *testing.F) {
	for _, s := range []string{
		"",
		"50%",
		"100%",
		"50% - 1",
		"50%-1",
		"50 % - 1",
		"  50%  ",
		"0%",
		"101%",
		"-50%",
		"1e20%",
		"50% - 9999",
		"50% -",
		"NaN%",
		"50% - abc",
	} {
		f.Add(s, 8)
	}

	f.Fuzz(func(t *testing.T, expr string, totalCPUs int) {
		got, err := Eval(expr, totalCPUs)
		if err == nil && got < 1 {
			t.Fatalf("Eval(%q, %d) returned %d with no error; result must be >= 1",
				expr, totalCPUs, got)
		}
	})
}
