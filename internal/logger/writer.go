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

// Package logger provides line-buffered prefix writers and log target forwarding.
package logger

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"time"
)

const defaultRingSize = 200

// maxBufSize is the maximum line buffer size before a forced flush.
// Prevents unbounded memory growth from output without newlines.
const maxBufSize = 1 << 20 // 1 MB

// slotRetainCap bounds the backing-array capacity a reused ring/subscriber slot
// keeps. Without it, one near-maxBufSize spike would pin that many bytes in
// every slot forever.
const slotRetainCap = 64 << 10 // 64 KiB

// subBufRingSize must exceed the maximum number of buffers that can be alive
// (referenced by anything other than the writer) at once. A buf is alive while
// queued in the sub channel (up to subChanCap), held by the receiver, or handed
// downstream without copying.
//
// gopherd's control LogsFn does a two-hop merge: sub channel (256) → pipe
// goroutine (1) → merged channel (256) → handler (1) = 514 alive max. Sized at
// 520 for slack so bufIdx never wraps onto an in-flight slot.
const (
	subChanCap     = 256
	subBufRingSize = 520
)

// DefaultPrefix is the default log prefix format: service name followed by timestamp.
const DefaultPrefix = "service timestamp"

// PrefixWriter is a line-buffered io.Writer that prefixes each line with
// configurable components (timestamp and/or service name):
//
//	[service-name] 2021-05-13T03:16:51.001Z output line here
//
// The prefix format is controlled by a space-separated token string:
//   - "service timestamp" (default): [name] then timestamp
//   - "timestamp service": timestamp then [name]
//   - "timestamp": timestamp only
//   - "service": [name] only
//   - "none" or "": no prefix
//
// Partial lines are buffered until a newline. Supports subscribers for live
// streaming and a ring buffer for recent history.
type PrefixWriter struct {
	dest         io.Writer
	name         string
	buf          []byte
	extra        []io.Writer // additional writers (log targets)
	ring         [][]byte    // circular buffer of recent prefixed lines
	subs         []*subscription
	parts        []string // parsed prefix components
	prefixBuf    []byte   // reusable buffer for building prefixed lines
	ringPos      int      // next write position in ring buffer
	prefixEstLen int      // estimated prefix length for capacity pre-allocation
	mu           sync.Mutex
	ringFull     bool // whether ring has wrapped around
}

// subscription holds a per-subscriber buffer ring so the fan-out path does not
// allocate per line. bufIdx advances only on a successful send, so dropped sends
// overwrite the same slot rather than poisoning a queued one.
type subscription struct {
	out    chan []byte
	bufs   [subBufRingSize][]byte
	bufIdx int
}

// NewPrefixWriter creates a new PrefixWriter. The prefix string controls which
// components appear and in what order. See DefaultPrefix for the default format.
func NewPrefixWriter(dest io.Writer, name, prefix string) *PrefixWriter {
	parts := parsePrefixParts(prefix)
	pw := &PrefixWriter{
		dest:  dest,
		name:  name,
		parts: parts,
		ring:  make([][]byte, defaultRingSize),
	}
	pw.prefixEstLen = estimatePrefixLen(parts, name) // for buffer pre-allocation
	return pw
}

func estimatePrefixLen(parts []string, name string) int {
	n := 0
	for _, p := range parts {
		switch p {
		case "timestamp":
			n += timestampLen
		case "service":
			n += len(name) + 3 // "[" + name + "] "
		}
	}
	return n
}

func parsePrefixParts(prefix string) []string {
	if prefix == "none" || prefix == "" {
		return nil
	}
	var parts []string
	for tok := range strings.FieldsSeq(prefix) {
		switch tok {
		case "timestamp", "service":
			parts = append(parts, tok)
		}
	}
	return parts
}

// AddTarget adds an additional writer that receives the same prefixed output.
func (pw *PrefixWriter) AddTarget(w io.Writer) {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	pw.extra = append(pw.extra, w)
}

// ClearTargets removes all additional writers registered via AddTarget, used
// during reload before re-wiring new ones. Nil (not extra[:0]) releases the
// backing array so stale writer references become GC-eligible immediately.
func (pw *PrefixWriter) ClearTargets() {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	pw.extra = nil
}

// Subscribe returns a channel that receives new prefixed log lines and an
// unsubscribe function. The channel is buffered to avoid blocking writes.
//
// Received slices alias a reused per-subscription buffer; consumers must
// consume each synchronously before the next receive. Holding a slice past the
// next receive risks observing torn writes once the writer reuses the slot.
func (pw *PrefixWriter) Subscribe() (<-chan []byte, func()) {
	s := &subscription{out: make(chan []byte, subChanCap)}
	pw.mu.Lock()
	pw.subs = append(pw.subs, s)
	pw.mu.Unlock()

	unsub := func() {
		pw.mu.Lock()
		defer pw.mu.Unlock()
		for i, sub := range pw.subs {
			if sub == s {
				pw.subs = append(pw.subs[:i], pw.subs[i+1:]...)
				close(s.out)
				return
			}
		}
	}
	return s.out, unsub
}

// Recent returns a deep copy of recent prefixed log lines from the ring buffer.
// Each returned slice is an independent copy so concurrent ring writes cannot
// overwrite the bytes that callers (e.g. the logs command handler) are reading.
func (pw *PrefixWriter) Recent() [][]byte {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	copySlot := func(slot []byte) []byte {
		cpy := make([]byte, len(slot))
		copy(cpy, slot)
		return cpy
	}

	if !pw.ringFull {
		out := make([][]byte, pw.ringPos)
		for i, slot := range pw.ring[:pw.ringPos] {
			out[i] = copySlot(slot)
		}
		return out
	}
	// Wrapped: ringPos..end then 0..ringPos yields oldest to newest.
	out := make([][]byte, defaultRingSize)
	n := 0
	for _, slot := range pw.ring[pw.ringPos:] {
		out[n] = copySlot(slot)
		n++
	}
	for _, slot := range pw.ring[:pw.ringPos] {
		out[n] = copySlot(slot)
		n++
	}
	return out
}

// Write implements io.Writer.
// Peak per-Write memory is bounded to ~maxBufSize: p is consumed in chunks that
// never grow pw.buf beyond the cap, and a synthetic newline is injected when the
// cap is reached with no natural newline, so a multi-megabyte newline-free chunk
// cannot allocate len(p) bytes before the size check fires.
func (pw *PrefixWriter) Write(p []byte) (int, error) {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	total := len(p)
	for len(p) > 0 {
		room := maxBufSize - len(pw.buf)
		if room <= 0 {
			// At the cap with no newline; inject one to force a drain.
			pw.buf = append(pw.buf, '\n')
		} else {
			take := min(room, len(p))
			// Stop at a natural newline so line boundaries stay aligned.
			if nl := bytes.IndexByte(p[:take], '\n'); nl >= 0 {
				take = nl + 1
			}
			pw.buf = append(pw.buf, p[:take]...)
			p = p[take:]
			// Filled the buffer without a newline; inject one so the drain
			// loop flushes rather than carrying an oversized fragment.
			if len(pw.buf) >= maxBufSize && bytes.IndexByte(pw.buf, '\n') < 0 {
				pw.buf = append(pw.buf, '\n')
			}
		}

		for {
			idx := bytes.IndexByte(pw.buf, '\n')
			if idx < 0 {
				break
			}
			line := pw.buf[:idx+1]
			rest := pw.buf[idx+1:]
			// Reset to the start of the backing array when possible so
			// append reuses capacity instead of allocating.
			if len(rest) == 0 {
				// Release a backing array inflated by a rare large burst so one
				// spike doesn't pin ~maxBufSize per stream (cf. slotRetainCap).
				if cap(pw.buf) > slotRetainCap {
					pw.buf = nil
				} else {
					pw.buf = pw.buf[:0]
				}
			} else {
				pw.buf = rest
			}

			prefixed := pw.prefix(line)
			// prefixed aliases pw.prefixBuf and is reused next iteration, so
			// every writer here must consume the bytes synchronously and not
			// retain the slice after returning.
			_, _ = pw.dest.Write(prefixed)
			for _, w := range pw.extra {
				_, _ = w.Write(prefixed)
			}

			// The ring feeds the control socket (Recent/Subscribe), which
			// writes to an operator's terminal — strip control chars here to
			// block ANSI/forged-line injection. The stdout passthrough above
			// stays raw to keep colored container logs.
			clean := sanitize(prefixed)
			slot := pw.ring[pw.ringPos]
			// Reuse the slot unless too small or an oversized leftover
			// (cap > slotRetainCap) a small line would pin; else realloc.
			if c := cap(slot); c >= len(clean) && c <= slotRetainCap {
				slot = slot[:len(clean)]
			} else {
				slot = make([]byte, len(clean))
			}
			copy(slot, clean)
			pw.ring[pw.ringPos] = slot
			pw.ringPos++
			if pw.ringPos >= defaultRingSize {
				pw.ringPos = 0
				pw.ringFull = true
			}

			for _, s := range pw.subs {
				buf := s.bufs[s.bufIdx]
				// Same reuse/shrink rule as the ring slot above.
				if c := cap(buf); c >= len(slot) && c <= slotRetainCap {
					buf = buf[:len(slot)]
				} else {
					buf = make([]byte, len(slot))
				}
				copy(buf, slot)
				s.bufs[s.bufIdx] = buf

				select {
				case s.out <- buf:
					s.bufIdx++
					if s.bufIdx >= subBufRingSize {
						s.bufIdx = 0
					}
				default:
					// subscriber too slow, drop line
				}
			}
		}
	}

	return total, nil
}

// Flush writes any remaining buffered content (for shutdown).
func (pw *PrefixWriter) Flush() {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	if len(pw.buf) > 0 {
		prefixed := pw.prefix(pw.buf)
		_, _ = pw.dest.Write(prefixed)
		for _, w := range pw.extra {
			_, _ = w.Write(prefixed)
		}
		pw.buf = nil
	}
}

// timestampLen is the byte length of "2006-01-02T15:04:05.000Z ".
const timestampLen = 25

func (pw *PrefixWriter) prefix(line []byte) []byte {
	if len(pw.parts) == 0 {
		return line
	}
	// Reuse pw.prefixBuf to avoid a per-line allocation.
	needed := pw.prefixEstLen + len(line)
	pw.prefixBuf = pw.prefixBuf[:0]
	if cap(pw.prefixBuf) < needed {
		pw.prefixBuf = make([]byte, 0, needed)
	}
	for _, p := range pw.parts {
		switch p {
		case "timestamp":
			pw.prefixBuf = time.Now().UTC().AppendFormat(pw.prefixBuf, "2006-01-02T15:04:05.000Z")
			pw.prefixBuf = append(pw.prefixBuf, ' ')
		case "service":
			pw.prefixBuf = append(pw.prefixBuf, '[')
			pw.prefixBuf = append(pw.prefixBuf, pw.name...)
			pw.prefixBuf = append(pw.prefixBuf, ']', ' ')
		}
	}
	pw.prefixBuf = append(pw.prefixBuf, line...)
	return pw.prefixBuf
}
