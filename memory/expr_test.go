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

import "testing"

func TestEval(t *testing.T) {
	t.Parallel()
	tests := []struct {
		expr     string
		totalMiB int64
		want     int64
		wantErr  bool
	}{
		// Percentages.
		{"66%", 3000, 1980, false},
		{"33%", 3000, 990, false},
		{"100%", 1024, 1024, false},
		{"50%", 1024, 512, false},

		// Percentage with subtraction.
		// Note: MB/GB use SI units (1 MB = 1,000,000 bytes); conversion to MiB
		// uses math.Round, so 200 MB = 191 MiB and 1 GB = 954 MiB.
		{"100% - 200MB", 3000, 2809, false},
		{"100% - 200MiB", 3000, 2800, false},
		{"100% - 1GB", 3000, 2046, false},
		{"100% - 1GiB", 3000, 1976, false},

		// Absolute values.
		{"512MB", 3000, 488, false},
		{"512MiB", 3000, 512, false},
		{"1GB", 3000, 954, false},
		{"2GiB", 3000, 2048, false},

		// Whitespace tolerance.
		{" 66% ", 3000, 1980, false},
		{"100%  -  200MiB", 3000, 2800, false},
		{" 512MiB ", 3000, 512, false},

		// Errors.
		{"", 3000, 0, true},
		{"abc", 3000, 0, true},
		{"0%", 3000, 0, true},             // zero result
		{"100% - 4000MiB", 3000, 0, true}, // negative result
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			t.Parallel()
			got, err := Eval(tt.expr, tt.totalMiB)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Eval(%q, %d) = %d, want error", tt.expr, tt.totalMiB, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Eval(%q, %d) error: %v", tt.expr, tt.totalMiB, err)
			}
			if got != tt.want {
				t.Errorf("Eval(%q, %d) = %d, want %d", tt.expr, tt.totalMiB, got, tt.want)
			}
		})
	}
}
