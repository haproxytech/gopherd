package logger

import (
	"testing"
)

func TestTargetAppliesToAll(t *testing.T) {
	lt := &Target{services: map[string]bool{}}
	if !lt.AppliesTo("anything") {
		t.Error("empty filter should apply to all")
	}
}

func TestTargetAppliesToFiltered(t *testing.T) {
	lt := &Target{services: map[string]bool{"app": true, "sidecar": true}}
	if !lt.AppliesTo("app") {
		t.Error("should apply to app")
	}
	if lt.AppliesTo("other") {
		t.Error("should not apply to other")
	}
}

func TestTargetCloseNilWriter(_ *testing.T) {
	lt := &Target{}
	lt.Close() // should not panic
}

func TestNewTargetUnsupportedType(t *testing.T) {
	_, err := NewTarget("bad", TargetConfig{Type: "unknown", Location: "udp://localhost:514"})
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

func TestNewTargetInvalidSyslogLocation(t *testing.T) {
	_, err := NewTarget("bad", TargetConfig{Type: "syslog", Location: "not-a-url"})
	if err == nil {
		t.Error("expected error for invalid location")
	}
}

func TestNewTargetSyslogEmptyLocation(t *testing.T) {
	_, err := NewTarget("bad", TargetConfig{Type: "syslog", Location: ""})
	if err == nil {
		t.Error("expected error for empty location")
	}
}
