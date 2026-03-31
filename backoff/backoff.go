// Package backoff provides exponential backoff with jitter for service restarts.
package backoff

import (
	"math"
	"math/rand/v2"
	"time"
)

// Backoff calculates exponential backoff delays with jitter for service restarts.
type Backoff struct {
	delay  time.Duration // initial delay (default 500ms)
	factor float64       // multiplier per attempt (default 2.0)
	Limit  time.Duration // max delay cap (default 30s)

	attempt int
}

// New creates a new Backoff with the given parameters.
// Zero/negative values are replaced with defaults.
func New(delay time.Duration, factor float64, limit time.Duration) *Backoff {
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}
	if factor <= 0 {
		factor = 2.0
	}
	if limit <= 0 {
		limit = 30 * time.Second
	}
	return &Backoff{delay: delay, factor: factor, Limit: limit}
}

// Next returns the next backoff duration with ±10% jitter.
func (b *Backoff) Next() time.Duration {
	d := float64(b.delay) * math.Pow(b.factor, float64(b.attempt))
	if d > float64(b.Limit) {
		d = float64(b.Limit)
	}
	b.attempt++
	// Add ±10% jitter
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
}
