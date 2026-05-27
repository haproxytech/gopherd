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

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2EEntrypointArgs(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "args.log")

	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo $0 $@ > %s && sleep 300"]
    use-entrypoint-args: true
    on-failure: shutdown
`, marker), "--", "--flag1", "value1")
	defer td.kill()

	time.Sleep(1 * time.Second)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	content := strings.TrimSpace(string(data))
	if !strings.Contains(content, "--flag1") || !strings.Contains(content, "value1") {
		t.Errorf("expected entrypoint args in output, got: %s", content)
	}

	td.stop()
}

func TestE2EEnvironment(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "env.log")

	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo $MY_VAR > %s && sleep 300"]
    environment:
      MY_VAR: hello-e2e
    on-failure: shutdown
`, marker))
	defer td.kill()

	time.Sleep(1 * time.Second)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read env log: %v", err)
	}
	if !strings.Contains(string(data), "hello-e2e") {
		t.Errorf("expected MY_VAR=hello-e2e, got: %s", data)
	}

	td.stop()
}

func TestE2EEnvironmentTemplate(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "tmpl.log")

	// Template expansion ({{.VAR}}) applies to args, not env values.
	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo {{.MY_NAME}} > %s && sleep 300"]
    environment:
      MY_NAME: world
    on-failure: shutdown
`, marker))
	defer td.kill()

	time.Sleep(1 * time.Second)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read template log: %v", err)
	}
	if !strings.Contains(string(data), "world") {
		t.Errorf("expected 'world' from template expansion, got: %s", data)
	}

	td.stop()
}

func TestE2EDotEnv(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, "app.env")
	marker := filepath.Join(dir, "dotenv.log")

	os.WriteFile(envFile, []byte("FROM_DOTENV=loaded\n"), 0o644)

	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "echo $FROM_DOTENV > %s && sleep 300"]
    dotenv: %s
    on-failure: shutdown
`, marker, envFile))
	defer td.kill()

	time.Sleep(1 * time.Second)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read dotenv log: %v", err)
	}
	if !strings.Contains(string(data), "loaded") {
		t.Errorf("expected FROM_DOTENV=loaded, got: %s", data)
	}

	td.stop()
}

func TestE2EWorkingDir(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "workdir")
	os.Mkdir(workDir, 0o755)
	marker := filepath.Join(dir, "wd.log")

	td := startDaemon(t, fmt.Sprintf(`
processes:
  - name: app
    command: /bin/sh
    args: ["-c", "pwd > %s && sleep 300"]
    working-dir: %s
    on-failure: shutdown
`, marker, workDir))
	defer td.kill()

	time.Sleep(1 * time.Second)

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read wd log: %v", err)
	}
	if !strings.Contains(string(data), workDir) {
		t.Errorf("expected working dir %s, got: %s", workDir, data)
	}

	td.stop()
}
