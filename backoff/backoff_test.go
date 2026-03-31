package backoff

import (
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	b := New(0, 0, 0)
	if b.Limit != 30*time.Second {
		t.Errorf("default limit = %v, want 30s", b.Limit)
	}
}

func TestExponentialGrowth(t *testing.T) {
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
