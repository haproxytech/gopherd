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
	"net"
	"os"
	"path/filepath"
	"strings"
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
