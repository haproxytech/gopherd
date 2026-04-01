package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSIGINT(t *testing.T) {
	dc := runDetached(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: app
    command: sleep
    args: ["300"]
    on-failure: shutdown
`,
	})
	defer dc.remove()

	time.Sleep(2 * time.Second)
	dc.signal("INT")
	code := dc.wait(10 * time.Second)
	if code != 0 {
		t.Errorf("expected exit 0 after SIGINT, got %d\nlogs:\n%s", code, dc.logs())
	}
}

func TestSIGTERM(t *testing.T) {
	dc := runDetached(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: app
    command: sleep
    args: ["300"]
    on-failure: shutdown
`,
	})
	defer dc.remove()

	time.Sleep(2 * time.Second)
	dc.signal("TERM")
	code := dc.wait(10 * time.Second)
	if code != 0 {
		t.Errorf("expected exit 0 after SIGTERM, got %d\nlogs:\n%s", code, dc.logs())
	}
}

func TestSIGHUPReload(t *testing.T) {
	dc := runDetached(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: original
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`,
	})
	defer dc.remove()

	time.Sleep(2 * time.Second)

	// Update config file, then send SIGHUP.
	newCfg := `no-logo: true
processes:
  - name: original
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown

  - name: added
    command: sleep
    args: ["300"]
    on-success: ignore
    on-failure: shutdown
`
	os.WriteFile(filepath.Join(dc.dir, "gopherd.yml"), []byte(newCfg), 0o644)

	dc.signal("HUP")
	time.Sleep(2 * time.Second)

	logs := dc.logs()
	if !strings.Contains(logs, "reload: ok") {
		t.Errorf("expected 'reload: ok' in logs after SIGHUP:\n%s", logs)
	}

	dc.signal("TERM")
	dc.wait(10 * time.Second)
}

func TestSignalForwardUSR1(t *testing.T) {
	dc := runDetached(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: trapper
    command: /bin/sh
    args: ["-c", "trap 'echo GOT_USR1 > /test/signal.log' USR1; while true; do sleep 0.1; done"]
    on-success: ignore
    on-failure: ignore

  - name: keeper
    command: /bin/sh
    args: ["-c", "trap '' USR1; sleep 300"]
    on-failure: shutdown
`,
	})
	defer dc.remove()

	time.Sleep(2 * time.Second)
	dc.signal("USR1")
	time.Sleep(2 * time.Second)

	data, err := os.ReadFile(filepath.Join(dc.dir, "signal.log"))
	if err != nil {
		t.Fatalf("signal.log not created (signal not forwarded?)\nlogs:\n%s", dc.logs())
	}
	if !strings.Contains(string(data), "GOT_USR1") {
		t.Errorf("expected GOT_USR1, got: %s", data)
	}

	dc.signal("TERM")
	dc.wait(10 * time.Second)
}

func TestSignalForwardUSR2(t *testing.T) {
	dc := runDetached(t, map[string]string{
		"gopherd.yml": `
no-logo: true
processes:
  - name: trapper
    command: /bin/sh
    args: ["-c", "trap 'echo GOT_USR2 > /test/signal.log' USR2; while true; do sleep 0.1; done"]
    on-success: ignore
    on-failure: ignore

  - name: keeper
    command: /bin/sh
    args: ["-c", "trap '' USR2; sleep 300"]
    on-failure: shutdown
`,
	})
	defer dc.remove()

	time.Sleep(2 * time.Second)
	dc.signal("USR2")
	time.Sleep(2 * time.Second)

	data, err := os.ReadFile(filepath.Join(dc.dir, "signal.log"))
	if err != nil {
		t.Fatalf("signal.log not created (USR2 not forwarded?)\nlogs:\n%s", dc.logs())
	}
	if !strings.Contains(string(data), "GOT_USR2") {
		t.Errorf("expected GOT_USR2, got: %s", data)
	}

	dc.signal("TERM")
	dc.wait(10 * time.Second)
}
