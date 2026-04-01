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

# List command
list=$(/usr/local/bin/gopherd list 2>&1)
echo "list: $list"
echo "$list" | grep -q "keeper" || { echo "FAIL: keeper not in list"; exit 1; }

# Stats command
stats=$(/usr/local/bin/gopherd stats 2>&1)
echo "stats: $stats"
echo "$stats" | grep -q "keeper" || { echo "FAIL: keeper not in stats"; exit 1; }

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
	os.WriteFile(filepath.Join(dc.dir, "gopherd.yml"), []byte(newCfg), 0o644)

	// Send reload command via docker exec.
	out, err := exec.Command("docker", "exec", dc.id,
		"/usr/local/bin/gopherd", "reload").CombinedOutput()
	if err != nil {
		t.Fatalf("reload command failed: %v\n%s", err, out)
	}

	time.Sleep(1 * time.Second)

	// Verify svc-b appeared.
	listOut, err := exec.Command("docker", "exec", dc.id,
		"/usr/local/bin/gopherd", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("list command failed: %v\n%s", err, listOut)
	}
	list := string(listOut)
	if !strings.Contains(list, "svc-a") {
		t.Errorf("expected svc-a in list: %s", list)
	}
	if !strings.Contains(list, "svc-b") {
		t.Errorf("expected svc-b in list after reload: %s", list)
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
