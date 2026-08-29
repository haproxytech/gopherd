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

package service

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// UnmetCondition evaluates the process's file conditions and returns a
// human-readable reason when a start should be skipped, or "" to proceed.
//
// os.Stat follows symlinks so a k8s ..data mount resolves to its target and a
// dangling symlink counts as missing. The check is advisory: the file state
// can change between this probe and the exec (inherent TOCTOU). A Stat error
// other than not-exist leaves the condition unmet with the error in the
// reason, so e.g. a permission problem never masquerades as a missing file.
func (p *Process) UnmetCondition() string {
	if p.ConditionFileExists != "" {
		exists, err := fileExists(p.ConditionFileExists)
		if err != nil {
			return fmt.Sprintf("condition-file-exists: %v", err)
		}
		if !exists {
			return fmt.Sprintf("condition-file-exists: %s is missing", p.ConditionFileExists)
		}
	}
	if p.ConditionFileMissing != "" {
		exists, err := fileExists(p.ConditionFileMissing)
		if err != nil {
			return fmt.Sprintf("condition-file-missing: %v", err)
		}
		if exists {
			return fmt.Sprintf("condition-file-missing: %s exists", p.ConditionFileMissing)
		}
	}
	return ""
}

// fileExists reports path existence via os.Stat. ENOENT and ENOTDIR mean
// missing; any other error is returned for the caller to surface.
func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return false, nil
	}
	return false, err
}
