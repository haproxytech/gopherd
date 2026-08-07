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

package check

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/reaper"
)

// reapAndDeliver stands in for the daemon reap loop: it owns Wait4(-1) and
// delivers every reaped status through the registry. Returns a stop func that
// blocks until the loop exits.
func reapAndDeliver(r *reaper.Registry) (stop func()) {
	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stopCh:
				return
			default:
			}
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, 0, nil)
			if err != nil {
				time.Sleep(time.Millisecond)
				continue
			}
			code := 1
			if ws.Exited() {
				code = ws.ExitStatus()
			} else if ws.Signaled() {
				code = 128 + int(ws.Signal())
			}
			r.Deliver(pid, code)
		}
	})
	return func() {
		close(stopCh)
		wg.Wait()
	}
}

func TestObserveInconclusiveNotCounted(t *testing.T) {
	t.Parallel()
	var failures, probes int
	c, err := New("obs", Config{
		Exec: &Exec{Command: "true"}, Period: "1s", Timeout: "1s", Threshold: 2,
	}, func(string) { failures++ }, func(string, bool) { probes++ })
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	inconclusive := fmt.Errorf("exec check: %w", ErrInconclusive)
	if c.observe(inconclusive) {
		t.Fatal("inconclusive probe tripped the threshold")
	}
	if c.observe(inconclusive) {
		t.Fatal("second inconclusive probe tripped the threshold")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failures != 0 {
		t.Fatalf("failures = %d, want 0", c.failures)
	}
	if !c.healthy {
		t.Fatal("checker became unhealthy from inconclusive probes")
	}
	if probes != 0 {
		t.Fatalf("metrics recorded %d probes, want 0 (no data)", probes)
	}
	if failures != 0 {
		t.Fatalf("onFailure fired %d times, want 0", failures)
	}
}

func TestObserveInconclusivePreservesStreak(t *testing.T) {
	t.Parallel()
	c, err := New("streak", Config{
		Exec: &Exec{Command: "true"}, Period: "1s", Timeout: "1s", Threshold: 2,
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	failure := errors.New("probe failed")
	if c.observe(failure) {
		t.Fatal("threshold tripped after one failure")
	}
	if c.observe(fmt.Errorf("exec check: %w", ErrInconclusive)) {
		t.Fatal("inconclusive probe tripped the threshold")
	}
	if !c.observe(failure) {
		t.Fatal("second real failure did not trip the threshold")
	}
}

func TestObserveSuccessAndFailureCounting(t *testing.T) {
	t.Parallel()
	c, err := New("count", Config{
		Exec: &Exec{Command: "true"}, Period: "1s", Timeout: "1s", Threshold: 2,
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	failure := errors.New("probe failed")
	c.observe(failure)
	if c.observe(nil) {
		t.Fatal("success tripped the threshold")
	}
	// Success resets the streak: two more failures needed to trip.
	if c.observe(failure) {
		t.Fatal("threshold tripped after streak reset")
	}
	if !c.observe(failure) {
		t.Fatal("threshold did not trip at 2 consecutive failures")
	}
}

// Reproduces the production restart: a concurrent Wait4(-1) loop (the daemon
// reap loop) steals exec probe exit statuses, and the resulting ECHILD must
// surface as ErrInconclusive, never as a probe failure.
func TestExecStolenStatusIsInconclusive(t *testing.T) {
	// No t.Parallel: the hostile Wait4(-1) would reap other tests' children.
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			var ws syscall.WaitStatus
			_, err := syscall.Wait4(-1, &ws, 0, nil)
			if err != nil {
				time.Sleep(time.Millisecond)
			}
		}
	})
	defer wg.Wait()
	defer close(stop)

	c, err := New("steal", Config{
		Exec: &Exec{Command: "true"}, Period: "1s", Timeout: "2s",
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	lost := 0
	for range 300 {
		err := c.Execute()
		if err == nil {
			continue
		}
		if !errors.Is(err, ErrInconclusive) {
			t.Fatalf("stolen status surfaced as a real failure: %v", err)
		}
		lost++
	}
	t.Logf("%d/300 probe statuses stolen by the reap loop", lost)
}

// With an active registry the reap loop delivers every status: no probe is
// ever lost or misreported, even though Wait4(-1) reaps all children.
func TestExecReaperDeliveredSuccess(t *testing.T) {
	// No t.Parallel: the Wait4(-1) loop would reap other tests' children.
	r := reaper.New(nil)
	r.Activate()
	defer reapAndDeliver(r)()

	c, err := New("delivered", Config{
		Exec: &Exec{Command: "true"}, Period: "1s", Timeout: "2s",
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.SetReaper(r)

	for i := range 300 {
		if err := c.Execute(); err != nil {
			t.Fatalf("probe %d failed: %v", i, err)
		}
	}
}

func TestExecReaperDeliveredFailure(t *testing.T) {
	// No t.Parallel: the Wait4(-1) loop would reap other tests' children.
	r := reaper.New(nil)
	r.Activate()
	defer reapAndDeliver(r)()

	c, err := New("delivered-fail", Config{
		Exec: &Exec{Command: "false"}, Period: "1s", Timeout: "2s",
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.SetReaper(r)

	err = c.Execute()
	if err == nil {
		t.Fatal("probe of 'false' succeeded")
	}
	if errors.Is(err, ErrInconclusive) {
		t.Fatalf("real failure reported as inconclusive: %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("err = %v, want exit status 1", err)
	}
}

func TestExecReaperTimeoutKillsChild(t *testing.T) {
	// No t.Parallel: the Wait4(-1) loop would reap other tests' children.
	r := reaper.New(nil)
	r.Activate()
	defer reapAndDeliver(r)()

	c, err := New("delivered-timeout", Config{
		Exec: &Exec{Command: "sleep", Args: []string{"10"}}, Period: "1s", Timeout: "100ms",
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.SetReaper(r)

	start := time.Now()
	err = c.Execute()
	if err == nil {
		t.Fatal("probe of 'sleep 10' with 100ms timeout succeeded")
	}
	if errors.Is(err, ErrInconclusive) {
		t.Fatalf("timeout reported as inconclusive: %v", err)
	}
	// Well under the 10s sleep: the group was killed, not waited out.
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("probe took %s, child was not killed on timeout", elapsed)
	}
}

// An inactive registry means no reap loop delivers yet (startup sequence):
// probes must fall back to waiting directly.
func TestExecReaperInactiveFallsBack(t *testing.T) {
	t.Parallel()
	c, err := New("inactive", Config{
		Exec: &Exec{Command: "true"}, Period: "1s", Timeout: "2s",
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.SetReaper(reaper.New(nil))

	if err := c.Execute(); err != nil {
		t.Fatalf("probe failed with inactive registry: %v", err)
	}
}
