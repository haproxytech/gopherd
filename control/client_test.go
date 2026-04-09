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
	"slices"
	"testing"
)

func TestIsClientCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"list"}, true},
		{[]string{"stats"}, true},
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
