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
		mib := toMiB(val, m[5])
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
	// Guard against the final float→int64 conversion overflowing on
	// pathologically large totals or percentages (L6). math.MaxInt64 as a
	// float64 loses precision; anything above that saturates to int64 min
	// on conversion, which would produce a negative "valid" result below.
	resF := float64(totalMiB) * pct / 100
	if resF > float64(math.MaxInt64) || math.IsInf(resF, 0) || math.IsNaN(resF) {
		return 0, fmt.Errorf("memory expression overflows int64: %q (total: %d MiB)", expr, totalMiB)
	}
	result := int64(resF)

	// Subtract fixed amount if present.
	if m[2] != "" {
		sub, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid memory expression: %q", expr)
		}
		result -= toMiB(sub, m[3])
	}

	if result <= 0 {
		return 0, fmt.Errorf("memory expression evaluates to %d MiB (total: %d MiB): %q", result, totalMiB, expr)
	}
	return result, nil
}

// toMiB converts a value with a unit to MiB.
// math.Round is used instead of truncation so that values like 1 MB
// (0.953 MiB) round to 1 MiB rather than 0, preventing a confusing
// "evaluates to 0 MiB" error for small but valid inputs.
func toMiB(val float64, unit string) int64 {
	switch strings.ToUpper(unit) {
	case "MB":
		return int64(math.Round(val * 1e6 / (1 << 20)))
	case "MIB":
		return int64(math.Round(val))
	case "GB":
		return int64(math.Round(val * 1e9 / (1 << 20)))
	case "GIB":
		return int64(math.Round(val * 1024))
	default:
		return int64(math.Round(val))
	}
}
