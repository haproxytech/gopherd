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

// ClientCommandList returns the list of client commands.
func ClientCommandList() []string {
	out := make([]string, 0, len(ClientCommands))
	for k := range ClientCommands {
		out = append(out, k)
	}
	return out
}

// RunClient connects to the go-init control socket and sends a command.
func RunClient(args []string) {
	socketPath := DefaultSocketPath
	if v := os.Getenv("GO_INIT_SOCKET"); v != "" {
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
		fmt.Fprintf(os.Stderr, "usage: go-init <service> <start|stop|restart|status>\n")
		fmt.Fprintf(os.Stderr, "       go-init signal <service> <signal-name>\n")
		fmt.Fprintf(os.Stderr, "       go-init logs <service> [-f]\n")
		fmt.Fprintf(os.Stderr, "       go-init reload\n")
		fmt.Fprintf(os.Stderr, "       go-init stats\n")
		fmt.Fprintf(os.Stderr, "       go-init list\n")
		os.Exit(1)
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot connect to go-init (is it running?): %v\n", err)
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
