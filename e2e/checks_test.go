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

func TestHTTPCheck(t *testing.T) {
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: web
    command: /test/http-server.sh
    on-failure: shutdown
    on-success: ignore

  - name: test-runner
    command: /bin/sh
    args: ["-c", "echo HTTP ready-check passed"]
    after: [web]
    ready-check: web-http
    ready-timeout: 10s
    on-success: success-shutdown
    on-failure: failure-shutdown

checks:
  web-http:
    http:
      url: http://127.0.0.1:8080/health
    period: 1s
    timeout: 2s
    threshold: 1
    level: ready
`,
		"http-server.sh": `#!/bin/sh
while true; do
  printf "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok" | nc -l -p 8080 2>/dev/null
done
`,
	}, 20*time.Second)

	if code != 0 {
		t.Fatalf("exit %d (HTTP check likely failed)\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "HTTP ready-check passed") {
		t.Errorf("test-runner did not execute:\n%s", out)
	}
}

func TestTCPCheck(t *testing.T) {
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: listener
    command: /bin/sh
    args: ["-c", "while true; do nc -l -p 5432 < /dev/null 2>/dev/null; done"]
    on-failure: shutdown
    on-success: ignore

  - name: test-runner
    command: /bin/sh
    args: ["-c", "echo TCP ready-check passed"]
    after: [listener]
    ready-check: tcp-check
    ready-timeout: 10s
    on-success: success-shutdown
    on-failure: failure-shutdown

checks:
  tcp-check:
    tcp:
      host: 127.0.0.1
      port: 5432
    period: 1s
    timeout: 2s
    threshold: 1
    level: ready
`,
	}, 20*time.Second)

	if code != 0 {
		t.Fatalf("exit %d (TCP check likely failed)\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "TCP ready-check passed") {
		t.Errorf("test-runner did not execute:\n%s", out)
	}
}

func TestExecCheckFailureShutdown(t *testing.T) {
	// Health check always fails, threshold 2, action: shutdown.
	_, code, _ := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: app
    command: sleep
    args: ["300"]
    on-failure: shutdown
    on-check-failure:
      bad-check: shutdown

checks:
  bad-check:
    exec:
      command: /bin/sh
      args: ["-c", "exit 1"]
    period: 500ms
    timeout: 1s
    threshold: 2
    initial-delay: 100ms
`,
	}, 15*time.Second)

	if code != 1 {
		t.Errorf("expected exit code 1 from check failure shutdown, got %d", code)
	}
}

func TestExecCheckFailureRestart(t *testing.T) {
	// Health check fails, on-check-failure: restart calls Stop() on the service.
	// The reap loop then sees a stopped-by-signal exit (effective code 0), so
	// on-success: restart is needed for the service to actually restart.
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown

  - name: app
    command: /bin/sh
    args: ["-c", "echo started >> /test/starts.log && sleep 300"]
    on-success: restart
    on-failure: restart
    on-check-failure:
      flaky-check: restart

  - name: test-runner
    command: /bin/sh
    args: ["-c", "sleep 10 && wc -l < /test/starts.log | tr -d ' '"]
    on-success: success-shutdown
    on-failure: failure-shutdown

checks:
  flaky-check:
    exec:
      command: /bin/sh
      args: ["-c", "exit 1"]
    period: 1s
    timeout: 1s
    threshold: 1
    initial-delay: 2s
`,
	}, 25*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	starts := strings.TrimSpace(readTestFile(t, dir, "starts.log"))
	lines := strings.Split(starts, "\n")
	if len(lines) < 2 {
		t.Errorf("expected at least 2 starts (check-failure restart), got %d", len(lines))
	}
}
