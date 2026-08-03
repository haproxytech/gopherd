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

package logstreaming

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/haproxytech/gopherd/internal/doctest"
)

// History mode returns buffered tick lines; follow mode streams new ones live.
func TestLogStreaming(t *testing.T) {
	d := doctest.RunFile(t, "example.yml", doctest.Options{})

	d.WaitRunning("ticker", 5*time.Second)
	time.Sleep(500 * time.Millisecond) // let a few ticks accumulate

	// ring-buffer history (no -f): returns and closes
	if resp := d.Command("logs ticker"); !strings.Contains(resp, "tick") {
		t.Fatalf("expected tick lines in history, got: %s", resp)
	}

	// follow mode: raw connection, expect several live lines
	conn, err := net.DialTimeout("unix", d.SocketPath(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial socket: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	fmt.Fprintf(conn, "logs ticker -f\n")

	got := 0
	sc := bufio.NewScanner(conn)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "tick") {
			got++
		}
		// history is served first; 8 lines guarantees live ones (ring holds
		// ~2-3 ticks after 500ms, new ticks arrive every 200ms)
		if got >= 8 {
			break
		}
	}
	if got < 8 {
		t.Fatalf("expected 8 streamed tick lines, got %d (err: %v)", got, sc.Err())
	}

	if code := d.Stop(); code != 0 {
		t.Errorf("expected clean exit 0, got %d", code)
	}
}
