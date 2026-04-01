package e2e

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSyslogTarget(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo syslog-test-payload && sleep 2"]
    on-success: ignore
    on-failure: ignore

  - name: test-runner
    command: /bin/sh
    args: ["-c", "sleep 4 && grep -q syslog-test-payload /test/syslog.log"]
    on-success: success-shutdown
    on-failure: failure-shutdown

log-targets:
  test-syslog:
    type: syslog
    location: tcp://127.0.0.1:5514
`,
		"run.sh": `#!/bin/sh
# Start TCP syslog receiver before gopherd (NewTarget dials on init).
while true; do nc -l -p 5514 >> /test/syslog.log 2>&1; done &
sleep 0.3
exec /usr/local/bin/gopherd
`,
	}, 20*time.Second)

	if code != 0 {
		syslog := ""
		if data, err := os.ReadFile(filepath.Join(dir, "syslog.log")); err == nil {
			syslog = string(data)
		}
		t.Fatalf("exit %d\nsyslog.log:\n%s\noutput:\n%s", code, syslog, out)
	}
}

func TestSyslogServicesFilter(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: included
    command: /bin/sh
    args: ["-c", "echo INCLUDED_MSG && sleep 2"]
    on-success: ignore
    on-failure: ignore

  - name: excluded
    command: /bin/sh
    args: ["-c", "echo EXCLUDED_MSG && sleep 2"]
    on-success: ignore
    on-failure: ignore

  - name: test-runner
    command: /bin/sh
    args: ["-c", "sleep 4 && grep -q INCLUDED_MSG /test/syslog.log && ! grep -q EXCLUDED_MSG /test/syslog.log"]
    on-success: success-shutdown
    on-failure: failure-shutdown

log-targets:
  filtered:
    type: syslog
    location: tcp://127.0.0.1:5514
    services: [included]
`,
		"run.sh": `#!/bin/sh
while true; do nc -l -p 5514 >> /test/syslog.log 2>&1; done &
sleep 0.3
exec /usr/local/bin/gopherd
`,
	}, 20*time.Second)

	if code != 0 {
		syslog := ""
		if data, err := os.ReadFile(filepath.Join(dir, "syslog.log")); err == nil {
			syslog = string(data)
		}
		t.Fatalf("exit %d\nsyslog.log:\n%s\noutput:\n%s", code, syslog, out)
	}
}

func TestSyslogLabels(t *testing.T) {
	dir, code, out := runContainer(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo label-test-msg && sleep 2"]
    on-success: ignore
    on-failure: ignore

  - name: test-runner
    command: /bin/sh
    args: ["-c", "sleep 4 && grep -q label-test-msg /test/syslog.log"]
    on-success: success-shutdown
    on-failure: failure-shutdown

log-targets:
  labeled:
    type: syslog
    location: tcp://127.0.0.1:5514
    labels:
      env: test
      region: us-east-1
`,
		"run.sh": `#!/bin/sh
while true; do nc -l -p 5514 >> /test/syslog.log 2>&1; done &
sleep 0.3
exec /usr/local/bin/gopherd
`,
	}, 20*time.Second)

	if code != 0 {
		syslog := ""
		if data, err := os.ReadFile(filepath.Join(dir, "syslog.log")); err == nil {
			syslog = string(data)
		}
		t.Fatalf("exit %d\nsyslog.log:\n%s\noutput:\n%s", code, syslog, out)
	}
}
