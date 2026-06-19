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

//go:build !linux

package service

import (
	"fmt"
	"syscall"
)

// SetChildSubreaper errors on non-Linux platforms: PR_SET_CHILD_SUBREAPER is
// Linux-only. The non-Linux build exists only for local development.
func SetChildSubreaper() error {
	return fmt.Errorf("subreaper mode is only supported on Linux")
}

// setPdeathsig is a no-op on non-Linux platforms (PR_SET_PDEATHSIG is Linux-only).
// Callers should surface a config-load error rather than silently ignore it.
func setPdeathsig(_ *syscall.SysProcAttr, _ syscall.Signal) {}
