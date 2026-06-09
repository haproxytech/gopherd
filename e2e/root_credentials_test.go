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

// Tests in this file require gopherd running as root (default in Docker)
// to exercise credential switching (user/group).

func TestRootUserGroupByName(t *testing.T) {
	requireRoot(t)
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: whoami
    command: /bin/sh
    args: ["-c", "id"]
    user: testuser
    group: testgroup
    startup: oneshot

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [whoami]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "uid=1234(testuser)") {
		t.Errorf("expected uid=1234(testuser) in output:\n%s", out)
	}
	if !strings.Contains(out, "gid=1234(testgroup)") {
		t.Errorf("expected gid=1234(testgroup) in output:\n%s", out)
	}
}

func TestRootUserGroupByID(t *testing.T) {
	requireRoot(t)
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: whoami
    command: /bin/sh
    args: ["-c", "id"]
    user-id: 1234
    group-id: 1234
    startup: oneshot

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [whoami]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "uid=1234") {
		t.Errorf("expected uid=1234 in output:\n%s", out)
	}
	if !strings.Contains(out, "gid=1234") {
		t.Errorf("expected gid=1234 in output:\n%s", out)
	}
}

func TestRootUserOnlyGroupInherited(t *testing.T) {
	requireRoot(t)
	// When only user is specified (no group), the user's primary group
	// should be used automatically.
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: whoami
    command: /bin/sh
    args: ["-c", "id"]
    user: testuser
    startup: oneshot

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [whoami]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	// testuser's primary group is testgroup (gid=1234).
	if !strings.Contains(out, "uid=1234(testuser)") {
		t.Errorf("expected uid=1234(testuser):\n%s", out)
	}
	if !strings.Contains(out, "gid=1234(testgroup)") {
		t.Errorf("expected gid=1234(testgroup) as inherited primary group:\n%s", out)
	}
}

func TestRootGroupOnlyUIDPreserved(t *testing.T) {
	requireRoot(t)
	// When only group is specified (no user), the current UID (root=0)
	// should be preserved, but GID should change.
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: whoami
    command: /bin/sh
    args: ["-c", "id"]
    group: testgroup
    startup: oneshot

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [whoami]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "uid=0(root)") {
		t.Errorf("expected uid=0(root) preserved:\n%s", out)
	}
	if !strings.Contains(out, "gid=1234(testgroup)") {
		t.Errorf("expected gid=1234(testgroup):\n%s", out)
	}
}

func TestRootOneshotAsUser(t *testing.T) {
	requireRoot(t)
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: init-as-user
    command: /bin/sh
    args: ["-c", "whoami"]
    user: testuser
    startup: oneshot

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [init-as-user]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "testuser") {
		t.Errorf("expected 'testuser' in whoami output:\n%s", out)
	}
}

func TestRootMixedUserServices(t *testing.T) {
	requireRoot(t)
	// Different services run as different users within the same daemon.
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: as-root
    command: /bin/sh
    args: ["-c", "id > /test/root-id.log"]
    startup: oneshot

  - name: as-testuser
    command: /bin/sh
    args: ["-c", "id > /test/user-id.log"]
    user: testuser
    group: testgroup
    startup: oneshot
    after: [as-root]

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [as-testuser]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}

	rootID := readTestFile(t, dir, "root-id.log")
	if !strings.Contains(rootID, "uid=0(root)") {
		t.Errorf("expected root in root-id.log: %s", rootID)
	}

	userID := readTestFile(t, dir, "user-id.log")
	if !strings.Contains(userID, "uid=1234(testuser)") {
		t.Errorf("expected testuser in user-id.log: %s", userID)
	}
}

func TestRootFileOwnership(t *testing.T) {
	requireRoot(t)
	// Service running as testuser writes a file; verify it's owned by testuser.
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: writer
    command: /bin/sh
    args: ["-c", "echo hello > /test/owned.txt && stat -c '%U:%G' /test/owned.txt > /test/owner.log"]
    user: testuser
    group: testgroup
    startup: oneshot

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [writer]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}

	owner := strings.TrimSpace(readTestFile(t, dir, "owner.log"))
	if owner != "testuser:testgroup" {
		t.Errorf("expected file owned by testuser:testgroup, got: %s", owner)
	}
}

func TestRootWriteToRootPath(t *testing.T) {
	requireRoot(t)
	// Root service can write to /etc, non-root service cannot.
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: root-writer
    command: /bin/sh
    args: ["-c", "echo root-was-here > /etc/gopherd-test && echo ROOT_OK"]
    startup: oneshot

  - name: user-writer
    command: /bin/sh
    args: ["-c", "echo user-was-here > /etc/gopherd-test2 2>&1 && echo USER_OK || echo USER_DENIED"]
    user: testuser
    startup: oneshot
    on-failure: ignore
    after: [root-writer]

  - name: done
    command: /bin/sh
    args: ["-c", "exit 0"]
    after: [user-writer]
    on-success: success-shutdown
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "ROOT_OK") {
		t.Errorf("expected root write to succeed:\n%s", out)
	}
	if !strings.Contains(out, "USER_DENIED") {
		t.Errorf("expected non-root write to /etc to fail:\n%s", out)
	}
}

func TestRootPrivilegedPort(t *testing.T) {
	requireRoot(t)
	// Only root can bind to port 80. Verify a root service can do this.
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
    args: ["-c", "echo PRIVILEGED_PORT_OK"]
    after: [web]
    ready-check: web-http
    ready-timeout: 10s
    on-success: success-shutdown
    on-failure: failure-shutdown

checks:
  web-http:
    http:
      url: http://127.0.0.1:80/health
    period: 1s
    timeout: 2s
    threshold: 1
    level: ready
`,
		"http-server.sh": `#!/bin/sh
while true; do
  printf "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok" | nc -l -p 80 2>/dev/null
done
`,
	}, 20*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "PRIVILEGED_PORT_OK") {
		t.Errorf("expected privileged port bind to succeed:\n%s", out)
	}
}

func TestRootKillDelay(t *testing.T) {
	// Service traps and ignores SIGTERM. With kill-delay: 1s, gopherd
	// must send SIGKILL after 1s to actually stop it.
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: keeper
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown

  - name: stubborn
    command: /bin/sh
    args: ["-c", "trap '' TERM; while true; do sleep 0.1; done"]
    kill-delay: 1s
    on-success: ignore
    on-failure: ignore

  - name: test-runner
    command: /test/assert.sh
    after: [stubborn]
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
		"assert.sh": `#!/bin/sh
sleep 1

# Stop stubborn using <service> <action> form.
# It ignores SIGTERM; gopherd must SIGKILL after kill-delay (1s).
/usr/local/bin/gopherd stubborn stop
echo "stop command sent"

# Wait for SIGKILL to take effect.
sleep 3

status=$(/usr/local/bin/gopherd stubborn status 2>&1)
echo "status: $status"
echo "$status" | grep -q "stopped" || { echo "FAIL: stubborn not stopped"; exit 1; }
echo "PASS: kill-delay worked"
`,
	}, 20*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "PASS: kill-delay worked") {
		t.Errorf("kill-delay test inconclusive:\n%s", out)
	}
}

func TestRootZombieReap(t *testing.T) {
	// Verify gopherd as PID 1 reaps orphaned zombie processes.
	_, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: zombie-maker
    command: /test/make-zombies.sh
    on-success: success-shutdown
    on-failure: failure-shutdown
`,
		"make-zombies.sh": `#!/bin/sh
# Fork a subprocess that spawns children and exits immediately.
# The children become orphans adopted by PID 1 (gopherd).
# When they exit, PID 1 should reap them (no zombies left).
/bin/sh -c '
  for i in 1 2 3 4 5; do sleep 0.2 & done
  exit 0
' &

# Wait for all orphans to finish.
sleep 3

# Check for zombie processes in /proc.
found=0
for f in /proc/[0-9]*/status; do
  if grep -q "^State:.*Z" "$f" 2>/dev/null; then
    echo "zombie: $f"
    found=1
  fi
done

if [ "$found" -eq 1 ]; then
  echo "FAIL: zombies found"
  exit 1
fi
echo "PASS: all zombies reaped"
exit 0
`,
	}, 15*time.Second)

	if code != 0 {
		t.Fatalf("exit %d\noutput:\n%s", code, out)
	}
	if !strings.Contains(out, "PASS: all zombies reaped") {
		t.Errorf("zombie reap check inconclusive:\n%s", out)
	}
}

func TestRootPID1ExitCode(t *testing.T) {
	_, code, _ := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: failer
    command: /bin/sh
    args: ["-c", "exit 42"]
    on-failure: shutdown
`,
	}, 15*time.Second)

	if code != 42 {
		t.Errorf("expected exit code 42 from PID 1, got %d", code)
	}
}
