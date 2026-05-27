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

package memory

import (
	"testing"
)

// FuzzEval fuzzes the {{mem ...}} expression parser. Contract: never panic;
// successful returns must be strictly positive (the implementation rejects
// zero/negative results as errors).
func FuzzEval(f *testing.F) {
	for _, s := range []string{
		"",
		"66%",
		"100%",
		"100% - 200MB",
		"512MB",
		"1GB",
		"1GiB",
		"1024MiB",
		"0%",
		"101%",
		"-50%",
		"1e20%",
		"NaN%",
		"50% -",
		"50 %",
		"  66 %  ",
		"66%-200MB",
		"66%- 200MB",
		"66 % - 200 MB",
		"99999999999999999999GB",
	} {
		f.Add(s, int64(8192))
	}

	f.Fuzz(func(t *testing.T, expr string, totalMiB int64) {
		got, err := Eval(expr, totalMiB)
		if err == nil && got <= 0 {
			t.Fatalf("Eval(%q, %d) returned %d with no error; result must be > 0",
				expr, totalMiB, got)
		}
	})
}
