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
