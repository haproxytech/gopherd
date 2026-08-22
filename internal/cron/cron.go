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

// Package cron parses standard 5-field cron expressions (minute, hour,
// day-of-month, month, day-of-week) and computes the next matching time.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// field bounds and name tables for each of the 5 cron fields, in order.
var fieldDefs = []struct {
	name  string
	min   int
	max   int
	names map[string]int
}{
	{"minute", 0, 59, nil},
	{"hour", 0, 23, nil},
	{"day of month", 1, 31, nil},
	{"month", 1, 12, map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}},
	{"day of week", 0, 7, map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}},
}

// Schedule is a parsed cron expression. Each field is a bitmask of the
// allowed values (bit n set = value n matches).
type Schedule struct {
	minute uint64
	hour   uint64
	dom    uint64
	month  uint64
	dow    uint64
	// domStar/dowStar record whether the field was "*" (or "*/step" of 1),
	// needed for the standard cron OR rule when both dom and dow are
	// restricted.
	domStar bool
	dowStar bool
}

// Parse parses a standard 5-field cron expression: minute (0-59),
// hour (0-23), day of month (1-31), month (1-12 or jan-dec) and day of week
// (0-7 or sun-sat; both 0 and 7 mean Sunday). Each field supports "*",
// lists ("1,15"), ranges ("1-5"), and steps ("*/5", "1-30/5").
func Parse(expr string) (*Schedule, error) {
	fields := strings.Fields(expr)
	if len(fields) != len(fieldDefs) {
		return nil, fmt.Errorf("cron expression %q: expected 5 fields, got %d", expr, len(fields))
	}
	var masks [5]uint64
	var stars [5]bool
	for i, f := range fields {
		mask, star, err := parseField(f, i)
		if err != nil {
			return nil, fmt.Errorf("cron expression %q: %w", expr, err)
		}
		masks[i] = mask
		stars[i] = star
	}
	s := &Schedule{
		minute:  masks[0],
		hour:    masks[1],
		dom:     masks[2],
		month:   masks[3],
		dow:     masks[4],
		domStar: stars[2],
		dowStar: stars[4],
	}
	// Day-of-week 7 is an alias for Sunday (0).
	if s.dow&(1<<7) != 0 {
		s.dow |= 1
		s.dow &^= 1 << 7
	}
	return s, nil
}

// parseField parses one comma-separated cron field into a bitmask. The
// returned bool reports whether the field is an unrestricted "*".
func parseField(field string, idx int) (uint64, bool, error) {
	def := fieldDefs[idx]
	var mask uint64
	star := false
	for part := range strings.SplitSeq(field, ",") {
		if part == "" {
			return 0, false, fmt.Errorf("%s: empty list item in %q", def.name, field)
		}
		rangePart, stepPart, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepPart)
			if err != nil {
				return 0, false, fmt.Errorf("%s: invalid step %q", def.name, stepPart)
			}
			if n < 1 {
				return 0, false, fmt.Errorf("%s: step must be positive, got %d", def.name, n)
			}
			step = n
		}
		lo, hi := def.min, def.max
		switch {
		case rangePart == "*":
			if !hasStep && field == "*" {
				star = true
			}
		case strings.Contains(rangePart, "-"):
			loStr, hiStr, _ := strings.Cut(rangePart, "-")
			var err error
			if lo, err = parseValue(loStr, idx); err != nil {
				return 0, false, err
			}
			if hi, err = parseValue(hiStr, idx); err != nil {
				return 0, false, err
			}
			if lo > hi {
				return 0, false, fmt.Errorf("%s: reversed range %q", def.name, rangePart)
			}
		default:
			v, err := parseValue(rangePart, idx)
			if err != nil {
				return 0, false, err
			}
			lo, hi = v, v
			if hasStep {
				// "N/step" means "N-max/step" in classic cron.
				hi = def.max
			}
		}
		for v := lo; v <= hi; v += step {
			mask |= 1 << uint(v)
		}
	}
	return mask, star, nil
}

// parseValue parses a single numeric or named value for field idx and checks
// its bounds.
func parseValue(s string, idx int) (int, error) {
	def := fieldDefs[idx]
	if def.names != nil {
		if v, ok := def.names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid value %q", def.name, s)
	}
	if v < def.min || v > def.max {
		return 0, fmt.Errorf("%s: value %d out of range (%d-%d)", def.name, v, def.min, def.max)
	}
	return v, nil
}

// nextLimitYears bounds the search in Next so an unsatisfiable schedule
// (e.g. "0 0 30 2 *") returns a zero time instead of looping forever. Nine
// years covers the longest real gap: Feb 29 across a skipped century leap
// year (2096 -> 2104) is eight years.
const nextLimitYears = 9

// Next returns the first time strictly after `after` that matches the
// schedule, at minute granularity, in after's location. It returns the zero
// time if no match exists within nextLimitYears.
func (s *Schedule) Next(after time.Time) time.Time {
	loc := after.Location()
	// Start at the next whole minute, seconds and below stripped.
	y, mo, d := after.Date()
	h, mi, _ := after.Clock()
	t := time.Date(y, mo, d, h, mi, 0, 0, loc).Add(time.Minute)

	limit := after.AddDate(nextLimitYears, 0, 0)
	for !t.After(limit) {
		if s.month&(1<<uint(t.Month())) == 0 {
			// Jump to the first minute of the next month.
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			continue
		}
		if s.minute&(1<<uint(t.Minute())) == 0 {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

// dayMatches applies the standard cron day rule: when both day-of-month and
// day-of-week are restricted, a day matching either fires; otherwise the
// restricted field (if any) governs alone.
func (s *Schedule) dayMatches(t time.Time) bool {
	domOK := s.dom&(1<<uint(t.Day())) != 0
	dowOK := s.dow&(1<<uint(t.Weekday())) != 0
	if !s.domStar && !s.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}
