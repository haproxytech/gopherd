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
	"syscall"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// testBinary is the path to the built gopherd binary, set by TestMain.
var testBinary string

func TestMain(m *testing.M) {
	bin, err := doctest.BuildBinary()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build failed: %v\n", err)
		os.Exit(1)
	}
	testBinary = bin
	os.Exit(m.Run())
}

// testDaemon wraps doctest.Daemon, preserving the legacy method names the
// existing e2e_*_test.go files call.
type testDaemon struct{ *doctest.Daemon }

func startDaemon(t *testing.T, config string, extraArgs ...string) *testDaemon {
	t.Helper()
	d := doctest.RunConfig(t, config, doctest.Options{ExtraArgs: extraArgs})
	return &testDaemon{d}
}

func (td *testDaemon) sendCommand(cmd string) string  { return td.Command(cmd) }
func (td *testDaemon) signal(sig syscall.Signal)      { td.Signal(sig) }
func (td *testDaemon) wait(timeout time.Duration) int { return td.Wait(timeout) }
func (td *testDaemon) kill()                          { td.Kill() }
func (td *testDaemon) stop() int                      { return td.Stop() }
func (td *testDaemon) daemonAlive() bool              { return td.Alive() }
func (td *testDaemon) updateConfig(config string)     { td.UpdateConfig(config) }
