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
// avoided.
//
// Abstract sockets have no filesystem permissions, so the listener enables
// SO_PASSCRED and accepts records only from the child's uid or root.
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

// readBufSize matches systemd's 4096; longer STATUS messages are truncated.
const readBufSize = 4096

// Listener owns a single abstract unix datagram socket and tracks state
// reported via sd_notify-style records. Safe for concurrent callers.
type Listener struct {
	conn    *net.UnixConn
	readyCh chan struct{}
	status  atomic.Value // string
	name    string
	path    string

	allowedUID int

	ready    atomic.Bool
	stopping atomic.Bool
	closed   atomic.Bool
	wg       sync.WaitGroup
}

// Listen creates a datagram socket at @gopherd-sd-notify-<pid>-<name> and
// begins reading. name is a stable service identifier; pid disambiguates
// concurrent gopherd instances.
//
// On Linux, records are accepted only from sender uid allowedUID (the child's
// uid) or root; other senders are dropped. Non-Linux accepts all (no creds).
func Listen(name string, pid, allowedUID int) (*Listener, error) {
	path := fmt.Sprintf("@gopherd-sd-notify-%d-%s", pid, name)
	addr := &net.UnixAddr{Net: "unixgram", Name: path}
	conn, err := net.ListenUnixgram("unixgram", addr)
	if err != nil {
		return nil, fmt.Errorf("sd_notify listen %s: %w", name, err)
	}
	if err := enablePassCred(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sd_notify listen %s: SO_PASSCRED: %w", name, err)
	}
	n := &Listener{
		conn:       conn,
		name:       name,
		path:       path,
		allowedUID: allowedUID,
		readyCh:    make(chan struct{}),
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
	oob := make([]byte, oobSize)
	for {
		nb, oobn, _, _, err := n.conn.ReadMsgUnix(buf, oob)
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
		if !n.authorized(oob[:oobn]) {
			continue
		}
		n.parse(buf[:nb])
	}
}

// authorized reports whether the datagram's sender (from ancillary creds) may
// drive state: the child's uid or root. Unattributable datagrams are dropped.
//
// Authorization is uid-only, so a co-located process sharing the child's uid
// can spoof READY/STOPPING/STATUS (the abstract socket name is discoverable).
// Impact is limited to readiness gating, not code execution; run each service
// under a distinct uid to avoid it. pid/cgroup attestation is overkill here.
func (n *Listener) authorized(oob []byte) bool {
	if !credEnabled {
		return true
	}
	uid, ok := parseSenderUID(oob)
	if !ok {
		log.Printf("sd_notify %s: dropping datagram with no sender credentials", n.name)
		return false
	}
	if uid != n.allowedUID && uid != 0 {
		log.Printf("sd_notify %s: dropping datagram from uid %d (expected %d or root)", n.name, uid, n.allowedUID)
		return false
	}
	return true
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
	}
}
