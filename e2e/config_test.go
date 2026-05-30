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

func TestEnvironment(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo $MY_VAR > /test/env.log && sleep 1"]
    environment:
      MY_VAR: hello-e2e
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	data := readTestFile(t, dir, "env.log")
	if !strings.Contains(data, "hello-e2e") {
		t.Errorf("expected MY_VAR=hello-e2e, got: %s", data)
	}
}

func TestTemplateExpansion(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo {{.MY_NAME}} > /test/tmpl.log && sleep 1"]
    environment:
      MY_NAME: world
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	data := readTestFile(t, dir, "tmpl.log")
	if !strings.Contains(data, "world") {
		t.Errorf("expected 'world' from template expansion, got: %s", data)
	}
}

func TestDotEnv(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo $FROM_DOTENV > /test/dotenv.log && sleep 1"]
    dotenv: /test/app.env
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
		"app.env": "FROM_DOTENV=loaded\n",
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	data := readTestFile(t, dir, "dotenv.log")
	if !strings.Contains(data, "loaded") {
		t.Errorf("expected FROM_DOTENV=loaded, got: %s", data)
	}
}

func TestWorkingDir(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "pwd > /test/wd.log && sleep 1"]
    working-dir: /tmp
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	data := readTestFile(t, dir, "wd.log")
	if !strings.Contains(data, "/tmp") {
		t.Errorf("expected /tmp, got: %s", data)
	}
}

func TestDisabledService(t *testing.T) {
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown

  - name: disabled-svc
    command: sleep
    args: ["300"]
    startup: disabled

  - name: test-runner
    command: /test/assert.sh
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
		"assert.sh": `#!/bin/sh
sleep 1

status=$(/usr/local/bin/gopherd disabled-svc status 2>&1)
echo "disabled: $status"
echo "$status" | grep -q "disabled" || { echo "FAIL: disabled-svc should be disabled"; exit 1; }

# Start it manually.
/usr/local/bin/gopherd disabled-svc start
sleep 1
status=$(/usr/local/bin/gopherd disabled-svc status 2>&1)
echo "after start: $status"
echo "$status" | grep -q "running" || { echo "FAIL: disabled-svc should now be running"; exit 1; }
echo "PASS"
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "PASS") {
		t.Errorf("disabled service test inconclusive:\n%s", out)
	}
}

func TestOneshotSuccess(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: init
    command: /bin/sh
    args: ["-c", "echo done > /test/init.log"]
    startup: oneshot

  - name: app
    command: /bin/sh
    args: ["-c", "sleep 1"]
    after: [init]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	data := readTestFile(t, dir, "init.log")
	if !strings.Contains(data, "done") {
		t.Errorf("oneshot marker not written: %s", data)
	}
}

func TestOneshotFailure(t *testing.T) {
	_, code, _ := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: bad-init
    command: /bin/sh
    args: ["-c", "exit 1"]
    startup: oneshot

  - name: app
    command: sleep
    args: ["300"]
    after: [bad-init]
`,
	}, 15*time.Second)

	if code == 0 {
		t.Error("expected non-zero exit for failed oneshot")
	}
}

func TestOneshotFailureIgnore(t *testing.T) {
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: bad-init
    command: /bin/sh
    args: ["-c", "exit 1"]
    startup: oneshot
    on-failure: ignore

  - name: app
    command: /bin/sh
    args: ["-c", "echo APP_STARTED && sleep 1"]
    after: [bad-init]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d (oneshot with on-failure:ignore should not abort)\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "APP_STARTED") {
		t.Errorf("expected APP_STARTED after ignored oneshot failure:\n%s", out)
	}
}

func TestExitActions(t *testing.T) {
	tests := []struct {
		name       string
		onSuccess  string
		onFailure  string
		cmd        string
		expectCode int
	}{
		{"success-shutdown-on-success", "success-shutdown", "shutdown", "exit 0", 0},
		{"shutdown-on-failure", "ignore", "shutdown", "exit 7", 7},
		{"failure-shutdown-on-failure", "ignore", "failure-shutdown", "exit 13", 13},
		{"ignore-on-failure", "ignore", "ignore", "exit 1", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// For "ignore" tests, we need a second service to terminate the daemon.
			config := ""
			if tt.onSuccess == "ignore" && tt.onFailure == "ignore" {
				config = `
no-logo: true
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "` + tt.cmd + `"]
    on-success: ` + tt.onSuccess + `
    on-failure: ` + tt.onFailure + `

  - name: stopper
    command: /bin/sh
    args: ["-c", "sleep 2"]
    on-success: success-shutdown
`
			} else {
				config = `
no-logo: true
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "sleep 1 && ` + tt.cmd + `"]
    on-success: ` + tt.onSuccess + `
    on-failure: ` + tt.onFailure + `
`
			}

			_, code, out := runContainer(t, map[string]string{
				"gopherd.yml": config,
			}, 15*time.Second)

			if code != tt.expectCode {
				t.Errorf("expected exit code %d, got %d\noutput:\n%s", tt.expectCode, code, out)
			}
		})
	}
}

func TestCustomStopSignal(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: trapper
    command: /bin/sh
    args: ["-c", "trap 'echo GOT_USR1 > /test/signal.log; exit 0' USR1; while true; do sleep 0.1; done"]
    stop-signal: SIGUSR1
    kill-delay: 5s
    on-success: success-shutdown
    on-failure: failure-shutdown

  - name: stopper
    command: /test/stop.sh
    after: [trapper]
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
		"stop.sh": `#!/bin/sh
sleep 1
/usr/local/bin/gopherd trapper stop
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	data := readTestFile(t, dir, "signal.log")
	if !strings.Contains(data, "GOT_USR1") {
		t.Errorf("expected GOT_USR1 in signal log: %s", data)
	}
}

func TestBackoffIncreases(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown

  - name: crasher
    command: /bin/sh
    args: ["-c", "date +%s.%N >> /test/times.log && exit 1"]
    on-failure: restart
    on-success: ignore
    backoff-delay: 200ms
    backoff-factor: 2.0
    backoff-limit: 5s

  - name: test-runner
    command: /bin/sh
    args: ["-c", "sleep 4 && wc -l < /test/times.log | tr -d ' '"]
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
	}, 20*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	data := readTestFile(t, dir, "times.log")
	lines := strings.Split(strings.TrimSpace(data), "\n")
	if len(lines) < 3 {
		t.Errorf("expected at least 3 restarts, got %d", len(lines))
	}
}
