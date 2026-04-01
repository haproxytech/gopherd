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

package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestGlobalPrefixTimestampService(t *testing.T) {
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
prefix: "timestamp service"
processes:
  - name: greeter
    command: /bin/sh
    args: ["-c", "echo hello-prefix"]
    startup: oneshot

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [greeter]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "hello-prefix") {
			if !strings.Contains(line, "[greeter]") {
				t.Errorf("missing [greeter] tag in line: %s", line)
			}
			if !strings.Contains(line, "T") || !strings.Contains(line, "Z") {
				t.Errorf("missing timestamp in line: %s", line)
			}
			return
		}
	}
	t.Errorf("hello-prefix not found in output:\n%s", out)
}

func TestPerProcessPrefixOverride(t *testing.T) {
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
prefix: "timestamp service"
processes:
  - name: svc1
    command: /bin/sh
    args: ["-c", "echo svc1-output"]
    startup: oneshot
    prefix: "service"

  - name: svc2
    command: /bin/sh
    args: ["-c", "echo svc2-output"]
    startup: oneshot
    after: [svc1]

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [svc2]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}

	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "svc1-output") {
			if !strings.Contains(line, "[svc1]") {
				t.Errorf("missing [svc1] in: %s", line)
			}
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "[svc1]") {
				t.Errorf("svc1 should have no timestamp prefix, got: %s", line)
			}
		}
		if strings.Contains(line, "svc2-output") {
			if !strings.Contains(line, "[svc2]") {
				t.Errorf("missing [svc2] in: %s", line)
			}
			if !strings.Contains(line, "T") || !strings.Contains(line, "Z") {
				t.Errorf("missing timestamp for svc2: %s", line)
			}
		}
	}
}

func TestPrefixNone(t *testing.T) {
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
prefix: "none"
processes:
  - name: raw
    command: /bin/sh
    args: ["-c", "echo raw-output-line"]
    startup: oneshot

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [raw]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "raw-output-line") {
			if strings.Contains(line, "[raw]") {
				t.Errorf("prefix=none should not include service tag: %s", line)
			}
			return
		}
	}
	t.Errorf("raw-output-line not found in output:\n%s", out)
}

func TestPrefixServiceOnly(t *testing.T) {
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
prefix: "service"
processes:
  - name: svc
    command: /bin/sh
    args: ["-c", "echo service-only-line"]
    startup: oneshot

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [svc]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "service-only-line") {
			if !strings.Contains(line, "[svc]") {
				t.Errorf("expected [svc] prefix, got: %s", line)
			}
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "[svc]") {
				t.Errorf("expected line to start with [svc], got: %s", trimmed)
			}
			return
		}
	}
	t.Errorf("service-only-line not found in output:\n%s", out)
}

func TestPrefixTimestampOnly(t *testing.T) {
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
prefix: "timestamp"
processes:
  - name: svc
    command: /bin/sh
    args: ["-c", "echo timestamp-only-line"]
    startup: oneshot

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [svc]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "timestamp-only-line") {
			if strings.Contains(line, "[svc]") {
				t.Errorf("prefix=timestamp should not include service tag: %s", line)
			}
			if !strings.Contains(line, "T") || !strings.Contains(line, "Z") {
				t.Errorf("expected timestamp in line: %s", line)
			}
			return
		}
	}
	t.Errorf("timestamp-only-line not found in output:\n%s", out)
}
