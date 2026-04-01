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
