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

// sdnotifyready is a doc-test stand-in daemon with sd_notify support: it
// sends READY=1 to $NOTIFY_SOCKET (abstract or path form) and stays running.
package main

import (
	"log"
	"net"
	"os"
	"strings"
	"time"
)

func main() {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		log.Fatal("NOTIFY_SOCKET not set")
	}
	// "@name" is the userland spelling of the abstract namespace (leading NUL).
	if strings.HasPrefix(addr, "@") {
		addr = "\x00" + addr[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		log.Fatalf("dial %q: %v", addr, err)
	}
	if _, err := conn.Write([]byte("READY=1")); err != nil {
		log.Fatalf("send READY=1: %v", err)
	}
	conn.Close()
	for {
		time.Sleep(time.Hour)
	}
}
