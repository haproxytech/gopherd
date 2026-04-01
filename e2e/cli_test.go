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

func TestTagCommand(t *testing.T) {
	code, out := runGopherdCmd(t, "tag")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("tag command returned empty output")
	}
}

func TestVersionCommand(t *testing.T) {
	code, out := runGopherdCmd(t, "version")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if !strings.Contains(out, "gopherd") {
		t.Errorf("expected 'gopherd' in version output: %s", out)
	}
}

func TestPassthrough(t *testing.T) {
	code, out := runGopherdCmd(t, "echo", "hello-passthrough")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if !strings.Contains(out, "hello-passthrough") {
		t.Errorf("expected passthrough output: %s", out)
	}
}

func TestPassthroughNotFound(t *testing.T) {
	code, _ := runGopherdCmd(t, "nonexistent-binary-xyz")
	if code != 1 {
		t.Errorf("expected exit 1 for missing binary, got %d", code)
	}
}

func TestEntrypointFlagArgs(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: app
    command: /test/dump-args.sh
    use-entrypoint-args: true
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
		"dump-args.sh": `#!/bin/sh
echo "$@" > /test/args.log
sleep 1
`,
		"run.sh": `#!/bin/sh
exec /usr/local/bin/gopherd -- --flag1 --flag2=value
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	data := readTestFile(t, dir, "args.log")
	if !strings.Contains(data, "--flag1") || !strings.Contains(data, "--flag2=value") {
		t.Errorf("expected entrypoint flags in args, got: %s", data)
	}
}

func TestAlreadyRunningDetection(t *testing.T) {
	// Start daemon, then try starting a second instance from within.
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown

  - name: test-runner
    command: /test/assert.sh
    after: [keeper]
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
		"assert.sh": `#!/bin/sh
sleep 1
# Attempt to start a second daemon; should fail.
GOPHERD_CONFIG=/test/gopherd.yml /usr/local/bin/gopherd 2>&1 && echo FAIL_DID_NOT_EXIT || echo PASS_DETECTED
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "PASS_DETECTED") {
		t.Errorf("expected already-running detection:\n%s", out)
	}
}
