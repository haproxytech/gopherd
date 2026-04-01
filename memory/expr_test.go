package memory

import "testing"

func TestEval(t *testing.T) {
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
		{"100% - 200MB", 3000, 2810, false},
		{"100% - 200MiB", 3000, 2800, false},
		{"100% - 1GB", 3000, 2047, false},
		{"100% - 1GiB", 3000, 1976, false},

		// Absolute values.
		{"512MB", 3000, 488, false},
		{"512MiB", 3000, 512, false},
		{"1GB", 3000, 953, false},
		{"2GiB", 3000, 2048, false},

		// Whitespace tolerance.
		{" 66% ", 3000, 1980, false},
		{"100%  -  200MiB", 3000, 2800, false},
		{" 512MiB ", 3000, 512, false},

		// Errors.
		{"", 3000, 0, true},
		{"abc", 3000, 0, true},
		{"0%", 3000, 0, true},           // zero result
		{"100% - 4000MiB", 3000, 0, true}, // negative result
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
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
