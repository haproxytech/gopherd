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

package cron

import (
	"testing"
	"time"
)

func TestParseValid(t *testing.T) {
	valid := []string{
		"* * * * *",
		"0 3 * * *",
		"*/5 * * * *",
		"1,15,30 * * * *",
		"0-30 * * * *",
		"0-30/5 * * * *",
		"0 0 1 jan mon",
		"0 0 1 JAN MON",
		"0 12 * * mon-fri",
		"59 23 31 12 6",
		"0 0 * * 0",
		"0 0 * * 7",          // 7 is an alias for Sunday
		"5/10 * * * *",       // "N/step" means "N-max/step"
		"  0   3  *  *  *  ", // extra whitespace
	}
	for _, expr := range valid {
		if _, err := Parse(expr); err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", expr, err)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	invalid := []struct {
		expr, reason string
	}{
		{"", "empty"},
		{"* * * *", "four fields"},
		{"* * * * * *", "six fields"},
		{"60 * * * *", "minute out of range"},
		{"* 24 * * *", "hour out of range"},
		{"* * 0 * *", "day of month zero"},
		{"* * 32 * *", "day of month out of range"},
		{"* * * 0 *", "month zero"},
		{"* * * 13 *", "month out of range"},
		{"* * * * 8", "day of week out of range"},
		{"a * * * *", "non-numeric"},
		{"* * * * xyz", "bad name"},
		{"*/0 * * * *", "zero step"},
		{"5-1 * * * *", "reversed range"},
		{"1- * * * *", "open range"},
		{"1,, * * * *", "empty list item"},
		{"*/x * * * *", "non-numeric step"},
	}
	for _, tc := range invalid {
		if _, err := Parse(tc.expr); err == nil {
			t.Errorf("Parse(%q) expected error (%s), got nil", tc.expr, tc.reason)
		}
	}
}

func mustParse(t *testing.T, expr string) *Schedule {
	t.Helper()
	s, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse(%q): %v", expr, err)
	}
	return s
}

func TestNext(t *testing.T) {
	// All in a fixed non-UTC zone to catch accidental UTC conversion.
	loc := time.FixedZone("TST", 2*60*60)
	at := func(y int, mo time.Month, d, h, mi int) time.Time {
		return time.Date(y, mo, d, h, mi, 0, 0, loc)
	}
	tests := []struct {
		expr  string
		after time.Time
		want  time.Time
	}{
		// Every minute: next minute boundary, seconds stripped.
		{"* * * * *", at(2026, 8, 22, 10, 30), at(2026, 8, 22, 10, 31)},
		{"* * * * *", time.Date(2026, 8, 22, 10, 30, 45, 123, loc), at(2026, 8, 22, 10, 31)},
		// Daily at 03:00 — same day if still ahead, else tomorrow.
		{"0 3 * * *", at(2026, 8, 22, 1, 0), at(2026, 8, 22, 3, 0)},
		{"0 3 * * *", at(2026, 8, 22, 3, 0), at(2026, 8, 23, 3, 0)}, // strictly after
		{"0 3 * * *", at(2026, 8, 22, 4, 0), at(2026, 8, 23, 3, 0)},
		// Every 15 minutes.
		{"*/15 * * * *", at(2026, 8, 22, 10, 16), at(2026, 8, 22, 10, 30)},
		{"*/15 * * * *", at(2026, 8, 22, 10, 45), at(2026, 8, 22, 11, 0)},
		// Hour rollover into next day.
		{"30 9 * * *", at(2026, 8, 22, 23, 59), at(2026, 8, 23, 9, 30)},
		// Day of week: 2026-08-22 is a Saturday; next Monday is 08-24.
		{"0 12 * * mon", at(2026, 8, 22, 0, 0), at(2026, 8, 24, 12, 0)},
		{"0 0 * * 0", at(2026, 8, 22, 0, 0), at(2026, 8, 23, 0, 0)}, // Sunday as 0
		{"0 0 * * 7", at(2026, 8, 22, 0, 0), at(2026, 8, 23, 0, 0)}, // ...and as 7
		// Steps: "*/step" spans the whole field, "N/step" spans N..max, and a
		// range endpoint is inclusive on both ends.
		{"5/10 * * * *", at(2026, 8, 22, 0, 0), at(2026, 8, 22, 0, 5)},
		{"5/10 * * * *", at(2026, 8, 22, 0, 5), at(2026, 8, 22, 0, 15)},
		{"5/10 * * * *", at(2026, 8, 22, 0, 45), at(2026, 8, 22, 0, 55)},
		{"55-59 * * * *", at(2026, 8, 22, 0, 58), at(2026, 8, 22, 0, 59)}, // hi inclusive
		{"* 20-23 * * *", at(2026, 8, 22, 22, 59), at(2026, 8, 22, 23, 0)},
		// Month boundary: 31st of next months that have one.
		{"0 0 31 * *", at(2026, 9, 1, 0, 0), at(2026, 10, 31, 0, 0)},
		// Month restriction rolls into next year.
		{"0 0 1 jan *", at(2026, 2, 1, 0, 0), at(2027, 1, 1, 0, 0)},
		// Jumping to a non-matching month must land on the 1st. Starting from a
		// day that does not exist in the next month (Jan 31 -> "Feb 31") would
		// otherwise normalise forward into March and skip the target day.
		{"0 0 1 3 *", at(2026, 1, 31, 0, 0), at(2026, 3, 1, 0, 0)},
		{"0 0 1 3 *", at(2026, 1, 29, 0, 0), at(2026, 3, 1, 0, 0)},
		{"0 0 5 4 *", at(2026, 1, 30, 0, 0), at(2026, 4, 5, 0, 0)},
		// Feb 29 only exists in leap years (2028 is next).
		{"0 0 29 2 *", at(2026, 1, 1, 0, 0), at(2028, 2, 29, 0, 0)},
		// dom AND dow when only one is restricted: 1st of month regardless of weekday.
		{"0 0 1 * *", at(2026, 8, 22, 0, 0), at(2026, 9, 1, 0, 0)},
		// Standard cron OR rule: both restricted -> either matches.
		// After Sat 2026-08-22: the 1st is Tue 09-01, but Monday 08-24 comes first.
		{"0 0 1 * mon", at(2026, 8, 22, 0, 0), at(2026, 8, 24, 0, 0)},
		// ...and dom can win the OR too: 1st (Tue) before next Friday 09-04.
		{"0 0 1 * fri", at(2026, 8, 30, 0, 0), at(2026, 9, 1, 0, 0)},
	}
	for _, tc := range tests {
		got := mustParse(t, tc.expr).Next(tc.after)
		if !got.Equal(tc.want) {
			t.Errorf("Next(%q, %v) = %v, want %v", tc.expr, tc.after, got, tc.want)
		}
		if got.Location() != tc.after.Location() {
			t.Errorf("Next(%q) location = %v, want %v", tc.expr, got.Location(), tc.after.Location())
		}
	}
}

// TestDayOfWeekSevenAliasesSunday pins the 0-and-7-both-mean-Sunday rule.
// time.Weekday() never sets bit 7, so without Parse's normalisation a
// "* * * * 7" schedule matches nothing and never runs.
func TestDayOfWeekSevenAliasesSunday(t *testing.T) {
	seven := mustParse(t, "0 3 * * 7")
	zero := mustParse(t, "0 3 * * 0")
	// Walk a whole week so a difference in any weekday shows up.
	at := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	for range 7 {
		got, want := seven.Next(at), zero.Next(at)
		if !got.Equal(want) {
			t.Fatalf("Next from %v: dow 7 = %v, dow 0 = %v (must be identical)", at, got, want)
		}
		if got.IsZero() {
			t.Fatalf("Next from %v returned the zero time: dow 7 matches nothing", at)
		}
		at = at.AddDate(0, 0, 1)
	}
}

func TestNextUnsatisfiable(t *testing.T) {
	// Feb 30 never exists; Next must give up rather than loop forever.
	s := mustParse(t, "0 0 30 2 *")
	if got := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !got.IsZero() {
		t.Errorf("Next on unsatisfiable schedule = %v, want zero time", got)
	}
}

func TestNextLeapAcrossSkippedCentury(t *testing.T) {
	// 2100 is not a leap year, so after Feb 29 2096 the next Feb 29 is 2104 —
	// an 8-year gap the search limit must accommodate.
	s := mustParse(t, "0 0 29 2 *")
	after := time.Date(2096, 3, 1, 0, 0, 0, 0, time.UTC)
	want := time.Date(2104, 2, 29, 0, 0, 0, 0, time.UTC)
	if got := s.Next(after); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}
