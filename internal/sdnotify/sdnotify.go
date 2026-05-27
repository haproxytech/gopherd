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

// Package sdnotify implements a subset of the systemd sd_notify readiness
// protocol on the gopherd side: it listens on a SOCK_DGRAM unix socket and
// exposes Ready/Stopping/Status signals parsed from newline-separated
// key=value records. The socket is bound in the Linux abstract namespace so
// there is no filesystem to clean up and rootless permission issues are
// avoided. Any process in the same network namespace (i.e. the container)
// can send to the socket, which matches the intended single-container
// deployment model.
package sdnotify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
)

// readBufSize is the datagram read buffer. systemd uses 4096; STATUS
// messages that exceed this are truncated.
const readBufSize = 4096

// Listener owns a single abstract unix datagram socket and tracks state
// reported via sd_notify-style records. Safe for concurrent callers.
type Listener struct {
	conn    *net.UnixConn
	readyCh chan struct{}
	status  atomic.Value // string
	name    string
	path    string

	ready    atomic.Bool
	stopping atomic.Bool
	closed   atomic.Bool
	wg       sync.WaitGroup
}

// Listen creates a datagram socket at @gopherd-sd-notify-<pid>-<name> and
// begins reading. The name should be a stable service identifier; pid
// disambiguates concurrent gopherd instances in the same container.
func Listen(name string, pid int) (*Listener, error) {
	path := fmt.Sprintf("@gopherd-sd-notify-%d-%s", pid, name)
	addr := &net.UnixAddr{Net: "unixgram", Name: path}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		return nil, fmt.Errorf("sd_notify listen %s: %w", name, err)
	}
	n := &Listener{
		conn:    conn,
		name:    name,
		path:    path,
		readyCh: make(chan struct{}),
	}
	n.status.Store("")
	n.wg.Add(1)
	go n.readLoop()
	return n, nil
}

// Path is the value to place in $NOTIFY_SOCKET for the child process.
func (n *Listener) Path() string { return n.path }

// Ready reports whether a "READY=1" record has been received.
func (n *Listener) Ready() bool { return n.ready.Load() }

// Stopping reports whether a "STOPPING=1" record has been received.
func (n *Listener) Stopping() bool { return n.stopping.Load() }

// Status returns the last STATUS= value, or "".
func (n *Listener) Status() string {
	v := n.status.Load()
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

// WaitReady blocks until READY=1 arrives or ctx is done. Safe to call
// multiple times; subsequent calls return immediately after the first
// READY=1 has been seen.
func (n *Listener) WaitReady(ctx context.Context) error {
	select {
	case <-n.readyCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close shuts down the listener. Idempotent. After Close the socket name
// is released and a subsequent Listen with the same name will succeed.
func (n *Listener) Close() error {
	if !n.closed.CompareAndSwap(false, true) {
		return nil
	}
	err := n.conn.Close()
	n.wg.Wait()
	return err
}

func (n *Listener) readLoop() {
	defer n.wg.Done()
	buf := make([]byte, readBufSize)
	for {
		nb, _, err := n.conn.ReadFromUnix(buf)
		if err != nil {
			if n.closed.Load() {
				return
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			log.Printf("sd_notify %s: read: %v", n.name, err)
			return
		}
		n.parse(buf[:nb])
	}
}

// parse processes a single datagram. Records are separated by \n; each is
// of the form KEY=VALUE. Unknown keys are ignored.
func (n *Listener) parse(data []byte) {
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		eq := bytes.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := string(line[:eq])
		val := string(line[eq+1:])
		switch key {
		case "READY":
			if val == "1" && !n.ready.Swap(true) {
				close(n.readyCh)
				log.Printf("sd_notify %s: READY=1", n.name)
			}
		case "STOPPING":
			if val == "1" {
				n.stopping.Store(true)
				log.Printf("sd_notify %s: STOPPING=1", n.name)
			}
		case "STATUS":
			n.status.Store(val)
		}
		// Other keys (MAINPID, BUSERROR, WATCHDOG, ...) are silently ignored.
	}
}
