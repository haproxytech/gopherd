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

package logger

import (
	"strings"
	"testing"
)

func TestTargetAppliesToAll(t *testing.T) {
	t.Parallel()
	lt := &Target{services: map[string]bool{}}
	if !lt.AppliesTo("anything") {
		t.Error("empty filter should apply to all")
	}
}

func TestTargetAppliesToFiltered(t *testing.T) {
	t.Parallel()
	lt := &Target{services: map[string]bool{"app": true, "sidecar": true}}
	if !lt.AppliesTo("app") {
		t.Error("should apply to app")
	}
	if lt.AppliesTo("other") {
		t.Error("should not apply to other")
	}
}

func TestTargetCloseNilWriter(t *testing.T) {
	t.Parallel()
	lt := &Target{}
	lt.Close() // should not panic
}

func TestNewTargetUnsupportedType(t *testing.T) {
	t.Parallel()
	_, err := NewTarget("bad", TargetConfig{Type: "unknown", Location: "udp://localhost:514"})
	if err == nil {
		t.Error("expected error for unsupported type")
	}
}

func TestNewTargetInvalidSyslogLocation(t *testing.T) {
	t.Parallel()
	_, err := NewTarget("bad", TargetConfig{Type: "syslog", Location: "not-a-url"})
	if err == nil {
		t.Error("expected error for invalid location")
	}
}

func TestNewTargetSyslogEmptyLocation(t *testing.T) {
	t.Parallel()
	_, err := NewTarget("bad", TargetConfig{Type: "syslog", Location: ""})
	if err == nil {
		t.Error("expected error for empty location")
	}
}

// TestSanitizePreservesNewlines covers M-51: the sanitize slow path (triggered by
// a control character < 0x20) must preserve '\n' bytes in the output. Without the
// '\n' exception, multi-line log entries forwarded to syslog/file targets would
// have their newlines stripped, merging separate lines into one long line.
func TestSanitizePreservesNewlines(t *testing.T) {
	t.Parallel()
	// \x01 triggers the slow path; \n must survive.
	input := []byte("line1\x01line2\nline3\n")
	got := sanitize(input)
	if !strings.Contains(got, "\n") {
		t.Errorf("sanitize() stripped newlines: %q", got)
	}
	if strings.Contains(got, "\x01") {
		t.Errorf("sanitize() kept control char \\x01: %q", got)
	}
}
