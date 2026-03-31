package control

import "testing"

func TestIsClientCommand(t *testing.T) {
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
