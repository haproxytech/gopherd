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

package sdnotify

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// dialAndSend sends a single datagram payload to the listener path. The
// test client is a plain unix datagram socket matching how sd_notify(3)
// writes: one datagram, abstract destination.
func dialAndSend(t *testing.T, path, payload string) {
	t.Helper()
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Net: "unixgram", Name: path})
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	defer c.Close()
	if _, err := c.Write([]byte(payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestListenerPathFormat(t *testing.T) {
	t.Parallel()
	n, err := Listen("svc", 42, os.Getuid())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer n.Close()
	if !strings.HasPrefix(n.Path(), "@gopherd-sd-notify-42-svc") {
		t.Errorf("Path = %q, want @gopherd-sd-notify-42-svc prefix", n.Path())
	}
}

func TestListenerReady(t *testing.T) {
	t.Parallel()
	n, err := Listen("ready", os.Getpid(), os.Getuid())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer n.Close()
	if n.Ready() {
		t.Fatal("Ready should be false before any datagram")
	}
	dialAndSend(t, n.Path(), "READY=1\n")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := n.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	if !n.Ready() {
		t.Error("Ready should be true after READY=1")
	}
}

func TestListenerReadyIdempotent(t *testing.T) {
	t.Parallel()
	// Second READY=1 must not panic (closing an already-closed channel
	// would). The atomic CompareAndSwap in parse() guards this.
	n, err := Listen("ready-idem", os.Getpid(), os.Getuid())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer n.Close()
	dialAndSend(t, n.Path(), "READY=1\n")
	dialAndSend(t, n.Path(), "READY=1\n")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := n.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestListenerStopping(t *testing.T) {
	t.Parallel()
	n, err := Listen("stopping", os.Getpid(), os.Getuid())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer n.Close()
	dialAndSend(t, n.Path(), "READY=1\nSTOPPING=1\n")
	// Poll briefly for the read goroutine to process both records.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !n.Stopping() {
		time.Sleep(5 * time.Millisecond)
	}
	if !n.Stopping() {
		t.Error("Stopping should be true after STOPPING=1")
	}
	if !n.Ready() {
		t.Error("Ready should be true after READY=1 (sent alongside STOPPING)")
	}
}

func TestListenerStatus(t *testing.T) {
	t.Parallel()
	n, err := Listen("status", os.Getpid(), os.Getuid())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer n.Close()
	dialAndSend(t, n.Path(), "STATUS=hello world\n")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && n.Status() == "" {
		time.Sleep(5 * time.Millisecond)
	}
	if got := n.Status(); got != "hello world" {
		t.Errorf("Status = %q, want %q", got, "hello world")
	}
}

func TestListenerWaitReadyContextCancel(t *testing.T) {
	t.Parallel()
	n, err := Listen("ctx", os.Getpid(), os.Getuid())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer n.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := n.WaitReady(ctx); err == nil {
		t.Fatal("WaitReady should return error on context cancel")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("WaitReady took %v, should return promptly on ctx cancel", elapsed)
	}
}

func TestListenerCloseIdempotent(t *testing.T) {
	t.Parallel()
	n, err := Listen("close", os.Getpid(), os.Getuid())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := n.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestListenerIgnoresUnknownKeys(t *testing.T) {
	t.Parallel()
	n, err := Listen("unknown", os.Getpid(), os.Getuid())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer n.Close()
	// MAINPID and WATCHDOG should not trip Ready/Stopping flags.
	dialAndSend(t, n.Path(), "MAINPID=123\nWATCHDOG=1\nBUSERROR=foo\n")
	time.Sleep(50 * time.Millisecond)
	if n.Ready() {
		t.Error("unknown keys should not set Ready")
	}
	if n.Stopping() {
		t.Error("unknown keys should not set Stopping")
	}
}

func TestListenerMalformedLines(t *testing.T) {
	t.Parallel()
	n, err := Listen("malformed", os.Getpid(), os.Getuid())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer n.Close()
	// Lines without '=' (or with leading '=') must not panic or set flags.
	dialAndSend(t, n.Path(), "no-equals-sign\n=leading-equals\n\nREADY=1\n")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := n.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady after mixed-payload: %v", err)
	}
}
