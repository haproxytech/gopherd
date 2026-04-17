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
