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
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestIsClientCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"status"}, true},
		{[]string{"reload"}, true},
		{[]string{"restart", "haproxy"}, true},
		{[]string{"haproxy", "restart"}, true},
		{[]string{"haproxy", "start"}, true},
		{[]string{"haproxy", "stop"}, true},
		{[]string{"haproxy", "status"}, true},
		{[]string{"signal", "haproxy", "SIGUSR1"}, true},
		{[]string{"logs", "haproxy"}, true},
		{[]string{"/bin/sh"}, false},
		{[]string{"haproxy"}, false},
		{[]string{"haproxy", "--verbose"}, false},
		// Only the four *service actions* may appear as the second argument;
		// the rest take no service there. So "<binary> logs" is an entrypoint
		// handed an argument that happens to share a command name —
		// passthrough. Widening this breaks `docker run image /bin/sh logs`.
		{[]string{"/bin/sh", "logs"}, false},
		{[]string{"/bin/sh", "reload"}, false},
		{[]string{"/bin/sh", "signal"}, false},
		{[]string{"myapp", "logs"}, false},
		{[]string{"myapp", "reload"}, false},
		{[]string{"myapp", "signal"}, false},
	}
	for _, tt := range tests {
		got := IsClientCommand(tt.args)
		if got != tt.want {
			t.Errorf("IsClientCommand(%v) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

func TestClientCommandListSorted(t *testing.T) {
	t.Parallel()
	list := ClientCommandList()
	if len(list) == 0 {
		t.Fatal("expected non-empty command list")
	}
	if !slices.IsSorted(list) {
		t.Errorf("ClientCommandList() is not sorted: %v", list)
	}
}

// TestScannerErrDetectedOnReadError verifies that bufio.Scanner.Err() is
// non-nil when the underlying reader returns a non-EOF error. This property
// is what the scanner.Err() check in RunClient relies on: a connection reset
// or broken pipe mid-read produces scanner.Err() != nil so the client can
// exit non-zero instead of silently succeeding with partial output.
// TestBuildClientCommandActionFirst covers the bug where "gopherd restart haproxy"
// (action-first form) was recognised by IsClientCommand but then mishandled in
// RunClient: args[1]="haproxy" was not a known action, so the client exited with
// "unknown action 'haproxy'" instead of sending "restart haproxy" to the daemon.
func TestBuildClientCommandActionFirst(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args    []string
		want    string
		wantErr bool
	}{
		// service-first form (existing, must still work)
		{[]string{"haproxy", "restart"}, "restart haproxy", false},
		{[]string{"haproxy", "start"}, "start haproxy", false},
		{[]string{"haproxy", "stop"}, "stop haproxy", false},
		{[]string{"haproxy", "status"}, "status haproxy", false},
		// action-first form (the bug: was returning "unknown action 'haproxy'")
		{[]string{"restart", "haproxy"}, "restart haproxy", false},
		{[]string{"start", "haproxy"}, "start haproxy", false},
		{[]string{"stop", "haproxy"}, "stop haproxy", false},
		{[]string{"status", "haproxy"}, "status haproxy", false},
		// one-word commands
		{[]string{"status"}, "status", false},
		{[]string{"reload"}, "reload", false},
		// status with -o json flag, in all three positional forms
		{[]string{"status", "-o", "json"}, "status -o json", false},
		{[]string{"status", "app", "-o", "json"}, "status app -o json", false},
		{[]string{"app", "status", "-o", "json"}, "status app -o json", false},
		// unknown format
		{[]string{"status", "-o", "xml"}, "", true},
		// invalid
		{[]string{"haproxy", "badaction"}, "", true},
		{[]string{}, "", true},
	}
	for _, tt := range tests {
		got, err := buildClientCommand(tt.args)
		if tt.wantErr {
			if err == nil {
				t.Errorf("buildClientCommand(%v) = %q, nil; want error", tt.args, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("buildClientCommand(%v) error: %v", tt.args, err)
			continue
		}
		if got != tt.want {
			t.Errorf("buildClientCommand(%v) = %q; want %q", tt.args, got, tt.want)
		}
	}
}

func TestScannerErrDetectedOnReadError(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	// Write a partial line (no newline), then close with a non-EOF error to
	// simulate a TCP RST or broken pipe mid-response.
	go func() {
		_, _ = pw.Write([]byte("partial line without newline"))
		pw.CloseWithError(fmt.Errorf("connection reset by peer"))
	}()

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		// consume any complete lines
	}
	if scanner.Err() == nil {
		t.Error("scanner.Err() must be non-nil when the underlying reader returns a non-EOF error")
	}
}

// TestIsAlive verifies the startup probe: a reachable listener owned by our own
// uid is treated as a running gopherd, while a non-existent socket is not. The
// same-uid case exercises the SO_PEERCRED acceptance path added to defend
// against a foreign-uid squatter blocking startup.
func TestIsAlive(t *testing.T) {
	t.Parallel()

	if IsAlive(filepath.Join(t.TempDir(), "nonexistent.sock")) {
		t.Error("IsAlive must be false for a non-existent socket path")
	}

	sockPath := filepath.Join(t.TempDir(), "alive.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()

	// Listener is owned by the test process (our euid), so the peer-cred check
	// accepts it. On non-Linux, peerUID is -1 and IsAlive falls back to
	// reachability — true either way.
	if !IsAlive(sockPath) {
		t.Error("IsAlive must be true for a reachable listener owned by our own uid")
	}
}

// TestExtractOutputFlag pins the trailing `-o <fmt>` extraction, including the
// malformed shapes users type. The scan must not read past the end of the
// slice: `gopherd status -o` with no format is an ordinary typo, and reading
// past it panics the CLI instead of printing a usage error.
func TestExtractOutputFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		args    []string
		pos     []string
		suffix  string
		wantErr bool
	}{
		{"no flag", []string{"status"}, []string{"status"}, "", false},
		{"flag only", []string{"status", "-o", "json"}, []string{"status"}, "-o json", false},
		{
			"with service",
			[]string{"status", "svc", "-o", "json"},
			[]string{"status", "svc"},
			"-o json", false,
		},
		// The dangling flag: nothing follows -o, so there is nothing to read.
		{"dangling -o", []string{"status", "-o"}, []string{"status", "-o"}, "", false},
		{"only -o", []string{"-o"}, []string{"-o"}, "", false},
		{"unknown format", []string{"status", "-o", "yaml"}, nil, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pos, suffix, err := extractOutputFlag(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("extractOutputFlag(%v) err = %v, wantErr = %v", tc.args, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if strings.Join(pos, " ") != strings.Join(tc.pos, " ") {
				t.Errorf("positional = %v, want %v", pos, tc.pos)
			}
			if suffix != tc.suffix {
				t.Errorf("suffix = %q, want %q", suffix, tc.suffix)
			}
		})
	}
}

// helperListenEnv names the socket path a re-executed helper should squat on.
const helperListenEnv = "GOPHERD_TEST_SQUAT_SOCKET"

// TestHelperListenAsOtherUID is not a test: it is the body of the child that
// TestIsAliveRejectsForeignOwner spawns as a different uid to hold a listener
// on the socket path.
func TestHelperListenAsOtherUID(t *testing.T) {
	path := os.Getenv(helperListenEnv)
	if path == "" {
		t.Skip("helper process only")
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		fmt.Printf("HELPER-LISTEN-ERROR: %v\n", err)
		return
	}
	defer ln.Close()
	fmt.Println("HELPER-READY")
	// Accept and discard until the parent kills us.
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		conn.Close()
	}
}

// TestIsAliveRejectsForeignOwner pins that "is another gopherd already running?"
// means *our* gopherd. IsAlive gates binding the control socket, so counting any
// listener as proof of life lets an unrelated local process squat the path and
// keep the daemon from starting — or, in client mode, answer for it.
func TestIsAliveRejectsForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to run the squatter under a different uid")
	}
	const otherUID = 65534 // nobody

	dir, err := os.MkdirTemp("", "squat-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Skipf("cannot stage a copy of the test binary: %v", err)
	}
	helperBin := filepath.Join(dir, "helper.test")
	if err := os.WriteFile(helperBin, self, 0o755); err != nil {
		t.Fatalf("stage helper: %v", err)
	}
	sock := filepath.Join(dir, "squatted.sock")

	cmd := exec.Command(helperBin, "-test.run=^TestHelperListenAsOtherUID$")
	cmd.Env = append(os.Environ(), helperListenEnv+"="+sock)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: otherUID, Gid: otherUID},
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	// Wait for the squatter to be listening.
	sc := bufio.NewScanner(stdout)
	ready := false
	for sc.Scan() {
		if strings.Contains(sc.Text(), "HELPER-READY") {
			ready = true
			break
		}
		if strings.Contains(sc.Text(), "HELPER-LISTEN-ERROR") {
			t.Skipf("squatter could not listen: %s", sc.Text())
		}
	}
	if !ready {
		t.Skip("squatter never reported ready")
	}

	if IsAlive(sock) {
		t.Error("IsAlive accepted a listener owned by another uid; only a socket " +
			"held by root or our own user is evidence that gopherd is running")
	}

	// Positive control: our own listener is recognised.
	ownSock := filepath.Join(dir, "own.sock")
	ln, err := net.Listen("unix", ownSock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if !IsAlive(ownSock) {
		t.Error("IsAlive rejected our own listener")
	}
}
