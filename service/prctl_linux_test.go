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

//go:build linux

package service

import (
	"syscall"
	"testing"
)

// TestSetChildSubreaper verifies the prctl call succeeds on Linux. The
// subreaper bit is a process-wide setting, so this test must NOT run in
// parallel: other tests in the suite spawn short-lived grandchildren that
// would otherwise re-parent to the test binary when their parent shell
// exits, left as unreaped zombies (because the Go test harness has no
// generic reap loop), and fool `kill(pid, 0)` liveness checks elsewhere
// in the suite. Undo the setting before returning so later tests observe
// the default behaviour even though t.Parallel schedules them after us.
func TestSetChildSubreaper(t *testing.T) {
	if err := SetChildSubreaper(); err != nil {
		t.Fatalf("SetChildSubreaper: %v", err)
	}
	// Clear the bit so parallel tests that follow see the original state.
	_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 0, 0, 0, 0, 0)
	if errno != 0 {
		t.Fatalf("undo subreaper: %v", errno)
	}
}

// TestSetPdeathsigWritesAttr verifies setPdeathsig populates the
// SysProcAttr.Pdeathsig field that Go's exec machinery will honour between
// fork and exec. Build tag keeps this test Linux-only because Pdeathsig is
// a Linux-only SysProcAttr field.
func TestSetPdeathsigWritesAttr(t *testing.T) {
	attr := &syscall.SysProcAttr{}
	setPdeathsig(attr, syscall.SIGTERM)
	if attr.Pdeathsig != syscall.SIGTERM {
		t.Errorf("Pdeathsig = %v, want SIGTERM", attr.Pdeathsig)
	}
}

// TestParentDeathSignalAppliedOnStart spawns a service with
// parent-death-signal configured and asserts that the process actually gets
// launched (exercising the setPdeathsig wiring in Start). A deeper test —
// "kill gopherd, check child dies" — would require spawning a grandparent
// process, which is better covered by an e2e docker test; this unit test
// verifies the fork/exec path does not reject the flag.
func TestParentDeathSignalAppliedOnStart(t *testing.T) {
	t.Parallel()
	svc := mustNew(t, Process{
		Name:              "pdeath",
		Command:           "sleep",
		Args:              []string{"5"},
		ParentDeathSignal: "SIGTERM",
	}, "")
	pid, err := svc.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Reap the child so we don't leak it.
	syscall.Kill(pid, syscall.SIGKILL)
	var ws syscall.WaitStatus
	syscall.Wait4(pid, &ws, 0, nil)
	svc.MarkExited()
}
