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

package backoff

import (
	"math"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	t.Parallel()
	b := New(0, 0, 0)
	if b.Limit != 30*time.Second {
		t.Errorf("default limit = %v, want 30s", b.Limit)
	}
}

// TestNonFiniteFactor guards the fix for the NaN fork-bomb: a NaN factor slips
// past the plain factor <= 0 check (every NaN comparison is false) and would
// otherwise collapse every delay after the first to zero. New must default such
// factors so Next() keeps producing sane, growing delays.
func TestNonFiniteFactor(t *testing.T) {
	t.Parallel()
	for _, factor := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, -2} {
		b := New(100*time.Millisecond, factor, 10*time.Second)
		for i := range 5 {
			d := b.Next()
			if d < 0 {
				t.Fatalf("factor %v: Next[%d] = %v, want a non-negative finite delay", factor, i, d)
			}
		}
		// After several attempts the defaulted factor (2.0) must have grown the
		// delay beyond the initial value — proving it is not stuck at zero.
		if got := b.Next(); got <= 0 {
			t.Errorf("factor %v: delay collapsed to %v after several attempts", factor, got)
		}
	}
}

func TestExponentialGrowth(t *testing.T) {
	t.Parallel()
	b := New(100*time.Millisecond, 2.0, 10*time.Second)
	prev := b.Next()
	for range 5 {
		next := b.Next()
		if next < prev {
			t.Errorf("backoff should generally grow: %v < %v", next, prev)
		}
		prev = next
	}
}

func TestCapsAtLimit(t *testing.T) {
	t.Parallel()
	b := New(1*time.Second, 10.0, 5*time.Second)
	for range 20 {
		d := b.Next()
		// Allow 10% jitter above limit
		if d > 5*time.Second+550*time.Millisecond {
			t.Errorf("exceeded limit with jitter: %v", d)
		}
	}
}

func TestReset(t *testing.T) {
	t.Parallel()
	b := New(100*time.Millisecond, 2.0, 10*time.Second)
	for range 10 {
		b.Next()
	}
	b.Reset()
	d := b.Next()
	if d > 200*time.Millisecond {
		t.Errorf("after reset, expected ~100ms, got %v", d)
	}
}

// TestResetGivesInitialDelay verifies that Reset() sets attempt to 0, not 1.
// With attempt=1 the first Next() after Reset() returns delay*factor^1 (200ms)
// instead of delay*factor^0 (100ms). We allow a 20% window to absorb jitter
// while still clearly distinguishing the two levels.
func TestResetGivesInitialDelay(t *testing.T) {
	t.Parallel()
	b := New(100*time.Millisecond, 2.0, 10*time.Second)
	for range 10 {
		b.Next()
	}
	b.Reset()
	d := b.Next()
	// After reset, first delay must be close to 100ms (±10% jitter → [90ms,110ms]).
	// If reset leaves attempt=1 the value would be ~200ms, well above 150ms.
	if d > 150*time.Millisecond {
		t.Errorf("after reset first delay = %v; want ≤150ms (should be ~100ms, not ~200ms)", d)
	}
	if d < 50*time.Millisecond {
		t.Errorf("after reset first delay = %v; want ≥50ms", d)
	}
}

// TestJitterBidirectional verifies that jitter is ±10%, not 0–10%.
// With one-sided jitter Next() can never return less than the base delay.
// Over many samples at least one must fall below the base value.
func TestJitterBidirectional(t *testing.T) {
	t.Parallel()
	const base = 500 * time.Millisecond
	b := New(base, 1.0, 10*time.Second) // factor=1 keeps d==base every call
	below := false
	for range 200 {
		if b.Next() < base {
			below = true
			break
		}
	}
	if !below {
		t.Error("jitter appears one-sided: no sample fell below base delay in 200 calls")
	}
}

// TestAtLimitLatchSetWhenCapped pins the overflow guard's own state. The
// clamped value is identical either way, so the output cannot show it — but the
// latch is what stops math.Pow being evaluated for an ever-growing attempt
// count, which is why it exists: without it a long-lived crash loop raises the
// factor to the thousandth power on every restart. Only the field itself pins
// it; Reset clearing it is covered separately.
func TestAtLimitLatchSetWhenCapped(t *testing.T) {
	t.Parallel()
	b := New(100*time.Millisecond, 2.0, time.Second)
	if b.atLimit {
		t.Fatal("a fresh backoff must not start latched")
	}

	// 100ms * 2^n passes 1s at n = 4.
	var capped bool
	for range 10 {
		b.Next()
		if b.atLimit {
			capped = true
			break
		}
	}
	if !capped {
		t.Error("the backoff reached its limit without latching; the latch is what " +
			"keeps math.Pow from being evaluated for an unbounded attempt count")
	}

	// Once latched it stays latched, and the delays stay at the cap.
	for range 5 {
		if d := b.Next(); d > time.Second+150*time.Millisecond {
			t.Errorf("delay %v exceeds the limit plus jitter", d)
		}
		if !b.atLimit {
			t.Error("the latch was cleared without a Reset")
		}
	}
}
