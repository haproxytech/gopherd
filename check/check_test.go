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

// TestInitialHealthyState verifies that a newly created Checker starts
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

// TestThresholdFiredAtExactCount verifies that the failure callback fires
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

// TestHTTP300IsFail verifies that HTTP status 300 is not a 2xx success and must
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

// TestFailureCounterResetOnSuccess verifies that a successful check resets
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

// TestWaitReadyImmediateCheck verifies that WaitReady executes the check
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

// TestWaitReadyLargePeriodCapped verifies that WaitReady caps its retry
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

// TestTCPPortZero verifies that a TCP check with port 0 (which results from a
// mis-parsed or missing port value in config) is rejected at check creation
// time with a clear error rather than silently producing a broken check.
func TestTCPPortZero(t *testing.T) {
	t.Parallel()
	_, err := New("bad-port", Config{TCP: &TCP{Host: "localhost", Port: 0}}, nil, nil)
	if err == nil {
		t.Error("expected error for TCP port 0")
	}
}

// TestTCPPortNegative verifies that a negative TCP port is also rejected.
func TestTCPPortNegative(t *testing.T) {
	t.Parallel()
	_, err := New("neg-port", Config{TCP: &TCP{Host: "localhost", Port: -1}}, nil, nil)
	if err == nil {
		t.Error("expected error for negative TCP port")
	}
}

// TestTCPPortAboveMax verifies that port 65536 is rejected.
func TestTCPPortAboveMax(t *testing.T) {
	t.Parallel()
	_, err := New("over-port", Config{TCP: &TCP{Host: "localhost", Port: 65536}}, nil, nil)
	if err == nil {
		t.Error("expected error for TCP port 65536")
	}
}

// TestStopSetsStopped verifies that Stop() sets the stopped atomic flag so that
// any in-flight onFailureFn invocation can detect the checker was stopped and
// avoid a stale callback after the checker has been shut down (B3).
func TestStopSetsStopped(t *testing.T) {
	t.Parallel()
	c, err := New("stop-flag", Config{TCP: &TCP{Port: 80}}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.stopped.Load() {
		t.Error("stopped should be false before Stop()")
	}
	c.Stop()
	if !c.stopped.Load() {
		t.Error("stopped should be true after Stop()")
	}
}

// trackingTransport is an http.RoundTripper that records CloseIdleConnections calls.
type trackingTransport struct {
	http.RoundTripper
	closed atomic.Bool
}

func (t *trackingTransport) CloseIdleConnections() {
	t.closed.Store(true)
}

// TestStopClosesHTTPIdleConnections covers O3: Stop() must call
// CloseIdleConnections() on the HTTP client so that pooled connections and their
// associated goroutines are released when the checker shuts down.
func TestStopClosesHTTPIdleConnections(t *testing.T) {
	t.Parallel()
	c, err := New("http-idle", Config{
		HTTP:    &HTTP{URL: "http://localhost"},
		Period:  "5s",
		Timeout: "1s",
	}, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Inject a tracking transport so we can observe CloseIdleConnections.
	tr := &trackingTransport{RoundTripper: http.DefaultTransport}
	c.httpClient = &http.Client{Transport: tr}

	c.Stop()

	if !tr.closed.Load() {
		t.Error("Stop() did not call CloseIdleConnections() on the HTTP client transport")
	}
}

// BenchmarkExecuteHTTPSuccess measures the per-check allocation of a successful
// HTTP probe against a loopback server. The current implementation does
// req.WithContext(ctx) per call (shallow Request copy) — this benchmark
// quantifies that alloc cost and any others on the success path.
func BenchmarkExecuteHTTPSuccess(b *testing.B) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()

	c, err := New("bench", Config{HTTP: &HTTP{URL: ts.URL}, Period: "1s", Timeout: "5s"}, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Stop()

	if err := c.Execute(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if err := c.Execute(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkExecuteTCPSuccess measures the per-check allocation of a successful
// TCP dial against a loopback listener.
func BenchmarkExecuteTCPSuccess(b *testing.B) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	var portN int
	fmt.Sscanf(port, "%d", &portN)
	c, err := New("bench", Config{TCP: &TCP{Host: host, Port: portN}, Period: "1s", Timeout: "5s"}, nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer c.Stop()

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		if err := c.Execute(); err != nil {
			b.Fatal(err)
		}
	}
}
