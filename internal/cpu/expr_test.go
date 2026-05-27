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

import "testing"

func TestEval(t *testing.T) {
	t.Parallel()
	tests := []struct {
		expr    string
		total   int
		want    int
		wantErr bool
	}{
		// Empty → totalCPUs.
		{"", 8, 8, false},
		{"  ", 4, 4, false},

		// Percentages (rounded up).
		{"50%", 8, 4, false},
		{"50%", 1, 1, false},
		{"33%", 8, 3, false}, // ceil(2.64) = 3
		{"100%", 4, 4, false},
		{"25%", 1, 1, false}, // ceil(0.25) = 1

		// Percentage with subtraction.
		{"100% - 1", 4, 3, false},
		{"100% - 1", 1, 1, false}, // ceil(1) - 1 = 0, clamped to 1
		{"50% - 1", 8, 3, false},  // ceil(4) - 1 = 3
		{"50% - 2", 4, 1, false},  // ceil(2) - 2 = 0, clamped to 1

		// Whitespace tolerance.
		{" 50% ", 8, 4, false},
		{"100%  -  1", 4, 3, false},

		// Errors.
		{"abc", 8, 0, true},
		{"50", 8, 0, true},   // missing %
		{"-50%", 8, 0, true}, // negative
		// Percentages must be in (0, 100]: zero is meaningless, above 100
		// is nonsensical, and huge values would otherwise overflow the
		// float→int conversion and rely on the clamp as a safety net.
		{"0%", 8, 0, true},
		{"101%", 8, 0, true},
		{"1e20%", 8, 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			t.Parallel()
			got, err := Eval(tt.expr, tt.total)
			if tt.wantErr {
				if err == nil {
					t.Errorf("Eval(%q, %d) = %d, want error", tt.expr, tt.total, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Eval(%q, %d) error: %v", tt.expr, tt.total, err)
			}
			if got != tt.want {
				t.Errorf("Eval(%q, %d) = %d, want %d", tt.expr, tt.total, got, tt.want)
			}
		})
	}
}
