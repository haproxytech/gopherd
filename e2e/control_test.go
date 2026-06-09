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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogsFollow(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: ticker
    command: /bin/sh
    args: ["-c", "for i in 1 2 3 4 5; do echo tick-$i; sleep 0.5; done; sleep 300"]
    on-failure: shutdown

  - name: test-runner
    command: /test/assert.sh
    after: [ticker]
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
		"assert.sh": `#!/bin/sh
sleep 4

# Test non-follow mode: recent logs.
recent=$(/usr/local/bin/gopherd logs ticker 2>&1)
echo "recent: $recent"
echo "$recent" | grep -q "tick-1" || { echo "FAIL: tick-1 not in recent logs"; exit 1; }

# Test follow mode with timeout.
timeout 3 /usr/local/bin/gopherd logs ticker -f > /test/follow.log 2>&1 || true
if [ ! -s /test/follow.log ]; then
  echo "FAIL: follow mode produced no output"
  exit 1
fi
echo "follow output:"
cat /test/follow.log
echo "PASS"
`,
	}, 30*time.Second)

	if code != 0 {
		follow := ""
		if data, err := os.ReadFile(filepath.Join(dir, "follow.log")); err == nil {
			follow = string(data)
		}
		t.Fatalf("exit %d\nfollow.log:\n%s\noutput:\n%s", code, follow, out)
	}
}

func TestClientForm(t *testing.T) {
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

# Client form: <service> <action>
status=$(/usr/local/bin/gopherd keeper status 2>&1)
echo "status: $status"
echo "$status" | grep -q "running" || { echo "FAIL: keeper not running"; exit 1; }

# Bare status: overview of all services
overview=$(/usr/local/bin/gopherd status 2>&1)
echo "overview: $overview"
echo "$overview" | grep -q "keeper" || { echo "FAIL: keeper not in overview"; exit 1; }

echo "PASS"
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
}

func TestControlReload(t *testing.T) {
	dc := runDetached(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: svc-a
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`,
	})
	defer dc.remove()

	time.Sleep(2 * time.Second)

	// Write new config with svc-b added.
	newCfg := `no-logo: true
processes:
  - name: svc-a
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown

  - name: svc-b
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`
	dc.writeConfig(newCfg)

	// Send reload command via docker exec.
	out, err := exec.Command("docker", "exec", dc.id,
		"/usr/local/bin/gopherd", "reload").CombinedOutput()
	if err != nil {
		t.Fatalf("reload command failed: %v\n%s", err, out)
	}

	time.Sleep(1 * time.Second)

	// Verify svc-b appeared.
	statsOut, err := exec.Command("docker", "exec", dc.id,
		"/usr/local/bin/gopherd", "status").CombinedOutput()
	if err != nil {
		t.Fatalf("stats command failed: %v\n%s", err, statsOut)
	}
	stats := string(statsOut)
	if !strings.Contains(stats, "svc-a") {
		t.Errorf("expected svc-a in stats: %s", stats)
	}
	if !strings.Contains(stats, "svc-b") {
		t.Errorf("expected svc-b in stats after reload: %s", stats)
	}

	dc.signal("TERM")
	dc.wait(10 * time.Second)
}

func TestControlStartStopRestart(t *testing.T) {
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-failure: shutdown

  - name: svc
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: ignore

  - name: test-runner
    command: /test/assert.sh
    after: [svc]
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
		"assert.sh": `#!/bin/sh
sleep 1

# Stop
/usr/local/bin/gopherd svc stop
sleep 1
status=$(/usr/local/bin/gopherd svc status 2>&1)
echo "$status" | grep -q "stopped" || { echo "FAIL: svc not stopped"; exit 1; }

# Start
/usr/local/bin/gopherd svc start
sleep 1
status=$(/usr/local/bin/gopherd svc status 2>&1)
echo "$status" | grep -q "running" || { echo "FAIL: svc not running after start"; exit 1; }

# Restart
/usr/local/bin/gopherd svc restart
sleep 2
status=$(/usr/local/bin/gopherd svc status 2>&1)
echo "$status" | grep -q "running" || { echo "FAIL: svc not running after restart"; exit 1; }

echo "PASS"
`,
	}, 20*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "PASS") {
		t.Errorf("control start/stop/restart test inconclusive:\n%s", out)
	}
}
