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

// Default prefix format: timestamp followed by service name.
const DefaultPrefix = "timestamp service"

// PrefixWriter is a line-buffered io.Writer that prefixes each line with
// configurable components (timestamp and/or service name):
//
//	2021-05-13T03:16:51.001Z [service-name] output line here
//
// The prefix format is controlled by a space-separated token string:
//   - "timestamp service" (default): timestamp then [name]
//   - "service timestamp": [name] then timestamp
//   - "timestamp": timestamp only
//   - "service": [name] only
//   - "none" or "": no prefix
//
// Partial lines are buffered until a newline is received.
// Supports subscribers for live log streaming and a ring buffer for recent history.
type PrefixWriter struct {
	dest   io.Writer
	name   string
	buf    []byte
	extra  []io.Writer // additional writers (log targets)
	ring   [][]byte    // ring buffer of recent prefixed lines
	subs   []chan []byte
	mu     sync.Mutex
	parts  []string // parsed prefix components
}

// NewPrefixWriter creates a new PrefixWriter. The prefix string controls which
// components appear and in what order. See DefaultPrefix for the default format.
func NewPrefixWriter(dest io.Writer, name, prefix string) *PrefixWriter {
	return &PrefixWriter{
		dest:  dest,
		name:  name,
		parts: parsePrefixParts(prefix),
	}
}

func parsePrefixParts(prefix string) []string {
	if prefix == "none" || prefix == "" {
		return nil
	}
	var parts []string
	for _, tok := range strings.Fields(prefix) {
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
	out := make([][]byte, len(pw.ring))
	copy(out, pw.ring)
	return out
}

// Write implements io.Writer.
func (pw *PrefixWriter) Write(p []byte) (int, error) {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	total := len(p)
	pw.buf = append(pw.buf, p...)

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

		// Store in ring buffer.
		lineCopy := make([]byte, len(prefixed))
		copy(lineCopy, prefixed)
		if len(pw.ring) >= defaultRingSize {
			pw.ring = pw.ring[1:]
		}
		pw.ring = append(pw.ring, lineCopy)

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

func (pw *PrefixWriter) prefix(line []byte) []byte {
	if len(pw.parts) == 0 {
		return line
	}
	var buf bytes.Buffer
	for _, p := range pw.parts {
		switch p {
		case "timestamp":
			buf.WriteString(time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
			buf.WriteByte(' ')
		case "service":
			buf.WriteByte('[')
			buf.WriteString(pw.name)
			buf.WriteString("] ")
		}
	}
	buf.Write(line)
	return buf.Bytes()
}
