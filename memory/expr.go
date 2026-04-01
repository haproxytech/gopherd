package memory

import (
	"fmt"
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
	result := int64(float64(totalMiB) * pct / 100)

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
func toMiB(val float64, unit string) int64 {
	switch strings.ToUpper(unit) {
	case "MB":
		return int64(val * 1000 * 1000 / (1024 * 1024))
	case "MIB":
		return int64(val)
	case "GB":
		return int64(val * 1000 * 1000 * 1000 / (1024 * 1024))
	case "GIB":
		return int64(val * 1024)
	default:
		return int64(val)
	}
}
