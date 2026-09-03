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

package reaper

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDeliverReachesRegisteredWaiter(t *testing.T) {
	r := New(nil)

	pid, status, err := r.Start(func() (int, error) { return 42, nil })
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if pid != 42 {
		t.Fatalf("pid = %d, want 42", pid)
	}

	if !r.Deliver(42, 7) {
		t.Fatal("Deliver returned false for a registered pid")
	}
	select {
	case code := <-status:
		if code != 7 {
			t.Fatalf("status = %d, want 7", code)
		}
	case <-time.After(time.Second):
		t.Fatal("status never delivered")
	}
}

func TestDeliverUnknownPidReturnsFalse(t *testing.T) {
	r := New(nil)
	if r.Deliver(999, 0) {
		t.Fatal("Deliver returned true for an unregistered pid")
	}
}

// waiterCount reads the registry size under its own lock; same-package access
// keeps this a test-only detail rather than exported surface.
func waiterCount(r *Registry) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.waiters)
}

// TestDeliverConsumesWaiter pins that a delivered pid is unregistered. Each
// status channel buffers exactly one send, so a waiter left behind makes the
// next Deliver for that recycled pid block on a full buffer — holding the
// registry lock, stalling the reap loop for good — and leaks a map entry per
// probe besides.
func TestDeliverConsumesWaiter(t *testing.T) {
	r := New(nil)
	pid, status, err := r.Start(func() (int, error) { return 42, nil })
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !r.Deliver(pid, 7) {
		t.Fatal("first Deliver returned false for a registered pid")
	}
	// Drain so a lingering waiter's buffer would have room; the point is that
	// no waiter should remain to claim a second delivery at all.
	<-status
	if r.Deliver(pid, 9) {
		t.Fatal("second Deliver claimed the same pid again: the waiter was not " +
			"removed on delivery")
	}

	// The registry must not grow across many probe lifecycles.
	for i := range 200 {
		p, ch, err := r.Start(func() (int, error) { return 1000 + i, nil })
		if err != nil {
			t.Fatalf("Start %d: %v", i, err)
		}
		r.Deliver(p, 0)
		<-ch
	}
	if n := waiterCount(r); n != 0 {
		t.Errorf("registry holds %d waiters after 200 delivered probes, want 0", n)
	}
}

func TestForgetDropsWaiter(t *testing.T) {
	r := New(nil)
	pid, _, err := r.Start(func() (int, error) { return 42, nil })
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	r.Forget(pid)
	if r.Deliver(pid, 0) {
		t.Fatal("Deliver returned true after Forget")
	}
}

func TestStartErrorRegistersNothing(t *testing.T) {
	r := New(nil)
	boom := errors.New("fork failed")
	_, _, err := r.Start(func() (int, error) { return 0, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if r.Deliver(0, 0) {
		t.Fatal("Deliver returned true after failed Start")
	}
}

// Deliver must not observe a forked pid before Start registers it: a Deliver
// racing with Start blocks until registration completes, then finds the waiter.
func TestDeliverCannotOutrunRegistration(t *testing.T) {
	r := New(nil)
	forked := make(chan struct{})
	var wg sync.WaitGroup
	var delivered bool
	wg.Go(func() {
		<-forked
		delivered = r.Deliver(42, 0)
	})

	_, status, err := r.Start(func() (int, error) {
		close(forked)
		// Give the delivering goroutine time to run: it must block on the
		// registry lock, not return false.
		time.Sleep(50 * time.Millisecond)
		return 42, nil
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	wg.Wait()
	if !delivered {
		t.Fatal("Deliver lost the race against Start registration")
	}
	select {
	case <-status:
	case <-time.After(time.Second):
		t.Fatal("status never delivered")
	}
}

func TestActivate(t *testing.T) {
	r := New(nil)
	if r.Active() {
		t.Fatal("new registry reports active")
	}
	r.Activate()
	if !r.Active() {
		t.Fatal("registry not active after Activate")
	}
}

func TestStartCallsWake(t *testing.T) {
	woken := 0
	r := New(func() { woken++ })
	if _, _, err := r.Start(func() (int, error) { return 42, nil }); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if woken != 1 {
		t.Fatalf("wake called %d times, want 1", woken)
	}
	// Failed forks must not wake: there is no child to reap.
	_, _, _ = r.Start(func() (int, error) { return 0, errors.New("fork failed") })
	if woken != 1 {
		t.Fatalf("wake called %d times after failed fork, want 1", woken)
	}
}
