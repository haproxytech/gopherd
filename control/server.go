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

// Package control provides a Unix socket server and CLI client for runtime service control.
package control

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DefaultSocketMode is the default Unix socket file permission.
// 0o600 matches the uid-based access check in handleConn (owner-only): a
// looser mode (e.g. 0o660) would allow same-group peers to open the socket
// but then be rejected at the first command, which is surprising. Setting
// an explicit socket-mode in config together with a GID-aware deployment
// is the supported way to broaden access.
const DefaultSocketMode = os.FileMode(0o600)

// DefaultSocketPath is the default Unix socket path.
const DefaultSocketPath = "/run/gopherd.sock"

// Server listens on a Unix socket and handles service control commands.
type Server struct {
	listener net.Listener

	// Callbacks wired from main.
	StartFn   func(name string) (string, error)
	StopFn    func(name string) (string, error)
	RestartFn func(name string) (string, error)
	StatusFn  func(name string) (string, error)
	SignalFn  func(name, signal string) (string, error)
	ReloadFn  func() (string, error)
	StatsFn   func() string
	// LogsFn returns recent lines and a subscribe channel (nil if not follow mode).
	// The unsubscribe func must be called when done.
	LogsFn func(name string, follow bool) (recent [][]byte, ch <-chan []byte, unsub func(), err error)

	// done is closed by Stop() to signal long-running handlers (currently only
	// `logs -f` streaming) that they must return immediately rather than
	// blocking on their log channel. Without this, an idle streaming client
	// would hold handlersWg open until it disconnects, blocking clean
	// shutdown and leaking buffered log lines that closeLogTargets() would
	// otherwise flush.
	done chan struct{}

	SocketPath string

	// handlersWg tracks in-flight handleConn goroutines. Stop() waits on it so
	// the daemon can safely close daemon-owned channels (e.g. restartCh) after
	// Stop() returns without racing an in-flight handler that may still invoke
	// RestartFn / ReloadFn.
	handlersWg sync.WaitGroup

	mu         sync.Mutex
	socketMode os.FileMode

	closed bool
}

// Config defines control socket configuration.
type Config struct {
	SocketPath string
	SocketMode os.FileMode
}

// NewServer creates a new control server.
func NewServer(cfg Config) *Server {
	path := cfg.SocketPath
	if path == "" {
		path = DefaultSocketPath
	}
	mode := cfg.SocketMode
	if mode == 0 {
		mode = DefaultSocketMode
	}
	return &Server{SocketPath: path, socketMode: mode, done: make(chan struct{})}
}

// Start begins listening. Call in a goroutine or before the reap loop.
func (cs *Server) Start() error {
	// Verify the socket path is not a symlink before removing, to prevent
	// a TOCTOU attack where a symlink is placed at the socket path.
	if info, err := os.Lstat(cs.SocketPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("control socket: %s is a symlink, refusing to replace", cs.SocketPath)
		}
		os.Remove(cs.SocketPath)
	}

	// Set a restrictive umask before Listen so the socket is never
	// world-accessible between creation and Chmod.
	oldMask := syscall.Umask(0o077)
	ln, err := net.Listen("unix", cs.SocketPath)
	syscall.Umask(oldMask)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	// Post-bind verification: an attacker with write access to the parent
	// directory could have planted a symlink between our Remove and Listen,
	// causing bind(2) to materialise the socket at the symlink target. Confirm
	// that the path we just bound is a real socket file, not a symlink or
	// regular file. Fail closed if hijacked.
	postInfo, err := os.Lstat(cs.SocketPath)
	if err != nil {
		ln.Close()
		return fmt.Errorf("control socket: post-bind lstat %s: %w", cs.SocketPath, err)
	}
	if postInfo.Mode()&os.ModeSymlink != 0 || postInfo.Mode()&os.ModeSocket == 0 {
		ln.Close()
		os.Remove(cs.SocketPath)
		return fmt.Errorf("control socket: %s is not a socket after bind (mode %s); refusing to serve", cs.SocketPath, postInfo.Mode())
	}
	if err := os.Chmod(cs.SocketPath, cs.socketMode); err != nil {
		log.Printf("warning: control socket: chmod %s: %v", cs.SocketPath, err)
	}

	cs.listener = ln
	go cs.acceptLoop()
	return nil
}

const (
	// maxConns is the maximum number of concurrent control socket connections
	// for one-shot commands (start, stop, status, etc.).
	maxConns = 64
	// maxStreaming is the maximum number of concurrent streaming connections
	// (logs -f). These are tracked separately so streaming clients cannot
	// starve one-shot command slots.
	maxStreaming = 16
)

func (cs *Server) acceptLoop() {
	sem := make(chan struct{}, maxConns)
	streamSem := make(chan struct{}, maxStreaming)
	for {
		conn, err := cs.listener.Accept()
		if err != nil {
			cs.mu.Lock()
			closed := cs.closed
			cs.mu.Unlock()
			if closed {
				return
			}
			log.Printf("control socket: accept error: %v", err)
			time.Sleep(5 * time.Millisecond)
			continue
		}
		select {
		case sem <- struct{}{}:
			cs.handlersWg.Go(func() {
				cs.handleConn(conn, sem, streamSem)
			})
		default:
			// At capacity — reject the connection.
			conn.Close()
		}
	}
}

// connReadTimeout is the maximum time to wait for a client to send a command.
// Prevents slowloris-style attacks that hold connection slots indefinitely.
const connReadTimeout = 5 * time.Second

// connWriteTimeout is the maximum time allowed to write a response to a client.
// Prevents stalled readers from holding connection slots indefinitely.
const connWriteTimeout = 10 * time.Second

// streamIdleTimeout caps how long a `logs -f` subscription may sit quiet on
// an idle service. Without a cap, an authorised client (or attacker with the
// daemon's uid) could open up to maxStreaming sessions on silent services and
// permanently hold every streaming slot. Set well above the client's own
// read idle timeout so a legitimate active session is closed by the client,
// not pre-emptively by the server.
const streamIdleTimeout = 1 * time.Hour

func (cs *Server) handleConn(conn net.Conn, cmdSem, streamSem chan struct{}) {
	// Centralise semaphore release and panic recovery so that no path leaks
	// a cmdSem slot — including panics between accept and the first early
	// return, or inside a callback.
	cmdReleased := false
	releaseCmd := func() {
		if !cmdReleased {
			cmdReleased = true
			<-cmdSem
		}
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("control: handleConn panic: %v", r)
		}
		releaseCmd()
		conn.Close()
	}()

	conn.SetReadDeadline(time.Now().Add(connReadTimeout))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}
	line := strings.TrimSpace(scanner.Text())
	if line == "" {
		return
	}

	uid := peerUID(conn)
	// On Linux, enforce that only root (uid 0) or the daemon's own user may
	// issue commands. This is defense-in-depth on top of socket file
	// permissions (mode 0600). On other platforms uid is -1 (unavailable)
	// and access control falls back to filesystem permissions alone.
	if uid != -1 && uid != 0 && uid != os.Geteuid() {
		log.Printf("control: uid=%d rejected: permission denied", uid)
		conn.SetWriteDeadline(time.Now().Add(connWriteTimeout))
		fmt.Fprintf(conn, "error: permission denied\n")
		return
	}
	log.Printf("control: uid=%d cmd=%q", uid, line)

	// Clear deadline for command handling (logs -f may stream indefinitely).
	conn.SetReadDeadline(time.Time{})

	parts := strings.Fields(line)
	// Streaming commands release the command slot and acquire a streaming
	// slot instead, so they cannot starve one-shot commands.
	if parts[0] == "logs" {
		releaseCmd() // give the command slot back before switching to streaming
		select {
		case streamSem <- struct{}{}:
			defer func() { <-streamSem }()
			cs.handleLogs(conn, parts)
		default:
			conn.SetWriteDeadline(time.Now().Add(connWriteTimeout))
			fmt.Fprintf(conn, "error: too many streaming connections\n")
		}
		return
	}

	resp := cs.handleCommand(parts)
	conn.SetWriteDeadline(time.Now().Add(connWriteTimeout))
	fmt.Fprintf(conn, "%s\n", resp)
}

func (cs *Server) handleCommand(parts []string) string {
	if len(parts) == 0 {
		return "error: empty command"
	}

	cmd := parts[0]

	switch cmd {
	case "status":
		// Bare "status" returns the overview table. With a service name it
		// returns the single-service line.
		if len(parts) < 2 {
			if cs.StatsFn == nil {
				return "error: status not supported"
			}
			return cs.StatsFn()
		}
		if cs.StatusFn == nil {
			return "error: status not supported"
		}
		msg, err := cs.StatusFn(parts[1])
		if err != nil {
			return fmt.Sprintf("error: %v", err)
		}
		return msg

	case "start", "stop", "restart":
		if len(parts) < 2 {
			return fmt.Sprintf("error: %s requires a service name", cmd)
		}
		name := parts[1]
		var fn func(string) (string, error)
		switch cmd {
		case "start":
			fn = cs.StartFn
		case "stop":
			fn = cs.StopFn
		case "restart":
			fn = cs.RestartFn
		}
		if fn == nil {
			return fmt.Sprintf("error: %s not supported", cmd)
		}
		msg, err := fn(name)
		if err != nil {
			return fmt.Sprintf("error: %v", err)
		}
		return msg

	case "signal":
		if len(parts) < 3 {
			return "error: signal requires <service> <signal-name>"
		}
		if cs.SignalFn == nil {
			return "error: signal not supported"
		}
		msg, err := cs.SignalFn(parts[1], parts[2])
		if err != nil {
			return fmt.Sprintf("error: %v", err)
		}
		return msg

	case "reload":
		if cs.ReloadFn == nil {
			return "error: reload not supported"
		}
		msg, err := cs.ReloadFn()
		if err != nil {
			return fmt.Sprintf("error: %v", err)
		}
		return msg

	default:
		return fmt.Sprintf("error: unknown command %q (try: start, stop, restart, status, signal, logs, reload)", cmd)
	}
}

func (cs *Server) handleLogs(conn net.Conn, parts []string) {
	if len(parts) < 2 {
		conn.SetWriteDeadline(time.Now().Add(connWriteTimeout))
		fmt.Fprintf(conn, "error: logs requires a service name\n")
		return
	}
	name := parts[1]
	follow := len(parts) >= 3 && parts[2] == "-f"

	if cs.LogsFn == nil {
		conn.SetWriteDeadline(time.Now().Add(connWriteTimeout))
		fmt.Fprintf(conn, "error: logs not supported\n")
		return
	}

	recent, ch, unsub, err := cs.LogsFn(name, follow)
	if err != nil {
		conn.SetWriteDeadline(time.Now().Add(connWriteTimeout))
		fmt.Fprintf(conn, "error: %v\n", err)
		return
	}
	if unsub != nil {
		defer unsub()
	}

	// Send recent buffered lines.
	for _, line := range recent {
		conn.SetWriteDeadline(time.Now().Add(connWriteTimeout))
		if _, err := conn.Write(line); err != nil {
			return
		}
	}

	if !follow || ch == nil {
		return
	}

	// Stream new lines until:
	//   - the subscription channel closes (the service removed its writer),
	//   - the client disconnects (surfaces as a Write error),
	//   - the server is shutting down (cs.done closed by Stop), or
	//   - the session has sat idle longer than streamIdleTimeout.
	// A per-write deadline separately prevents a slow reader from stalling
	// an in-flight Write.
	idle := time.NewTimer(streamIdleTimeout)
	defer idle.Stop()
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(connWriteTimeout))
			if _, err := conn.Write(line); err != nil {
				return
			}
			// Reset idle timer on every successful write.
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(streamIdleTimeout)
		case <-idle.C:
			return
		case <-cs.done:
			return
		}
	}
}

// Stop shuts down the server and removes the socket file.
// Blocks until all in-flight handleConn goroutines have returned, so callers
// can rely on no further callbacks (StartFn, RestartFn, ReloadFn, ...) firing
// after Stop() returns. Without this, a late RestartFn could panic sending on
// an already-closed restartCh.
func (cs *Server) Stop() {
	cs.mu.Lock()
	alreadyClosed := cs.closed
	cs.closed = true
	cs.mu.Unlock()
	// Signal streaming handlers to return. Guard against a double Stop(),
	// which would double-close the channel and panic.
	if !alreadyClosed && cs.done != nil {
		close(cs.done)
	}
	if cs.listener != nil {
		cs.listener.Close()
	}
	// Closing the listener wakes up Accept(); wait for all in-flight handlers
	// to return before the caller proceeds to tear down shared state.
	cs.handlersWg.Wait()
	os.Remove(cs.SocketPath)
}
