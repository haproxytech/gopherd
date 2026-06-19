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
	"fmt"
	"syscall"
)

// prSetChildSubreaper is the prctl(2) option for marking the caller as the child
// subreaper (Linux >= 3.4). Not exported by the stdlib syscall package; the value
// is stable per include/uapi/linux/prctl.h.
const prSetChildSubreaper = 36

// SetChildSubreaper marks the current process as the child subreaper so orphaned
// descendants re-parent to it (and get reaped by gopherd's Wait4 loop) instead of
// PID 1. Matters when gopherd is not PID 1 (docker exec, a k8s sidecar, nested
// init); harmless when it already is.
func SetChildSubreaper() error {
	_, _, errno := syscall.Syscall6(syscall.SYS_PRCTL, prSetChildSubreaper, 1, 0, 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("prctl(PR_SET_CHILD_SUBREAPER): %w", errno)
	}
	return nil
}

// setPdeathsig records the parent-death signal in attr so the kernel delivers sig
// to the child when its parent thread terminates (PR_SET_PDEATHSIG).
func setPdeathsig(attr *syscall.SysProcAttr, sig syscall.Signal) {
	attr.Pdeathsig = sig
}
