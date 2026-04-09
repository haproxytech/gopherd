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

package control

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"time"
)

// ClientCommands lists valid client-mode commands for disambiguation with passthrough.
var ClientCommands = map[string]bool{
	"list": true, "stats": true, "start": true, "stop": true, "restart": true,
	"status": true, "signal": true, "logs": true, "reload": true,
}

// serviceActions are commands that can appear as the second arg in "<service> <action>" form.
var serviceActions = map[string]bool{
	"start": true, "stop": true, "restart": true, "status": true,
}

// IsClientCommand returns true if args look like a client-mode invocation.
// Handles both "gopherd restart haproxy" and "gopherd haproxy restart" forms.
func IsClientCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if ClientCommands[args[0]] {
		return true
	}
	// "<service> <action>" form: second arg is the action keyword.
	if len(args) >= 2 && serviceActions[args[1]] {
		return true
	}
	return false
}

// ClientCommandList returns the list of client commands.
func ClientCommandList() []string {
	out := make([]string, 0, len(ClientCommands))
	for k := range ClientCommands {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// clientDialTimeout is the maximum time to wait when connecting to the
// control socket. Matches the server's connReadTimeout so a slow-to-accept
// server doesn't stall the CLI or startup probe indefinitely.
const clientDialTimeout = 5 * time.Second

// IsAlive checks whether a gopherd daemon is reachable on the given socket path.
// It dials and immediately closes without sending a command. The server holds a
// connection slot for up to connReadTimeout (5s) per call — acceptable for the
// startup probe use case where IsAlive is called once during initialisation.
func IsAlive(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, clientDialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// RunClient connects to the gopherd control socket and sends a command.
func RunClient(args []string) {
	socketPath := DefaultSocketPath
	if v := os.Getenv("GOPHERD_SOCKET"); v != "" {
		socketPath = v
	}

	var command string
	switch {
	case len(args) == 1 && (args[0] == "list" || args[0] == "stats" || args[0] == "reload"):
		command = args[0]
	case len(args) == 3 && args[0] == "signal":
		command = "signal " + args[1] + " " + args[2]
	case len(args) >= 2 && args[0] == "logs":
		// logs <service> [-f]
		command = strings.Join(args, " ")
	case len(args) == 2:
		svcName := args[0]
		action := args[1]
		switch action {
		case "start", "stop", "restart", "status":
			command = action + " " + svcName
		default:
			fmt.Fprintf(os.Stderr, "unknown action %q (try: start, stop, restart, status)\n", action)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "usage: gopherd <service> <start|stop|restart|status>\n")
		fmt.Fprintf(os.Stderr, "       gopherd signal <service> <signal-name>\n")
		fmt.Fprintf(os.Stderr, "       gopherd logs <service> [-f]\n")
		fmt.Fprintf(os.Stderr, "       gopherd reload\n")
		fmt.Fprintf(os.Stderr, "       gopherd stats\n")
		fmt.Fprintf(os.Stderr, "       gopherd list\n")
		os.Exit(1)
	}

	conn, err := net.DialTimeout("unix", socketPath, clientDialTimeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to gopherd (is it running?): %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "%s\n", command)

	scanner := bufio.NewScanner(conn)
	hasError := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "error:") {
			fmt.Fprintln(os.Stderr, line)
			hasError = true
		} else {
			fmt.Println(line)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "error: reading response: %v\n", err)
		os.Exit(1)
	}
	if hasError {
		os.Exit(1)
	}
}
