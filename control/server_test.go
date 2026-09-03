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

package control

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func testSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "test.sock")
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cs := NewServer(Config{SocketPath: testSocket(t)})
	cs.StatusFn = func(name string) (string, error) {
		if name == "svc1" {
			return "svc1: running (pid 42)", nil
		}
		return "", fmt.Errorf("unknown service %q", name)
	}
	cs.StartFn = func(name string) (string, error) {
		if name == "svc1" {
			return "svc1: started (pid 100)", nil
		}
		return "", fmt.Errorf("unknown service %q", name)
	}
	cs.StopFn = func(name string) (string, error) {
		if name == "svc1" {
			return "svc1: stop signal sent", nil
		}
		return "", fmt.Errorf("unknown service %q", name)
	}
	cs.RestartFn = func(name string) (string, error) {
		if name == "svc1" {
			return "svc1: restart scheduled", nil
		}
		return "", fmt.Errorf("unknown service %q", name)
	}
	cs.SignalFn = func(name, sig string) (string, error) {
		if name == "svc1" {
			return fmt.Sprintf("svc1: sent %s", sig), nil
		}
		return "", fmt.Errorf("unknown service %q", name)
	}
	cs.ReloadFn = func() (string, error) {
		return "reload: ok", nil
	}
	cs.StatsFn = func() string {
		return "services:\n  svc1  up 5m  exits=0 restarts=0 ok=0 fail=0"
	}
	cs.LogsFn = func(name string, follow bool) ([][]byte, <-chan []byte, func(), error) {
		if name != "svc1" {
			return nil, nil, nil, fmt.Errorf("unknown service %q", name)
		}
		recent := [][]byte{[]byte("[svc1] line1\n"), []byte("[svc1] line2\n")}
		if !follow {
			return recent, nil, nil, nil
		}
		ch := make(chan []byte, 10)
		ch <- []byte("[svc1] live1\n")
		close(ch)
		return recent, ch, func() {}, nil
	}

	if err := cs.Start(); err != nil {
		t.Fatalf("start control server: %v", err)
	}
	t.Cleanup(cs.Stop)
	return cs
}

func sendCommand(t *testing.T, socketPath, cmd string) string {
	t.Helper()
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "%s\n", cmd)
	scanner := bufio.NewScanner(conn)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return strings.Join(lines, "\n")
}

func TestStatus(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "status svc1")
	if !strings.Contains(resp, "running") {
		t.Errorf("expected running, got: %q", resp)
	}
}

func TestStatusUnknown(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "status bogus")
	if !strings.Contains(resp, "error:") {
		t.Errorf("expected error, got: %q", resp)
	}
}

func TestStartCmd(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "start svc1")
	if !strings.Contains(resp, "started") {
		t.Errorf("expected started, got: %q", resp)
	}
}

func TestStopCmd(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "stop svc1")
	if !strings.Contains(resp, "stop signal sent") {
		t.Errorf("expected stop signal sent, got: %q", resp)
	}
}

func TestRestart(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "restart svc1")
	if !strings.Contains(resp, "restart scheduled") {
		t.Errorf("expected restart scheduled, got: %q", resp)
	}
}

func TestSignalCmd(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "signal svc1 SIGUSR2")
	if !strings.Contains(resp, "sent SIGUSR2") {
		t.Errorf("expected sent SIGUSR2, got: %q", resp)
	}
}

func TestSignalUnknownService(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "signal bogus SIGUSR2")
	if !strings.Contains(resp, "error:") {
		t.Errorf("expected error, got: %q", resp)
	}
}

func TestSignalMissingArgs(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "signal svc1")
	if !strings.Contains(resp, "error:") {
		t.Errorf("expected error, got: %q", resp)
	}
}

func TestUnknownCommand(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "foobar")
	if !strings.Contains(resp, "error:") {
		t.Errorf("expected error, got: %q", resp)
	}
}

func TestMissingServiceName(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "start")
	if !strings.Contains(resp, "error:") {
		t.Errorf("expected error, got: %q", resp)
	}
}

func TestSocketCleanup(t *testing.T) {
	t.Parallel()
	path := testSocket(t)
	cs := NewServer(Config{SocketPath: path})
	cs.StatusFn = func(string) (string, error) { return "", nil }
	cs.StartFn = func(string) (string, error) { return "", nil }
	cs.StopFn = func(string) (string, error) { return "", nil }
	cs.RestartFn = func(string) (string, error) { return "", nil }
	cs.SignalFn = func(string, string) (string, error) { return "", nil }

	if err := cs.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("socket should exist: %v", err)
	}
	cs.Stop()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("socket should be removed after stop")
	}
}

func TestStatusOverviewCmd(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "status")
	if !strings.Contains(resp, "svc1") {
		t.Errorf("expected svc1 in overview, got: %q", resp)
	}
}

func TestStatusJSONOverview(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	cs.StatsJSONFn = func() string { return `{"services":[{"name":"svc1"}],"checks":[]}` }
	resp := sendCommand(t, cs.SocketPath, "status -o json")
	if !strings.Contains(resp, `"services"`) || !strings.Contains(resp, `"svc1"`) {
		t.Errorf("expected JSON with svc1, got: %q", resp)
	}
}

func TestStatusJSONSingle(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	cs.StatusJSONFn = func(name string) (string, error) {
		return `{"name":"` + name + `","state":"up"}`, nil
	}
	resp := sendCommand(t, cs.SocketPath, "status svc1 -o json")
	if !strings.Contains(resp, `"name":"svc1"`) || !strings.Contains(resp, `"state":"up"`) {
		t.Errorf("expected JSON for svc1, got: %q", resp)
	}
}

func TestStatusUnknownFormat(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "status -o xml")
	if !strings.Contains(resp, "error") {
		t.Errorf("expected error for unknown format, got: %q", resp)
	}
}

func TestStatusOExpectsArg(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "status -o")
	if !strings.Contains(resp, "error") {
		t.Errorf("expected error for bare -o, got: %q", resp)
	}
}

func TestParseStatusArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args    []string
		svc     string
		fmt     string
		wantErr bool
	}{
		{nil, "", "", false},
		{[]string{"svc1"}, "svc1", "", false},
		{[]string{"-o", "json"}, "", "json", false},
		{[]string{"svc1", "-o", "json"}, "svc1", "json", false},
		{[]string{"-o", "json", "svc1"}, "svc1", "json", false},
		{[]string{"-o"}, "", "", true},
		{[]string{"-o", "xml"}, "", "", true},
		{[]string{"a", "b"}, "", "", true},
	}
	for _, tt := range tests {
		svc, fmt, errMsg := parseStatusArgs(tt.args)
		gotErr := errMsg != ""
		if gotErr != tt.wantErr {
			t.Errorf("parseStatusArgs(%v) errMsg=%q want err=%v", tt.args, errMsg, tt.wantErr)
			continue
		}
		if !tt.wantErr && (svc != tt.svc || fmt != tt.fmt) {
			t.Errorf("parseStatusArgs(%v) = (%q, %q); want (%q, %q)",
				tt.args, svc, fmt, tt.svc, tt.fmt)
		}
	}
}

func TestReloadCmd(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "reload")
	if !strings.Contains(resp, "reload: ok") {
		t.Errorf("expected reload ok, got: %q", resp)
	}
}

func TestLogsRecent(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "logs svc1")
	if !strings.Contains(resp, "line1") || !strings.Contains(resp, "line2") {
		t.Errorf("expected recent lines, got: %q", resp)
	}
}

func TestLogsFollow(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "logs svc1 -f")
	// Should contain both recent and live lines.
	if !strings.Contains(resp, "line1") {
		t.Errorf("expected recent line1, got: %q", resp)
	}
	if !strings.Contains(resp, "live1") {
		t.Errorf("expected live line, got: %q", resp)
	}
}

func TestLogsUnknownService(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "logs bogus")
	if !strings.Contains(resp, "error:") {
		t.Errorf("expected error, got: %q", resp)
	}
}

func TestLogsMissingName(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)
	resp := sendCommand(t, cs.SocketPath, "logs")
	if !strings.Contains(resp, "error:") {
		t.Errorf("expected error, got: %q", resp)
	}
}

// TestLogsFollowUnblocksOnStop verifies that Server.Stop() returns promptly
// even while an idle `logs -f` subscriber is still connected. Without the
// cs.done signal, handleLogs would block on an empty channel, handlersWg
// would never complete, and the daemon would hang on shutdown.
func TestLogsFollowUnblocksOnStop(t *testing.T) {
	t.Parallel()
	cs := NewServer(Config{SocketPath: testSocket(t)})
	// LogsFn returns a channel that is never written to and never closed,
	// simulating a tail on a completely idle service.
	cs.LogsFn = func(_ string, follow bool) ([][]byte, <-chan []byte, func(), error) {
		if !follow {
			return nil, nil, nil, nil
		}
		ch := make(chan []byte) // never closed, never sent to
		return nil, ch, func() {}, nil
	}
	if err := cs.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Dial, send the follow command, and keep the connection open.
	conn, err := net.Dial("unix", cs.SocketPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "logs svc -f\n"); err != nil {
		t.Fatalf("send command: %v", err)
	}

	// Give the server a moment to enter the streaming select.
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		cs.Stop()
		close(done)
	}()
	select {
	case <-done:
		// Success: Stop() returned even though the subscriber was idle.
	case <-time.After(3 * time.Second):
		t.Fatal("Server.Stop() blocked on idle logs -f subscriber")
	}
}

func TestClientCommands(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"start", "stop", "restart", "status", "signal", "logs", "reload"} {
		if !ClientCommands[cmd] {
			t.Errorf("expected %q to be a client command", cmd)
		}
	}
	for _, cmd := range []string{"bash", "/bin/sh", "ls"} {
		if ClientCommands[cmd] {
			t.Errorf("expected %q to NOT be a client command", cmd)
		}
	}
}

func TestClientCommandListFunc(t *testing.T) {
	t.Parallel()
	list := ClientCommandList()
	if len(list) != len(ClientCommands) {
		t.Errorf("got %d items, want %d", len(list), len(ClientCommands))
	}
}

// TestStartAppliesSocketMode pins the control socket's permissions. The socket
// is full remote control — start, stop, signal, reload — so its mode is a
// security boundary. A widened mode has no functional symptom: every command
// test passes just the same, which is why it needs an explicit assertion.
func TestStartAppliesSocketMode(t *testing.T) {
	t.Parallel()
	cs := newTestServer(t)

	info, err := os.Lstat(cs.SocketPath)
	if err != nil {
		t.Fatalf("lstat socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is not a socket (mode %s)", cs.SocketPath, info.Mode())
	}
	if got, want := info.Mode().Perm(), DefaultSocketMode.Perm(); got != want {
		t.Errorf("socket mode = %04o, want %04o (the control socket must not be "+
			"reachable by other users)", got, want)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("socket mode %04o grants access outside the owner", perm)
	}
}

// TestStartRestoresUmask pins that Start leaves the process-wide umask as it
// found it. It tightens the umask so the socket is never briefly
// world-accessible between bind and chmod; not restoring it silently narrows
// the permissions of every file the daemon and its children create afterwards
// — log files, {{file}} targets — far from anything that would fail.
func TestStartRestoresUmask(t *testing.T) {
	// Not parallel: umask is process-wide state.
	const probe = 0o022
	before := syscall.Umask(probe)
	t.Cleanup(func() { syscall.Umask(before) })

	cs := NewServer(Config{SocketPath: testSocket(t)})
	if err := cs.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer cs.Stop()

	// Read the umask back by setting it again; Umask returns the previous value.
	after := syscall.Umask(probe)
	if after != probe {
		t.Errorf("umask after Start() = %04o, want %04o (Start must restore the "+
			"umask it tightened for the bind)", after, probe)
	}
}

// TestStopWaitsForInFlightHandlers pins that Stop does not return while a
// command handler is still running. The daemon tears down shared state right
// after Stop(), closing the restart channel, so a handler still inside
// RestartFn would send on a closed channel and panic PID 1 mid-shutdown.
func TestStopWaitsForInFlightHandlers(t *testing.T) {
	t.Parallel()
	cs := NewServer(Config{SocketPath: testSocket(t)})
	entered := make(chan struct{})
	release := make(chan struct{})
	var handlerDone atomic.Bool
	cs.StatusFn = func(string) (string, error) {
		close(entered)
		<-release
		handlerDone.Store(true)
		return "svc1: running (pid 42)", nil
	}
	if err := cs.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Drive a command and wait until the handler is provably inside StatusFn.
	go func() {
		conn, err := net.Dial("unix", cs.SocketPath)
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(conn, "status svc1\n")
		io.Copy(io.Discard, conn) //nolint:errcheck
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler never started")
	}

	stopped := make(chan struct{})
	go func() { cs.Stop(); close(stopped) }()

	// Stop must still be blocked on the in-flight handler.
	select {
	case <-stopped:
		t.Fatal("Stop() returned while a command handler was still running; the " +
			"daemon tears down shared state immediately afterwards")
	case <-time.After(300 * time.Millisecond):
	}

	close(release)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return after the handler finished")
	}
	if !handlerDone.Load() {
		t.Error("handler did not complete before Stop() returned")
	}
}

// TestStopIsIdempotent pins that a second Stop is harmless. Stop runs both from
// the shutdown path and from a defer, so a double call is the normal case, and
// closing the done channel twice would panic instead of shutting down.
func TestStopIsIdempotent(t *testing.T) {
	t.Parallel()
	cs := NewServer(Config{SocketPath: testSocket(t)})
	if err := cs.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	cs.Stop()
	cs.Stop() // must not panic
	cs.Stop()
}

// TestStartRefusesSymlinkedSocketPath pins the pre-bind symlink guard. Start
// removes a stale socket before binding, so without the check a symlink at the
// socket path is followed and the daemon serves start/stop/signal/reload at an
// attacker-chosen location. The residual TOCTOU window between Remove and
// Listen is covered by the separate post-bind verification.
func TestStartRefusesSymlinkedSocketPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "control.sock")
	// A dangling symlink: Listen(2) on it binds the *target* name.
	if err := os.Symlink(filepath.Join(dir, "elsewhere.sock"), sockPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cs := NewServer(Config{SocketPath: sockPath})
	err := cs.Start()
	if err == nil {
		cs.Stop()
		info, lerr := os.Lstat(sockPath)
		t.Fatalf("Start() accepted a symlinked socket path (lstat mode %v, %v); "+
			"it must verify after bind that the path is a real socket",
			info.Mode(), lerr)
	}
}

// helperSocketEnv names the socket a re-executed test helper should connect to.
const helperSocketEnv = "GOPHERD_TEST_PEER_SOCKET"

// TestHelperConnectAsOtherUID is not a test: it is the body of the child that
// TestPeerUIDGate spawns by re-executing this binary under a different uid. It
// prints the server's answer for the parent to assert on, and without the env
// var set does nothing.
func TestHelperConnectAsOtherUID(t *testing.T) {
	sock := os.Getenv(helperSocketEnv)
	if sock == "" {
		t.Skip("helper process only")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		fmt.Printf("HELPER-DIAL-ERROR: %v\n", err)
		return
	}
	defer conn.Close()
	fmt.Fprintf(conn, "status svc1\n")
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		fmt.Printf("HELPER-RESPONSE: %s\n", sc.Text())
	}
}

// TestPeerUIDGate pins the SO_PEERCRED check: a peer that is neither root nor
// the daemon's own user is refused even when it can open the socket.
//
// The gate sits behind the 0600 socket mode, so the test has to widen the mode
// to reach it at all. An operator who loosens socket-mode — to admit a sidecar,
// say — is then relying on this check alone to keep every other local uid away
// from start/stop/signal/reload.
func TestPeerUIDGate(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to connect from a different uid")
	}
	const otherUID = 65534 // nobody

	// t.TempDir() nests under a 0700 parent, and the test binary itself lives
	// somewhere only root can traverse, so stage both in a world-traversable
	// directory the child can actually reach.
	dir, err := os.MkdirTemp("", "peeruid-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Skipf("cannot read the test binary to stage a copy: %v", err)
	}
	helperBin := filepath.Join(dir, "helper.test")
	if err := os.WriteFile(helperBin, self, 0o755); err != nil {
		t.Fatalf("stage helper binary: %v", err)
	}

	cs := NewServer(Config{
		SocketPath: filepath.Join(dir, "control.sock"),
		SocketMode: 0o666, // deliberately wide, so the uid gate is the only barrier
	})
	cs.StatusFn = func(string) (string, error) { return "svc1: running (pid 42)", nil }
	if err := cs.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer cs.Stop()

	run := func(uid int) string {
		t.Helper()
		cmd := exec.Command(helperBin, "-test.run=^TestHelperConnectAsOtherUID$")
		cmd.Env = append(os.Environ(), helperSocketEnv+"="+cs.SocketPath)
		if uid >= 0 {
			cmd.SysProcAttr = &syscall.SysProcAttr{
				Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(uid)},
			}
		}
		out, err := cmd.CombinedOutput()
		if err != nil && !strings.Contains(string(out), "HELPER-") {
			t.Fatalf("helper (uid %d) failed: %v\n%s", uid, err, out)
		}
		return string(out)
	}

	// Positive control: as root the command is served.
	if out := run(-1); !strings.Contains(out, "svc1: running") {
		t.Fatalf("root peer was not served: %s", out)
	}

	// A foreign uid must be refused, and must not receive the status.
	out := run(otherUID)
	if strings.Contains(out, "HELPER-DIAL-ERROR") {
		t.Skipf("uid %d could not reach the socket, so the gate was not exercised: %s",
			otherUID, out)
	}
	if !strings.Contains(out, "permission denied") {
		t.Errorf("peer uid %d was not refused; response: %s", otherUID, out)
	}
	if strings.Contains(out, "svc1: running") {
		t.Errorf("peer uid %d was served a command; the peer-credential check must "+
			"reject any uid other than root or the daemon's own: %s", otherUID, out)
	}
}

// TestStreamingDoesNotHoldCommandSlot pins that a `logs -f` session releases
// its one-shot command slot before it starts streaming. The budgets are
// separate — 64 commands, 16 streams — precisely so streaming cannot starve
// commands. Holding both makes every live viewer cost a command slot, and
// enough of them stop the daemon answering `status` at the moment an operator
// is watching logs to find out what is wrong.
func TestStreamingDoesNotHoldCommandSlot(t *testing.T) {
	cs := NewServer(Config{SocketPath: testSocket(t)})
	// The one-shot handler blocks too, so the commands genuinely hold slots at
	// once; instant completions would recycle slots and hide an exhausted
	// budget.
	release := make(chan struct{})
	var inFlight atomic.Int32
	cs.StatusFn = func(string) (string, error) {
		inFlight.Add(1)
		<-release
		return "svc1: running (pid 42)", nil
	}
	cs.LogsFn = func(_ string, follow bool) ([][]byte, <-chan []byte, func(), error) {
		if !follow {
			return [][]byte{[]byte("line\n")}, nil, nil, nil
		}
		ch := make(chan []byte)
		go func() { <-release; close(ch) }()
		return nil, ch, func() {}, nil
	}
	if err := cs.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer cs.Stop()

	// Saturate the streaming budget with live sessions.
	var streams []net.Conn
	for range maxStreaming {
		conn, err := net.Dial("unix", cs.SocketPath)
		if err != nil {
			t.Fatalf("dial stream: %v", err)
		}
		fmt.Fprintf(conn, "logs svc1 -f\n")
		streams = append(streams, conn)
	}
	defer func() {
		for _, c := range streams {
			c.Close()
		}
	}()
	// Give the server time to accept them and reach the streaming path.
	time.Sleep(300 * time.Millisecond)

	// With the streaming sessions holding only streaming slots, there is still
	// room for more than (maxConns - maxStreaming) simultaneous commands.
	const commands = maxConns - maxStreaming + 8
	var wg sync.WaitGroup
	var answered atomic.Int32
	for range commands {
		wg.Go(func() {
			conn, err := net.Dial("unix", cs.SocketPath)
			if err != nil {
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
			fmt.Fprintf(conn, "status svc1\n")
			sc := bufio.NewScanner(conn)
			if sc.Scan() && strings.Contains(sc.Text(), "svc1") {
				answered.Add(1)
			}
		})
	}
	// Let every command that can be accepted reach the (blocked) handler.
	time.Sleep(time.Second)
	close(release)
	wg.Wait()

	if got := answered.Load(); int(got) != commands {
		t.Errorf("%d of %d one-shot commands were answered while %d log streams "+
			"were open; a streaming session must release its command slot",
			got, commands, maxStreaming)
	}
}
