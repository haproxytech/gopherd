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
	"strconv"
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

// TestSubscribeSlowConsumerNoCorruption stresses the per-subscription buffer
// ring with a deliberately slow consumer (simulates a stalled `logs -f`).
// Under the previous alloc-per-line design the consumer always saw immutable
// copies; the new design reuses bufs, so a too-small ring would let the writer
// overwrite a buf the consumer still references. The test asserts that every
// observed line still parses as a well-formed sequenced line, with the race
// detector active.
func TestSubscribeSlowConsumerNoCorruption(t *testing.T) {
	t.Parallel()
	pw := NewPrefixWriter(io.Discard, "svc", "none")

	ch, unsub := pw.Subscribe()
	defer unsub()

	done := make(chan struct{})
	var received [][]byte
	go func() {
		defer close(done)
		for line := range ch {
			cp := make([]byte, len(line))
			copy(cp, line)
			received = append(received, cp)
		}
	}()

	// Wrap the buf ring multiple times under back-pressure.
	const writes = subBufRingSize * 4
	for i := range writes {
		fmt.Fprintf(pw, "line-%06d\n", i)
	}
	unsub()
	<-done

	// Shape alone is not enough: a buffer overwritten while queued is replaced
	// by another well-formed line. The give-away is a repeated or out-of-order
	// sequence number — the same buffer handed over twice.
	seen := make(map[int]bool, len(received))
	prev := -1
	for i, line := range received {
		s := strings.TrimSuffix(string(line), "\n")
		if !strings.HasPrefix(s, "line-") || len(s) != len("line-000000") {
			t.Fatalf("received[%d] = %q; corrupted line (writer overwrote a buf still in-flight)", i, s)
		}
		n, err := strconv.Atoi(strings.TrimPrefix(s, "line-"))
		if err != nil {
			t.Fatalf("received[%d] = %q: unparseable sequence number", i, s)
		}
		if seen[n] {
			t.Fatalf("received[%d] = %q: line delivered twice — the writer reused a "+
				"buffer that was still queued for the subscriber", i, s)
		}
		seen[n] = true
		if n <= prev {
			t.Fatalf("received[%d] = %q arrived after line-%06d; queued buffers must "+
				"not be overwritten out of order", i, s, prev)
		}
		prev = n
	}
	if len(received) == 0 {
		t.Fatal("subscriber received nothing")
	}
}

// TestRecentReturnsIndependentCopies pins that Recent hands out deep copies.
// The ring reuses its slots, so returning them lets the writer rewrite bytes
// the control-socket handler is still formatting: torn `gopherd logs` output,
// and a data race. Re-reading the returned slices after the ring has moved on
// is what exposes the aliasing.
func TestRecentReturnsIndependentCopies(t *testing.T) {
	t.Parallel()
	pw := NewPrefixWriter(io.Discard, "svc", "none")

	// Originals long, overwrites short: the ring reuses a slot in place only
	// when the new line fits the old capacity, which is when aliasing shows.
	const pad = "-padding-padding-padding-padding-padding-padding"
	for i := range 5 {
		fmt.Fprintf(pw, "original-%02d%s\n", i, pad)
	}
	snap := pw.Recent()
	if len(snap) != 5 {
		t.Fatalf("Recent() returned %d lines, want 5", len(snap))
	}
	before := make([]string, len(snap))
	for i, line := range snap {
		before[i] = string(line)
	}

	// Fill the ring several times over so every slot is reused.
	for i := range defaultRingSize * 3 {
		fmt.Fprintf(pw, "x-%04d\n", i)
	}

	for i, line := range snap {
		if got := string(line); got != before[i] {
			t.Errorf("Recent()[%d] changed from %q to %q after later writes; "+
				"Recent must copy each slot, not alias the ring", i, before[i], got)
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

// TestRecentSanitizesControlChars verifies child control chars are stripped
// from the control-socket buffers (Recent/Subscribe) but left raw on the
// stdout passthrough, so colored container logs survive.
func TestRecentSanitizesControlChars(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(&buf, "svc", "none")

	// BEL (0x07), CR (0x0d) and cursor motion are injection bytes; SGR colors
	// are harmless and must survive for `logs` streaming.
	pw.Write([]byte("ok\x1b[31mRED\x1b[2A\x07\rEVIL\n"))

	recent := pw.Recent()
	if len(recent) != 1 {
		t.Fatalf("expected 1 recent line, got %d", len(recent))
	}
	if bytes.ContainsAny(recent[0], "\x07\r") || bytes.Contains(recent[0], []byte("\x1b[2A")) {
		t.Errorf("Recent() retained injection bytes: %q", recent[0])
	}
	if !bytes.Contains(recent[0], []byte("\x1b[31mRED")) || !bytes.Contains(recent[0], []byte("EVIL")) {
		t.Errorf("Recent() should keep text and SGR colors: %q", recent[0])
	}

	// A subscriber must also receive sanitized bytes.
	pwSub := NewPrefixWriter(io.Discard, "svc", "none")
	ch, unsub := pwSub.Subscribe()
	defer unsub()
	pwSub.Write([]byte("clean\x1b]0;evil\x07EVIL\n"))
	select {
	case line := <-ch:
		if bytes.IndexByte(line, 0x1b) >= 0 || bytes.IndexByte(line, 0x07) >= 0 {
			t.Errorf("subscriber retained OSC bytes: %q", line)
		}
	case <-time.After(time.Second):
		t.Error("subscriber did not receive line")
	}

	// stdout/stderr passthrough must remain raw (colors preserved).
	if !bytes.Contains(buf.Bytes(), []byte("\x1b[31m")) {
		t.Errorf("stdout passthrough should retain raw bytes: %q", buf.Bytes())
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

// TestRecentRingOverflowPreservesOrder verifies that when the ring has wrapped,
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

// TestFlushIdempotent verifies that after Flush(), the internal buffer is
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

// TestFlushWritesToExtraTarget covers the bug where Flush() used pw.dest instead
// of w in the extra-writers loop, so log targets never received flushed content.
func TestFlushWritesToExtraTarget(t *testing.T) {
	t.Parallel()
	var dest, extra bytes.Buffer
	pw := NewPrefixWriter(&dest, "svc", "none")
	pw.AddTarget(&extra)
	pw.Write([]byte("partial")) // no newline → buffered, not written yet
	if dest.Len() != 0 || extra.Len() != 0 {
		t.Fatal("content should be buffered before flush")
	}
	pw.Flush()
	if !strings.Contains(extra.String(), "partial") {
		t.Errorf("Flush must write to extra targets; extra got: %q", extra.String())
	}
}

// TestFlushNoDuplicateDestWrites covers the duplicate-write side effect: when
// there are N extra targets, the old code wrote to pw.dest N+1 times instead of once.
func TestFlushNoDuplicateDestWrites(t *testing.T) {
	t.Parallel()
	var dest bytes.Buffer
	extra1, extra2 := &bytes.Buffer{}, &bytes.Buffer{}
	pw := NewPrefixWriter(&dest, "svc", "none")
	pw.AddTarget(extra1)
	pw.AddTarget(extra2)
	pw.Write([]byte("unique")) // no newline → buffered
	pw.Flush()
	if count := strings.Count(dest.String(), "unique"); count != 1 {
		t.Errorf("dest must be written exactly once; got %d copies (bug: pw.dest used instead of w in loop)", count)
	}
}

// TestClearTargetsReleasesBackingArray verifies that ClearTargets() sets extra
// to nil (not extra[:0]) so the backing array and stale writer references are
// eligible for garbage collection (B4).
func TestClearTargetsReleasesBackingArray(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	pw := NewPrefixWriter(io.Discard, "svc", "none")
	pw.AddTarget(&buf)
	pw.ClearTargets()

	pw.mu.Lock()
	extra := pw.extra
	pw.mu.Unlock()
	if extra != nil {
		t.Error("ClearTargets should set extra to nil, not extra[:0]; stale GC references remain")
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

// BenchmarkWriteWithSubscriber drives a steady consumer that drains the
// subscription channel as fast as it is filled. The per-subscription buffer
// ring should reach steady state and produce zero allocations per line.
func BenchmarkWriteWithSubscriber(b *testing.B) {
	pw := NewPrefixWriter(io.Discard, "my-service", "service timestamp")
	line := []byte("2026-04-06 some log output from the application\n")

	ch, unsub := pw.Subscribe()
	defer unsub()

	done := make(chan struct{})
	go func() {
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					return
				}
			case <-done:
				return
			}
		}
	}()

	// Warm the ring buffer and the per-subscription bufs to steady state.
	for range subBufRingSize * 2 {
		pw.Write(line)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		pw.Write(line)
	}
	b.StopTimer()
	close(done)
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

// TestRingSlotCapacityReclaimed verifies a one-off huge line's oversized slot is
// reclaimed once normal lines cycle the ring: no slot exceeds slotRetainCap.
func TestRingSlotCapacityReclaimed(t *testing.T) {
	t.Parallel()
	pw := NewPrefixWriter(io.Discard, "svc", "none")
	// One huge line (> slotRetainCap, < maxBufSize) inflates a single slot.
	huge := append(bytes.Repeat([]byte("x"), 128<<10), '\n')
	if _, err := pw.Write(huge); err != nil {
		t.Fatal(err)
	}
	// Cycle the whole ring with small lines so every slot is reused at least once.
	for range defaultRingSize + 2 {
		if _, err := pw.Write([]byte("small\n")); err != nil {
			t.Fatal(err)
		}
	}
	pw.mu.Lock()
	defer pw.mu.Unlock()
	for i, slot := range pw.ring {
		if cap(slot) > slotRetainCap {
			t.Errorf("ring slot %d retained cap %d > slotRetainCap %d after a spike", i, cap(slot), slotRetainCap)
		}
	}
}

// TestLineBufCapacityReclaimed verifies the line buffer's oversized backing
// array (grown by a one-off large burst) is released once the buffer drains, so
// a single spike does not pin ~maxBufSize per stream for the process lifetime.
func TestLineBufCapacityReclaimed(t *testing.T) {
	t.Parallel()
	pw := NewPrefixWriter(io.Discard, "svc", "none")
	// A huge line (> slotRetainCap, < maxBufSize) grows pw.buf's backing array.
	huge := append(bytes.Repeat([]byte("x"), 128<<10), '\n')
	if _, err := pw.Write(huge); err != nil {
		t.Fatal(err)
	}
	// A small terminated line drains the buffer fully.
	if _, err := pw.Write([]byte("small\n")); err != nil {
		t.Fatal(err)
	}
	pw.mu.Lock()
	defer pw.mu.Unlock()
	if cap(pw.buf) > slotRetainCap {
		t.Errorf("line buffer retained cap %d > slotRetainCap %d after a spike", cap(pw.buf), slotRetainCap)
	}
}
