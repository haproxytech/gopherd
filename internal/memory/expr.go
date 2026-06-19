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
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// exprRe matches memory expressions:
//
//	"66%"          → percentage of available
//	"100% - 200MB" → percentage minus absolute
//	"512MB"        → absolute value
var exprRe = regexp.MustCompile(
	`^\s*(?:(\d+(?:\.\d+)?)\s*%\s*(?:-\s*(\d+(?:\.\d+)?)\s*(MB|MiB|GB|GiB))?\s*|(\d+(?:\.\d+)?)\s*(MB|MiB|GB|GiB))\s*$`,
)

// Eval evaluates a memory expression against the given total memory (in MiB).
// The result is always a whole number of MiB.
func Eval(expr string, totalMiB int64) (int64, error) {
	m := exprRe.FindStringSubmatch(expr)
	if m == nil {
		return 0, fmt.Errorf("invalid memory expression: %q", expr)
	}

	// Absolute value: "512MB", "1GiB"
	if m[4] != "" {
		val, err := strconv.ParseFloat(m[4], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid memory expression: %q", expr)
		}
		mib, err := toMiB(val, m[5])
		if err != nil {
			return 0, fmt.Errorf("invalid memory expression %q: %w", expr, err)
		}
		if mib <= 0 {
			return 0, fmt.Errorf("memory expression evaluates to %d MiB: %q", mib, expr)
		}
		return mib, nil
	}

	// Percentage: "66%" or "100% - 200MB"
	pct, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory expression: %q", expr)
	}
	// Reject percentages outside (0, 100]: a service cannot exceed available memory.
	if pct <= 0 || pct > 100 {
		return 0, fmt.Errorf("memory percentage must be in (0, 100], got %g: %q", pct, expr)
	}
	// Guard overflow: a corrupted, pathologically large totalMiB would saturate
	// the float64→int64 conversion to int64 min, yielding a bogus negative result.
	resF := float64(totalMiB) * pct / 100
	if resF > float64(math.MaxInt64) || math.IsInf(resF, 0) || math.IsNaN(resF) {
		return 0, fmt.Errorf("memory expression overflows int64: %q (total: %d MiB)", expr, totalMiB)
	}
	result := int64(resF)

	if m[2] != "" {
		sub, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid memory expression: %q", expr)
		}
		subMiB, err := toMiB(sub, m[3])
		if err != nil {
			return 0, fmt.Errorf("invalid memory expression %q: %w", expr, err)
		}
		result -= subMiB
	}

	if result <= 0 {
		return 0, fmt.Errorf("memory expression evaluates to %d MiB (total: %d MiB): %q", result, totalMiB, expr)
	}
	return result, nil
}

// toMiB converts a value+unit to MiB, rounding so 1 MB (0.953 MiB) becomes 1, not
// a confusing 0. Out-of-range/non-finite results are rejected explicitly rather
// than trusting the implementation-defined overflowing float64→int64 conversion.
func toMiB(val float64, unit string) (int64, error) {
	var mib float64
	switch strings.ToUpper(unit) {
	case "MB":
		mib = math.Round(val * 1e6 / (1 << 20))
	case "MIB":
		mib = math.Round(val)
	case "GB":
		mib = math.Round(val * 1e9 / (1 << 20))
	case "GIB":
		mib = math.Round(val * 1024)
	default:
		mib = math.Round(val)
	}
	if math.IsNaN(mib) || math.IsInf(mib, 0) || mib > float64(math.MaxInt64) {
		return 0, fmt.Errorf("memory value %g %s overflows int64", val, unit)
	}
	return int64(mib), nil
}
