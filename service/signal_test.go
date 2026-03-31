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
