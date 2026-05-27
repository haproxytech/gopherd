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
	"fmt"
	"math"
	"regexp"
	"strconv"
)

// exprRe matches CPU expressions:
//
//	"50%"      → percentage of available CPUs
//	"50% - 1"  → percentage minus fixed count
var exprRe = regexp.MustCompile(
	`^\s*(\d+(?:\.\d+)?)\s*%\s*(?:-\s*(\d+)\s*)?\s*$`,
)

// Eval evaluates a CPU expression against the given total CPU count.
// Result is always >= 1.
//
// Supported expressions:
//   - "" (empty) → totalCPUs
//   - "50%" → ceil(totalCPUs * 50 / 100), minimum 1
//   - "50% - 1" → ceil(totalCPUs * 50 / 100) - 1, minimum 1
func Eval(expr string, totalCPUs int) (int, error) {
	expr = trimSpace(expr)
	if expr == "" {
		if totalCPUs < 1 {
			return 1, nil
		}
		return totalCPUs, nil
	}

	m := exprRe.FindStringSubmatch(expr)
	if m == nil {
		return 0, fmt.Errorf("invalid cpu expression: %q", expr)
	}

	pct, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid cpu expression: %q", expr)
	}
	// Reject percentages outside (0, 100]. Values above 100% are nonsensical
	// (a service cannot use more CPUs than exist) and, without this check,
	// pathological inputs like "1e20%" would overflow the float→int
	// conversion below, leaving the program to rely on the final clamp as
	// a safety net.
	if pct <= 0 || pct > 100 {
		return 0, fmt.Errorf("cpu percentage must be in (0, 100], got %g: %q", pct, expr)
	}

	result := int(math.Ceil(float64(totalCPUs) * pct / 100))

	if m[2] != "" {
		sub, err := strconv.Atoi(m[2])
		if err != nil {
			return 0, fmt.Errorf("invalid cpu expression: %q", expr)
		}
		result -= sub
	}

	if result < 1 {
		result = 1
	}
	return result, nil
}

// trimSpace trims ASCII whitespace without allocating for already-trimmed strings.
func trimSpace(s string) string {
	for len(s) > 0 && s[0] == ' ' {
		s = s[1:]
	}
	for len(s) > 0 && s[len(s)-1] == ' ' {
		s = s[:len(s)-1]
	}
	return s
}
