package control

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strings"
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
	return out
}

// IsAlive checks whether a gopherd daemon is reachable on the given socket path.
func IsAlive(socketPath string) bool {
	conn, err := net.Dial("unix", socketPath)
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

	conn, err := net.Dial("unix", socketPath)
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
	if hasError {
		os.Exit(1)
	}
}
