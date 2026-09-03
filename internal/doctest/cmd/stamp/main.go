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

// stamp appends its start time to a file and exits, for tests that need to
// measure when the daemon spawned something.
//
// Usage: stamp <file> [exit-code]
//
// The time is written as decimal nanoseconds since the epoch: exact, and
// parseable with strconv.ParseInt. The shell equivalent, `date +%s.%N`, is
// GNU-only -- busybox leaves %N unexpanded, so on Alpine it silently degrades
// to whole seconds and any sub-second measurement built on it is wrong.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

func main() {
	now := time.Now()
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: stamp <file> [exit-code]")
		os.Exit(2)
	}
	code := 0
	if len(os.Args) > 2 {
		var err error
		if code, err = strconv.Atoi(os.Args[2]); err != nil {
			fmt.Fprintf(os.Stderr, "stamp: bad exit code %q: %v\n", os.Args[2], err)
			os.Exit(2)
		}
	}
	// O_APPEND keeps a short write atomic, so repeated spawns cannot interleave.
	f, err := os.OpenFile(os.Args[1], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stamp: %v\n", err)
		os.Exit(2)
	}
	if _, err := fmt.Fprintf(f, "%d\n", now.UnixNano()); err != nil {
		fmt.Fprintf(os.Stderr, "stamp: %v\n", err)
		os.Exit(2)
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "stamp: %v\n", err)
		os.Exit(2)
	}
	os.Exit(code)
}
