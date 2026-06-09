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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var imageName string

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("docker"); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: docker not found, skipping tests")
		os.Exit(0)
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: docker daemon not running, skipping tests")
		os.Exit(0)
	}

	tmpDir, err := os.MkdirTemp("", "gopherd-e2e-docker-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: mktemp: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	// Build static Linux binary.
	binPath := filepath.Join(tmpDir, "gopherd")
	build := exec.Command("go", "build", "-o", binPath, "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: go build failed: %v\n", err)
		os.Exit(1)
	}

	// Copy Dockerfile next to the binary.
	dockerfileSrc, _ := os.ReadFile("Dockerfile")
	os.WriteFile(filepath.Join(tmpDir, "Dockerfile"), dockerfileSrc, 0o644)

	// Build Docker image.
	imageName = fmt.Sprintf("gopherd-e2e-%d", os.Getpid())
	imgBuild := exec.Command("docker", "build", "-t", imageName, tmpDir)
	imgBuild.Stdout = os.Stdout
	imgBuild.Stderr = os.Stderr
	if err := imgBuild.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: docker build failed: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	exec.Command("docker", "rmi", "-f", imageName).Run()
	os.Exit(code)
}

// --- helpers ---

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0o777)
	for name, content := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o777)
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// fixTestFileOwnership chowns bind-mounted test files to root using a
// throwaway container so gopherd's config ownership check passes regardless
// of the host UID that created them. No-op when already running as root.
func fixTestFileOwnership(t *testing.T, dir string) {
	t.Helper()
	if os.Getuid() == 0 {
		return
	}
	cmd := exec.Command("docker", "run", "--rm", "-v", dir+":/test",
		"--entrypoint", "chown", imageName, "-R", "0:0", "/test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixTestFileOwnership: %v\n%s", err, out)
	}
}

type containerOpts struct {
	user      string // --user flag (e.g. "1234:1234" for non-root)
	socket    string // GOPHERD_SOCKET (daemon + client); for rootless, a writable path
	extraArgs []string
}

// Default UID:GID and control socket used when running the suite rootless.
// /run is root-owned so the socket must live on the writable /test mount.
const (
	defaultRootlessUser   = "1234:1234"
	defaultRootlessSocket = "/test/gopherd.sock"
)

// rootlessMode reports whether the whole suite is being re-run as a non-root
// UID (GOPHERD_E2E_ROOTLESS=1). The CI test matrix sets it for the rootless leg.
func rootlessMode() bool {
	return os.Getenv("GOPHERD_E2E_ROOTLESS") == "1"
}

// requireRoot skips a test that genuinely needs UID 0 (e.g. privilege
// dropping) when the suite runs rootless.
func requireRoot(t *testing.T) {
	t.Helper()
	if rootlessMode() {
		t.Skip("requires gopherd running as root; skipped in rootless mode")
	}
}

// applyRootless fills in user/socket defaults when the suite runs rootless and
// the test did not already pin them. A test that sets its own values (the
// dedicated rootless tests) is left untouched.
func applyRootless(o containerOpts) containerOpts {
	if !rootlessMode() {
		return o
	}
	if o.user == "" {
		o.user = defaultRootlessUser
	}
	if o.socket == "" {
		o.socket = defaultRootlessSocket
	}
	return o
}

// runContainer runs gopherd as PID 1 in Docker. If files contains "run.sh",
// that script is used as the entrypoint instead (it should exec gopherd).
func runContainer(t *testing.T, files map[string]string, timeout time.Duration, opts ...containerOpts) (dir string, exitCode int, output string) {
	t.Helper()
	dir = writeFiles(t, files)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	fixTestFileOwnership(t, dir)

	var o containerOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	o = applyRootless(o)

	args := []string{
		"run", "--rm",
		"-v", dir + ":/test",
		"-e", "GOPHERD_CONFIG=/test/gopherd.yml",
	}
	if o.user != "" {
		args = append(args, "--user", o.user)
	}
	if o.socket != "" {
		args = append(args, "-e", "GOPHERD_SOCKET="+o.socket)
	}
	args = append(args, o.extraArgs...)
	if _, ok := files["run.sh"]; ok {
		args = append(args, "--entrypoint", "/test/run.sh")
	}
	args = append(args, imageName)

	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("container timed out after %s\noutput:\n%s", timeout, out)
	}
	exitCode = 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("docker run: %v\noutput:\n%s", err, out)
		}
	}
	return dir, exitCode, string(out)
}

// runGopherdCmd runs gopherd with the given CLI args (not as daemon).
func runGopherdCmd(t *testing.T, args ...string) (int, string) {
	t.Helper()
	dockerArgs := append([]string{"run", "--rm", imageName}, args...)
	cmd := exec.Command("docker", dockerArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), string(out)
		}
		t.Fatalf("docker run: %v", err)
	}
	return 0, string(out)
}

// detachedContainer wraps a background Docker container for signal tests.
type detachedContainer struct {
	id     string
	dir    string
	socket string // GOPHERD_SOCKET for client exec calls; empty uses the default
	t      *testing.T
}

func runDetached(t *testing.T, files map[string]string, opts ...containerOpts) *detachedContainer {
	t.Helper()
	dir := writeFiles(t, files)
	name := fmt.Sprintf("gopherd-e2e-%s-%d", sanitize(t.Name()), time.Now().UnixNano()%100000)

	fixTestFileOwnership(t, dir)

	args := []string{
		"run", "-d", "--name", name,
		"-v", dir + ":/test",
		"-e", "GOPHERD_CONFIG=/test/gopherd.yml",
	}
	var o containerOpts
	if len(opts) > 0 {
		o = opts[0]
	}
	o = applyRootless(o)
	socket := o.socket
	if o.user != "" {
		args = append(args, "--user", o.user)
	}
	if socket != "" {
		args = append(args, "-e", "GOPHERD_SOCKET="+socket)
	}
	args = append(args, o.extraArgs...)
	if _, ok := files["run.sh"]; ok {
		args = append(args, "--entrypoint", "/test/run.sh")
	}
	args = append(args, imageName)

	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker run -d: %v\n%s", err, out)
	}
	return &detachedContainer{
		id:     strings.TrimSpace(string(out)),
		dir:    dir,
		socket: socket,
		t:      t,
	}
}

// exec runs a gopherd client command inside the container, propagating the
// container's GOPHERD_SOCKET (docker exec starts a fresh process that does not
// inherit the daemon's env).
func (dc *detachedContainer) exec(args ...string) (string, error) {
	dc.t.Helper()
	full := []string{"exec"}
	if dc.socket != "" {
		full = append(full, "-e", "GOPHERD_SOCKET="+dc.socket)
	}
	full = append(full, dc.id, "/usr/local/bin/gopherd")
	full = append(full, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	return string(out), err
}

func (dc *detachedContainer) signal(sig string) {
	dc.t.Helper()
	if out, err := exec.Command("docker", "kill", "-s", sig, dc.id).CombinedOutput(); err != nil {
		dc.t.Fatalf("docker kill -s %s: %v\n%s", sig, err, out)
	}
}

func (dc *detachedContainer) wait(timeout time.Duration) int {
	dc.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "wait", dc.id).Output()
	if err != nil {
		dc.t.Fatalf("docker wait: %v", err)
	}
	var code int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &code)
	return code
}

func (dc *detachedContainer) logs() string {
	out, _ := exec.Command("docker", "logs", dc.id).CombinedOutput()
	return string(out)
}

func (dc *detachedContainer) remove() {
	exec.Command("docker", "rm", "-f", dc.id).Run()
}

// writeConfig rewrites the bind-mounted config from inside the container so the
// write runs as root. Writing from the host fails when fixTestFileOwnership has
// chowned the file to root (i.e. when the host UID is non-root).
func (dc *detachedContainer) writeConfig(content string) {
	dc.t.Helper()
	cmd := exec.Command("docker", "exec", "-i", dc.id, "tee", "/test/gopherd.yml")
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		dc.t.Fatalf("writeConfig: %v\n%s", err, out)
	}
}

func sanitize(s string) string {
	r := strings.NewReplacer("/", "-", " ", "-")
	return strings.ToLower(r.Replace(s))
}

func readTestFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}
