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

package logger

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestSingleLine(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "service")
	pw.Write([]byte("hello\n"))
	if !strings.Contains(buf.String(), "[svc] hello\n") {
		t.Errorf("unexpected output: %q", buf.String())
	}
}

func TestNoTime(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "test", "service")
	pw.Write([]byte("line\n"))
	if strings.Contains(buf.String(), "Z") {
		t.Errorf("expected no timestamp: %q", buf.String())
	}
}

func TestMultipleLines(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "service")
	pw.Write([]byte("line1\nline2\n"))
	if strings.Count(buf.String(), "[svc]") != 2 {
		t.Errorf("expected 2 prefixes: %q", buf.String())
	}
}

func TestPartialLines(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "service")
	pw.Write([]byte("par"))
	if buf.Len() != 0 {
		t.Error("partial line should be buffered")
	}
	pw.Write([]byte("tial\n"))
	if !strings.Contains(buf.String(), "partial") {
		t.Errorf("unexpected: %q", buf.String())
	}
}

func TestFlush(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "service")
	pw.Write([]byte("no newline"))
	if buf.Len() != 0 {
		t.Error("should be buffered")
	}
	pw.Flush()
	if !strings.Contains(buf.String(), "no newline") {
		t.Errorf("flush should write: %q", buf.String())
	}
}

func TestExtraTarget(t *testing.T) {
	t.Parallel()
	var buf1, buf2 bytes.Buffer
	pw := NewPrefixWriter(&buf1, "svc", "service")
	pw.AddTarget(&buf2)
	pw.Write([]byte("hello\n"))
	if !strings.Contains(buf2.String(), "hello") {
		t.Errorf("extra target missing: %q", buf2.String())
	}
}

func TestSubscribe(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "service")

	ch, unsub := pw.Subscribe()
	defer unsub()

	pw.Write([]byte("hello\n"))

	select {
	case line := <-ch:
		if !strings.Contains(string(line), "hello") {
			t.Errorf("subscriber got: %q", line)
		}
	case <-time.After(time.Second):
		t.Error("subscriber did not receive line")
	}
}

func TestSubscribeMultiple(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "service")

	ch1, unsub1 := pw.Subscribe()
	ch2, unsub2 := pw.Subscribe()
	defer unsub1()
	defer unsub2()

	pw.Write([]byte("msg\n"))

	for _, ch := range []<-chan []byte{ch1, ch2} {
		select {
		case line := <-ch:
			if !strings.Contains(string(line), "msg") {
				t.Errorf("got: %q", line)
			}
		case <-time.After(time.Second):
			t.Error("subscriber timeout")
		}
	}
}

func TestUnsubscribe(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "service")

	ch, unsub := pw.Subscribe()
	unsub()

	// Channel should be closed after unsubscribe.
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed")
	}

	// Writing after unsubscribe should not panic.
	pw.Write([]byte("after unsub\n"))
}

func TestRecent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "service")

	pw.Write([]byte("line1\nline2\nline3\n"))

	recent := pw.Recent()
	if len(recent) != 3 {
		t.Fatalf("expected 3 recent lines, got %d", len(recent))
	}
	if !strings.Contains(string(recent[0]), "line1") {
		t.Errorf("recent[0] = %q", recent[0])
	}
}

func TestRecentRingOverflow(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "service")

	// Write more than defaultRingSize lines.
	for i := range defaultRingSize + 50 {
		pw.Write([]byte(strings.Repeat("x", 10) + "\n"))
		_ = i
	}

	recent := pw.Recent()
	if len(recent) != defaultRingSize {
		t.Errorf("expected %d recent lines, got %d", defaultRingSize, len(recent))
	}
}

// TestRecentRingOverflowPreservesOrder covers M-46: when the ring has wrapped,
// Recent() must return lines in chronological order (oldest→newest), not reversed.
func TestRecentRingOverflowPreservesOrder(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "none")

	// Write defaultRingSize+10 distinct lines so the ring wraps by 10.
	// Lines are numbered 0..defaultRingSize+9; after wrap the ring holds
	// lines 10..defaultRingSize+9 (oldest=10, newest=defaultRingSize+9).
	total := defaultRingSize + 10
	for i := range total {
		fmt.Fprintf(pw, "line-%04d\n", i)
	}

	recent := pw.Recent()
	if len(recent) != defaultRingSize {
		t.Fatalf("expected %d lines, got %d", defaultRingSize, len(recent))
	}

	// First entry must be the oldest surviving line (line-0010).
	first := strings.TrimSpace(string(recent[0]))
	if first != "line-0010" {
		t.Errorf("Recent()[0] = %q; want line-0010 (oldest, not newest)", first)
	}

	// Last entry must be the newest line.
	last := strings.TrimSpace(string(recent[defaultRingSize-1]))
	wantLast := fmt.Sprintf("line-%04d", total-1)
	if last != wantLast {
		t.Errorf("Recent()[%d] = %q; want %s (newest)", defaultRingSize-1, last, wantLast)
	}
}

// TestFlushIdempotent covers M-49: after Flush(), the internal buffer must be
// cleared so a second Flush() does not duplicate the partial line.
func TestFlushIdempotent(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "none")
	pw.Write([]byte("partial"))
	pw.Flush()
	pw.Flush() // second flush must be a no-op

	// "partial" must appear exactly once in the output.
	count := strings.Count(buf.String(), "partial")
	if count != 1 {
		t.Errorf("'partial' appears %d times after double Flush; want exactly 1", count)
	}
}

func TestPrefixNone(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "none")
	pw.Write([]byte("raw line\n"))
	if buf.String() != "raw line\n" {
		t.Errorf("expected raw output, got: %q", buf.String())
	}
}

func TestPrefixEmpty(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "")
	pw.Write([]byte("raw line\n"))
	if buf.String() != "raw line\n" {
		t.Errorf("expected raw output, got: %q", buf.String())
	}
}

func TestPrefixTimestampOnly(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "timestamp")
	pw.Write([]byte("hello\n"))
	got := buf.String()
	if !strings.Contains(got, "Z hello\n") {
		t.Errorf("expected timestamp prefix only, got: %q", got)
	}
	if strings.Contains(got, "[svc]") {
		t.Errorf("should not contain service name: %q", got)
	}
}

func TestPrefixServiceOnly(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "service")
	pw.Write([]byte("hello\n"))
	got := buf.String()
	if got != "[svc] hello\n" {
		t.Errorf("expected service prefix only, got: %q", got)
	}
}

func TestPrefixTimestampService(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "timestamp service")
	pw.Write([]byte("hello\n"))
	got := buf.String()
	// Should have timestamp before [svc].
	svcIdx := strings.Index(got, "[svc]")
	zIdx := strings.Index(got, "Z")
	if svcIdx < 0 || zIdx < 0 || zIdx >= svcIdx {
		t.Errorf("expected timestamp before service, got: %q", got)
	}
}

func TestPrefixServiceTimestamp(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "service timestamp")
	pw.Write([]byte("hello\n"))
	got := buf.String()
	// Should have [svc] before timestamp.
	svcIdx := strings.Index(got, "[svc]")
	zIdx := strings.Index(got, "Z")
	if svcIdx < 0 || zIdx < 0 || svcIdx >= zIdx {
		t.Errorf("expected service before timestamp, got: %q", got)
	}
}

func TestPrefixDefaultIsServiceTimestamp(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", DefaultPrefix)
	pw.Write([]byte("hello\n"))
	got := buf.String()
	svcIdx := strings.Index(got, "[svc]")
	zIdx := strings.Index(got, "Z")
	if svcIdx < 0 || zIdx < 0 || svcIdx >= zIdx {
		t.Errorf("default should be service timestamp, got: %q", got)
	}
}

func TestPrefixMultipleLines(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "none")
	pw.Write([]byte("line1\nline2\n"))
	if buf.String() != "line1\nline2\n" {
		t.Errorf("expected raw output, got: %q", buf.String())
	}
}

func BenchmarkWriteServiceTimestamp(b *testing.B) {
	pw := NewPrefixWriter(io.Discard, "my-service", "service timestamp")
	line := []byte("2026-04-06 some log output from the application\n")
	// Warm the ring buffer so slot reuse kicks in.
	for range defaultRingSize {
		pw.Write(line)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		pw.Write(line)
	}
}

func BenchmarkWriteServiceTimestampCold(b *testing.B) {
	pw := NewPrefixWriter(io.Discard, "my-service", "service timestamp")
	line := []byte("2026-04-06 some log output from the application\n")
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		pw.Write(line)
	}
}

func BenchmarkWriteServiceOnly(b *testing.B) {
	pw := NewPrefixWriter(io.Discard, "my-service", "service")
	line := []byte("2026-04-06 some log output from the application\n")
	for range defaultRingSize {
		pw.Write(line)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		pw.Write(line)
	}
}

func BenchmarkWriteNone(b *testing.B) {
	pw := NewPrefixWriter(io.Discard, "my-service", "none")
	line := []byte("2026-04-06 some log output from the application\n")
	for range defaultRingSize {
		pw.Write(line)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		pw.Write(line)
	}
}
