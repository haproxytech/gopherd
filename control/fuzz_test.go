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
	"fmt"
	"strings"
	"testing"
)

// FuzzHandleCommand feeds arbitrary command lines through the same
// tokenize-and-dispatch path that handleConn uses on the wire. gopherd runs
// as PID 1: a panic here from a malformed peer command would kill the
// container's init.
func FuzzHandleCommand(f *testing.F) {
	seeds := []string{
		"",
		" ",
		"\t\t\t",
		"status",
		"start",
		"start svc",
		"stop svc",
		"restart svc",
		"status svc",
		"signal",
		"signal svc",
		"signal svc SIGTERM",
		"signal svc \x00",
		"reload",
		"logs",
		"logs svc",
		"logs svc -f",
		"unknown-command",
		"start  svc  extra args",
		"start\tsvc",
		"start \"quoted name\"",
		"start svc\nstop svc",
		strings.Repeat("a", 4096),
		"signal " + strings.Repeat("x", 1024) + " SIGTERM",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	srv := &Server{
		// Wire every callback so each branch is reachable; return values
		// must not influence whether a panic occurs.
		StartFn:   func(name string) (string, error) { return "started " + name, nil },
		StopFn:    func(name string) (string, error) { return "stopped " + name, nil },
		RestartFn: func(name string) (string, error) { return "restarted " + name, nil },
		StatusFn:  func(name string) (string, error) { return "ok " + name, nil },
		SignalFn:  func(name, sig string) (string, error) { return "signaled " + name + " " + sig, nil },
		ReloadFn:  func() (string, error) { return "reloaded", nil },
		StatsFn:   func() string { return "stats" },
	}

	f.Fuzz(func(t *testing.T, line string) {
		// Match the tokenization handleConn performs before dispatch.
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		parts := strings.Fields(line)
		if len(parts) == 0 {
			return
		}
		resp := srv.handleCommand(parts)
		// handleCommand must always return some response; an empty string
		// would mean the server sends a bare newline to a peer, which we
		// treat as a contract violation.
		if resp == "" {
			t.Fatalf("empty response for input %q", line)
		}
		_ = fmt.Sprint(resp)
	})
}
