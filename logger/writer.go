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
// Partial lines are buffered until a newline is received.
// Supports subscribers for live log streaming and a ring buffer for recent history.
type PrefixWriter struct {
	dest         io.Writer
	name         string
	buf          []byte
	extra        []io.Writer // additional writers (log targets)
	ring         [][]byte    // circular ring buffer of recent prefixed lines
	subs         []chan []byte
	parts        []string // parsed prefix components
	prefixBuf    []byte   // reusable buffer for building prefixed lines
	ringPos      int      // next write position in ring buffer
	prefixEstLen int      // estimated prefix length for capacity pre-allocation
	mu           sync.Mutex
	ringFull     bool // whether ring has wrapped around
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
	// Estimate prefix length for buffer pre-allocation.
	pw.prefixEstLen = estimatePrefixLen(parts, name)
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

// Subscribe returns a channel that receives new prefixed log lines and an
// unsubscribe function. The channel is buffered to avoid blocking writes.
func (pw *PrefixWriter) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 256)
	pw.mu.Lock()
	pw.subs = append(pw.subs, ch)
	pw.mu.Unlock()

	unsub := func() {
		pw.mu.Lock()
		defer pw.mu.Unlock()
		for i, s := range pw.subs {
			if s == ch {
				pw.subs = append(pw.subs[:i], pw.subs[i+1:]...)
				close(ch)
				return
			}
		}
	}
	return ch, unsub
}

// Recent returns a copy of recent prefixed log lines from the ring buffer.
func (pw *PrefixWriter) Recent() [][]byte {
	pw.mu.Lock()
	defer pw.mu.Unlock()
	if !pw.ringFull {
		out := make([][]byte, pw.ringPos)
		copy(out, pw.ring[:pw.ringPos])
		return out
	}
	// Ring has wrapped: return from ringPos..end, then 0..ringPos (oldest to newest).
	out := make([][]byte, defaultRingSize)
	n := copy(out, pw.ring[pw.ringPos:])
	copy(out[n:], pw.ring[:pw.ringPos])
	return out
}

// Write implements io.Writer.
func (pw *PrefixWriter) Write(p []byte) (int, error) {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	total := len(p)
	pw.buf = append(pw.buf, p...)

	// If buffer exceeds max size without a newline, force a flush to prevent
	// unbounded memory growth from binary or malicious output.
	if len(pw.buf) > maxBufSize && !bytes.Contains(pw.buf, []byte{'\n'}) {
		pw.buf = append(pw.buf, '\n')
	}

	for {
		idx := bytes.IndexByte(pw.buf, '\n')
		if idx < 0 {
			break
		}
		line := pw.buf[:idx+1]
		pw.buf = pw.buf[idx+1:]

		prefixed := pw.prefix(line)
		_, _ = pw.dest.Write(prefixed)
		for _, w := range pw.extra {
			_, _ = w.Write(prefixed)
		}

		// Store in circular ring buffer (no slice shifting).
		lineCopy := make([]byte, len(prefixed))
		copy(lineCopy, prefixed)
		pw.ring[pw.ringPos] = lineCopy
		pw.ringPos++
		if pw.ringPos >= defaultRingSize {
			pw.ringPos = 0
			pw.ringFull = true
		}

		// Fan out to subscribers (non-blocking).
		for _, ch := range pw.subs {
			select {
			case ch <- lineCopy:
			default:
				// subscriber too slow, drop line
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
	// Reuse pw.prefixBuf to avoid allocating a new bytes.Buffer per line.
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
