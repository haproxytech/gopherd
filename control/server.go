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
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultSocketMode is the default Unix socket file permission.
const DefaultSocketMode = os.FileMode(0o660)

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
	ListFn    func() string
	// LogsFn returns recent lines and a subscribe channel (nil if not follow mode).
	// The unsubscribe func must be called when done.
	LogsFn func(name string, follow bool) (recent [][]byte, ch <-chan []byte, unsub func(), err error)

	SocketPath string
	socketMode os.FileMode

	mu     sync.Mutex
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
	return &Server{SocketPath: path, socketMode: mode}
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

	ln, err := net.Listen("unix", cs.SocketPath)
	if err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	os.Chmod(cs.SocketPath, cs.socketMode)

	cs.listener = ln
	go cs.acceptLoop()
	return nil
}

// maxConns is the maximum number of concurrent control socket connections.
const maxConns = 64

func (cs *Server) acceptLoop() {
	sem := make(chan struct{}, maxConns)
	for {
		conn, err := cs.listener.Accept()
		if err != nil {
			cs.mu.Lock()
			closed := cs.closed
			cs.mu.Unlock()
			if closed {
				return
			}
			continue
		}
		select {
		case sem <- struct{}{}:
			go func() {
				defer func() { <-sem }()
				cs.handleConn(conn)
			}()
		default:
			// At capacity — reject the connection.
			conn.Close()
		}
	}
}

// connReadTimeout is the maximum time to wait for a client to send a command.
// Prevents slowloris-style attacks that hold connection slots indefinitely.
const connReadTimeout = 5 * time.Second

func (cs *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(connReadTimeout))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		return
	}
	line := strings.TrimSpace(scanner.Text())
	if line == "" {
		return
	}

	// Clear deadline for command handling (logs -f may stream indefinitely).
	conn.SetReadDeadline(time.Time{})

	parts := strings.Fields(line)
	// Handle streaming commands separately (they keep the conn open).
	if parts[0] == "logs" {
		cs.handleLogs(conn, parts)
		return
	}

	resp := cs.handleCommand(line)
	fmt.Fprintf(conn, "%s\n", resp)
}

func (cs *Server) handleCommand(line string) string {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return "error: empty command"
	}

	cmd := parts[0]

	switch cmd {
	case "list":
		return cs.ListFn()

	case "stats":
		if cs.StatsFn == nil {
			return "error: stats not supported"
		}
		return cs.StatsFn()

	case "start", "stop", "restart", "status":
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
		case "status":
			fn = cs.StatusFn
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
		return fmt.Sprintf("error: unknown command %q (try: list, stats, start, stop, restart, status, signal, logs, reload)", cmd)
	}
}

func (cs *Server) handleLogs(conn net.Conn, parts []string) {
	if len(parts) < 2 {
		fmt.Fprintf(conn, "error: logs requires a service name\n")
		return
	}
	name := parts[1]
	follow := len(parts) >= 3 && parts[2] == "-f"

	if cs.LogsFn == nil {
		fmt.Fprintf(conn, "error: logs not supported\n")
		return
	}

	recent, ch, unsub, err := cs.LogsFn(name, follow)
	if err != nil {
		fmt.Fprintf(conn, "error: %v\n", err)
		return
	}
	if unsub != nil {
		defer unsub()
	}

	// Send recent buffered lines.
	for _, line := range recent {
		if _, err := conn.Write(line); err != nil {
			return
		}
	}

	if !follow || ch == nil {
		return
	}

	// Stream new lines until client disconnects or channel closes.
	for line := range ch {
		if _, err := conn.Write(line); err != nil {
			return // client disconnected
		}
	}
}

// Stop shuts down the server and removes the socket file.
func (cs *Server) Stop() {
	cs.mu.Lock()
	cs.closed = true
	cs.mu.Unlock()
	if cs.listener != nil {
		cs.listener.Close()
	}
	os.Remove(cs.SocketPath)
}
