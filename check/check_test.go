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
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewValidation(t *testing.T) {
	t.Parallel()
	_, err := New("bad", Config{Period: "1s", Timeout: "1s"}, nil, nil)
	if err == nil {
		t.Fatal("expected error for no check type")
	}

	_, err = New("bad", Config{
		HTTP: &HTTP{URL: "http://localhost"},
		TCP:  &TCP{Port: 80},
	}, nil, nil)
	if err == nil {
		t.Fatal("expected error for multiple check types")
	}

	c, err := New("ok", Config{
		HTTP:    &HTTP{URL: "http://localhost"},
		Period:  "5s",
		Timeout: "2s",
	}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.period != 5*time.Second {
		t.Errorf("period = %v, want 5s", c.period)
	}
}

func TestDefaults(t *testing.T) {
	t.Parallel()
	c, err := New("defaults", Config{TCP: &TCP{Port: 80}}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.period != 10*time.Second {
		t.Errorf("default period = %v, want 10s", c.period)
	}
	if c.timeout != 3*time.Second {
		t.Errorf("default timeout = %v, want 3s", c.timeout)
	}
	if c.threshold != 3 {
		t.Errorf("default threshold = %d, want 3", c.threshold)
	}
}

func TestHTTPSuccess(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	c, _ := New("http-ok", Config{HTTP: &HTTP{URL: ts.URL}, Period: "1s", Timeout: "2s"}, nil, nil)
	if err := c.Execute(); err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}

func TestHTTPFailure(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer ts.Close()

	c, _ := New("http-fail", Config{HTTP: &HTTP{URL: ts.URL}, Period: "1s", Timeout: "2s"}, nil, nil)
	if err := c.Execute(); err == nil {
		t.Error("expected failure for 500 status")
	}
}

func TestTCPSuccess(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	c, _ := New("tcp-ok", Config{TCP: &TCP{Host: "127.0.0.1", Port: port}, Period: "1s", Timeout: "2s"}, nil, nil)
	if err := c.Execute(); err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}

func TestTCPFailure(t *testing.T) {
	t.Parallel()
	c, _ := New("tcp-fail", Config{TCP: &TCP{Host: "127.0.0.1", Port: 1}, Period: "1s", Timeout: "1s"}, nil, nil)
	if err := c.Execute(); err == nil {
		t.Error("expected failure for closed port")
	}
}

func TestExecSuccess(t *testing.T) {
	t.Parallel()
	c, _ := New("exec-ok", Config{Exec: &Exec{Command: "true"}, Period: "1s", Timeout: "2s"}, nil, nil)
	if err := c.Execute(); err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}

func TestExecFailure(t *testing.T) {
	t.Parallel()
	c, _ := New("exec-fail", Config{Exec: &Exec{Command: "false"}, Period: "1s", Timeout: "2s"}, nil, nil)
	if err := c.Execute(); err == nil {
		t.Error("expected failure for 'false' command")
	}
}

func TestThresholdCallback(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer ts.Close()

	var called atomic.Int32
	c, _ := New("threshold", Config{
		HTTP: &HTTP{URL: ts.URL}, Period: "50ms", Timeout: "1s", Threshold: 2,
	}, func(_ string) { called.Add(1) }, nil)

	c.Run()
	defer c.Stop()
	time.Sleep(300 * time.Millisecond)

	if called.Load() == 0 {
		t.Error("expected onFailure callback")
	}
}

func TestHTTPUnixSocket(t *testing.T) {
	t.Parallel()
	sockPath := filepath.Join(t.TempDir(), "health.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	c, _ := New("unix-http", Config{
		HTTP: &HTTP{URL: "http://localhost/healthz", Socket: sockPath}, Period: "1s", Timeout: "2s",
	}, nil, nil)
	if err := c.Execute(); err != nil {
		t.Errorf("expected success via unix socket, got: %v", err)
	}
}

func TestWaitReadySuccess(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	c, _ := New("ready-ok", Config{HTTP: &HTTP{URL: ts.URL}, Period: "50ms", Timeout: "1s"}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.WaitReady(ctx); err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}

func TestWaitReadyTimeout(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer ts.Close()

	c, _ := New("ready-fail", Config{HTTP: &HTTP{URL: ts.URL}, Period: "50ms", Timeout: "1s"}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := c.WaitReady(ctx); err == nil {
		t.Error("expected timeout")
	}
}

func TestWaitReadyEventualSuccess(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	defer ts.Close()

	c, _ := New("ready-eventual", Config{HTTP: &HTTP{URL: ts.URL}, Period: "50ms", Timeout: "1s"}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.WaitReady(ctx); err != nil {
		t.Errorf("expected eventual success, got: %v", err)
	}
}

func TestInitialDelay(t *testing.T) {
	t.Parallel()
	// Server always returns 500 — check should not fire callback
	// within the initial delay window.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer ts.Close()

	var called atomic.Int32
	c, err := New("delay", Config{
		HTTP:         &HTTP{URL: ts.URL},
		Period:       "50ms",
		Timeout:      "1s",
		Threshold:    1,
		InitialDelay: "500ms", // don't check for 500ms
	}, func(_ string) { called.Add(1) }, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c.Run()
	defer c.Stop()

	// After 200ms (within initial delay), callback should NOT have fired.
	time.Sleep(200 * time.Millisecond)
	if called.Load() != 0 {
		t.Error("callback fired during initial delay")
	}

	// After 700ms total (past initial delay + enough periods), it should have fired.
	time.Sleep(500 * time.Millisecond)
	if called.Load() == 0 {
		t.Error("expected callback after initial delay passed")
	}
}

func TestInitialDelayZero(t *testing.T) {
	t.Parallel()
	// initial-delay = 0 means check immediately.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer ts.Close()

	var called atomic.Int32
	c, _ := New("nodelay", Config{
		HTTP:         &HTTP{URL: ts.URL},
		Period:       "50ms",
		Timeout:      "1s",
		Threshold:    1,
		InitialDelay: "0s",
	}, func(_ string) { called.Add(1) }, nil)

	c.Run()
	defer c.Stop()

	time.Sleep(150 * time.Millisecond)
	if called.Load() == 0 {
		t.Error("expected immediate check with initial-delay=0")
	}
}

func TestInitialDelayDefault(t *testing.T) {
	t.Parallel()
	// Without initial-delay set, default should be 1x period.
	c, err := New("default-delay", Config{
		HTTP:   &HTTP{URL: "http://localhost"},
		Period: "5s",
	}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.initialDelay != 5*time.Second {
		t.Errorf("initialDelay = %v, want 5s (same as period)", c.initialDelay)
	}
}

func TestInitialDelayInvalid(t *testing.T) {
	t.Parallel()
	_, err := New("bad-delay", Config{
		HTTP:         &HTTP{URL: "http://localhost"},
		Period:       "1s",
		InitialDelay: "not-a-duration",
	}, nil, nil)
	if err == nil {
		t.Error("expected error for invalid initial-delay")
	}
}

// TestInitialHealthyState covers M-26: a newly created Checker must start
// in a healthy=true state so the first callback only fires after threshold
// consecutive failures, not immediately on the first failure.
func TestInitialHealthyState(t *testing.T) {
	t.Parallel()
	c, err := New("healthy-init", Config{TCP: &TCP{Port: 80}, Period: "5s", Timeout: "1s"}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !c.healthy {
		t.Error("new Checker must start healthy=true")
	}
}

// TestThresholdFiredAtExactCount covers M-23: the failure callback must fire
// after exactly Threshold failures, not Threshold+1.
// With InitialDelay="0s" the first check runs immediately; with threshold=1 the
// callback fires before the first period elapses. The mutation (>threshold) needs
// a second failure, which takes one full period (100ms). We sleep 50ms — past
// goroutine startup but well before the second tick.
func TestThresholdFiredAtExactCount(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer ts.Close()

	fired := make(chan struct{}, 1)
	c, _ := New("exact-threshold", Config{
		HTTP:         &HTTP{URL: ts.URL},
		Period:       "100ms",
		Timeout:      "500ms",
		Threshold:    1,
		InitialDelay: "0s",
	}, func(_ string) {
		select {
		case fired <- struct{}{}:
		default:
		}
	}, nil)

	c.Run()
	defer c.Stop()

	// Original: fires after the first check (~0ms + goroutine startup).
	// Mutation (>threshold): fires only after the second check (~100ms later).
	// We wait up to 50ms — enough for the first check but not the second tick.
	select {
	case <-fired:
		// correct
	case <-time.After(50 * time.Millisecond):
		t.Error("callback should fire after exactly 1 failure (threshold=1); not fired within 50ms")
	}
}

// TestHTTP300IsFail covers M-24: HTTP status 300 is not a 2xx success and must
// be treated as a health-check failure.
func TestHTTP300IsFail(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(300)
	}))
	defer ts.Close()

	c, _ := New("http-300", Config{HTTP: &HTTP{URL: ts.URL}, Period: "1s", Timeout: "2s"}, nil, nil)
	if err := c.Execute(); err == nil {
		t.Error("expected failure for HTTP 300 (not a 2xx status)")
	}
}

// TestFailureCounterResetOnSuccess covers M-25: a successful check must reset
// the failure counter to 0, not merely decrement it.
//
// Sequence (threshold=2, period=20ms):
//
//	call 1 (F): failures=1
//	call 2 (F): failures=2 → callback fires (count=1), healthy=false
//	call 3 (S): failures=0 (reset) OR failures=1 (decrement mutation)
//	call 4 (F): failures=1 (reset) OR failures=2 (decrement → fires again, count=2)
//	calls 5+ (S): healthy reset
//
// After exactly 4 calls the original has 1 fire, the mutation has 2 fires.
// We sleep 70ms to cover ~3.5 periods (calls 1-4) and stop before call 5.
func TestFailureCounterResetOnSuccess(t *testing.T) {
	t.Parallel()
	var callN atomic.Int32
	var fired atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := int(callN.Add(1))
		switch {
		case n == 3: // one success after the initial two failures
			w.WriteHeader(200)
		case n >= 5: // successes after call 4 to prevent further fires in both cases
			w.WriteHeader(200)
		default:
			w.WriteHeader(500)
		}
	}))
	defer ts.Close()

	c, _ := New("counter-reset", Config{
		HTTP:         &HTTP{URL: ts.URL},
		Period:       "20ms",
		Timeout:      "500ms",
		Threshold:    2,
		InitialDelay: "0s",
	}, func(_ string) { fired.Add(1) }, nil)

	c.Run()
	defer c.Stop()

	// 70ms covers calls 1-4 (at t≈0,20,40,60ms). After that calls 5+ return 200.
	time.Sleep(70 * time.Millisecond)

	// With decrement mutation the callback fires twice (at calls 2 and 4).
	// With proper reset it fires only once (at call 2; call 4 leaves failures=1).
	if fired.Load() > 1 {
		t.Errorf("callback fired %d times; want ≤1 (failure counter must reset to 0 on success)", fired.Load())
	}
}

// TestWaitReadyImmediateCheck covers M-28: WaitReady must execute the check
// immediately before waiting for the first ticker tick. If the initial Execute()
// call is removed, a check with a long period stalls for one full period.
func TestWaitReadyImmediateCheck(t *testing.T) {
	t.Parallel()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200) // always healthy
	}))
	defer ts.Close()

	// period=2s: without the immediate Execute() the ticker fires after 2s, well past the 500ms ctx.
	c, _ := New("immediate", Config{HTTP: &HTTP{URL: ts.URL}, Period: "2s", Timeout: "500ms"}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := c.WaitReady(ctx); err != nil {
		t.Errorf("WaitReady should return immediately on first success, got: %v", err)
	}
}

// TestWaitReadyLargePeriodCapped covers M-27: WaitReady must cap its retry
// interval at 1 second regardless of the configured check period. Without the
// cap a service with period=2s would stall startup for 2s between retries.
func TestWaitReadyLargePeriodCapped(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 1 {
			w.WriteHeader(500) // first call fails
			return
		}
		w.WriteHeader(200)
	}))
	defer ts.Close()

	// period=2s: with capping at 1s, second check fires after ~1s → passes 1.5s timeout.
	// Without capping, second check fires after ~2s → fails 1.5s timeout.
	c, _ := New("capped", Config{HTTP: &HTTP{URL: ts.URL}, Period: "2s", Timeout: "500ms"}, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := c.WaitReady(ctx); err != nil {
		t.Errorf("WaitReady should succeed within 1.5s (period capped at 1s), got: %v", err)
	}
}
