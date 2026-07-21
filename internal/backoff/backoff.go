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

// Package backoff provides exponential backoff with jitter for service restarts.
package backoff

import (
	"math"
	"math/rand/v2"
	"time"
)

// Backoff calculates exponential backoff delays with jitter for service restarts.
// Not thread-safe: Next() and Reset() must be called from a single goroutine or
// under an external lock. In gopherd, both are called from the reap loop under d.mu.
type Backoff struct {
	delay   time.Duration // initial delay
	factor  float64       // multiplier per attempt
	Limit   time.Duration // max delay cap
	atLimit bool          // capped; skips further math.Pow (P3)

	attempt int
}

// New creates a new Backoff. Zero/negative/non-finite values are replaced with
// defaults (delay 500ms, factor 2.0, limit 30s).
func New(delay time.Duration, factor float64, limit time.Duration) *Backoff {
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}
	// factor <= 0 alone misses NaN (all NaN comparisons are false), which makes
	// Next() collapse to zero delays after the first attempt. yml rejects these
	// at load too; this keeps the library safe for any caller.
	if factor <= 0 || math.IsNaN(factor) || math.IsInf(factor, 0) {
		factor = 2.0
	}
	if limit <= 0 {
		limit = 30 * time.Second
	}
	return &Backoff{delay: delay, factor: factor, Limit: limit}
}

// Next returns the next backoff duration with ±10% jitter.
func (b *Backoff) Next() time.Duration {
	var d float64
	if b.atLimit {
		// Skip math.Pow, which would overflow to +Inf for large attempt (P3).
		d = float64(b.Limit)
	} else {
		d = float64(b.delay) * math.Pow(b.factor, float64(b.attempt))
		if d > float64(b.Limit) {
			d = float64(b.Limit)
			b.atLimit = true
		}
	}
	b.attempt++
	jitter := d * 0.1 * (2*rand.Float64() - 1)
	d += jitter
	if d < 0 {
		d = 0
	}
	return time.Duration(d)
}

// Reset resets the backoff counter (called when a process runs longer than limit).
func (b *Backoff) Reset() {
	b.attempt = 0
	b.atLimit = false
}
