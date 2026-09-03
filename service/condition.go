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

// FileConditions is the start-gating subset of Process; reload swaps it atomically on running services.
type FileConditions struct {
	Exists  string
	Missing string
}

// FileConditions extracts the start-gating paths from p.
func (p *Process) FileConditions() FileConditions {
	return FileConditions{Exists: p.ConditionFileExists, Missing: p.ConditionFileMissing}
}

// UnmetCondition evaluates the file conditions, returning the skip reason, or "".
func (p *Process) UnmetCondition() string {
	return p.FileConditions().Unmet()
}

// UnmetCondition evaluates the live file conditions, returning the skip reason, or "".
func (s *Service) UnmetCondition() string {
	c := s.conds.Load()
	if c == nil {
		return s.Proc.UnmetCondition()
	}
	return c.Unmet()
}

// SetFileConditions replaces the live conditions; Proc keeps the original ones.
func (s *Service) SetFileConditions(c FileConditions) {
	s.conds.Store(&c)
}

// Unmet evaluates the conditions, returning the skip reason, or "".
func (c FileConditions) Unmet() string {
	if c.Exists != "" {
		exists, err := fileExists(c.Exists)
		if err != nil {
			return fmt.Sprintf("condition-file-exists: %v", err)
		}
		if !exists {
			return fmt.Sprintf("condition-file-exists: %s is missing", c.Exists)
		}
	}
	if c.Missing != "" {
		exists, err := fileExists(c.Missing)
		if err != nil {
			return fmt.Sprintf("condition-file-missing: %v", err)
		}
		if exists {
			return fmt.Sprintf("condition-file-missing: %s exists", c.Missing)
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
