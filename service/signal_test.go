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
	"syscall"
	"testing"
)

func TestParseSignal(t *testing.T) {
	tests := []struct {
		input string
		want  syscall.Signal
		err   bool
	}{
		{"SIGTERM", syscall.SIGTERM, false},
		{"TERM", syscall.SIGTERM, false},
		{"sigterm", syscall.SIGTERM, false},
		{"SIGUSR1", syscall.SIGUSR1, false},
		{"USR1", syscall.SIGUSR1, false},
		{"", syscall.SIGTERM, false},
		{"BOGUS", 0, true},
	}
	for _, tt := range tests {
		sig, err := ParseSignal(tt.input)
		if tt.err && err == nil {
			t.Errorf("ParseSignal(%q): expected error", tt.input)
		}
		if !tt.err && err != nil {
			t.Errorf("ParseSignal(%q): unexpected error: %v", tt.input, err)
		}
		if !tt.err && sig != tt.want {
			t.Errorf("ParseSignal(%q) = %v, want %v", tt.input, sig, tt.want)
		}
	}
}
