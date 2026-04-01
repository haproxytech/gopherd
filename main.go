// Package main implements gopherd, a minimal PID 1 init process and service supervisor.
package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/haproxytech/gopherd/control"
	"github.com/haproxytech/gopherd/version"
)

const defaultConfigPath = "/var/lib/gopherd/gopherd.yml"

func main() {
	log.SetFlags(0)
	log.SetPrefix("gopherd: ")

	_ = version.Set()

	// Split os.Args on "--": everything after is entrypoint extra args.
	// Also, if the first arg starts with "-", it's neither a client command
	// nor a passthrough binary — treat all args as entrypoint args.
	var entrypointArgs []string
	programArgs := os.Args[1:]
	if len(programArgs) > 0 && strings.HasPrefix(programArgs[0], "-") {
		// All flag-style args belong to the entrypoint target service.
		// Strip a leading "--" separator if present.
		if programArgs[0] == "--" {
			entrypointArgs = programArgs[1:]
		} else {
			entrypointArgs = programArgs
		}
		programArgs = nil
	} else {
		for i, arg := range programArgs {
			if arg == "--" {
				entrypointArgs = programArgs[i+1:]
				programArgs = programArgs[:i]
				break
			}
		}
	}

	// CLI client mode or passthrough exec.
	if len(programArgs) > 0 {
		first := programArgs[0]
		if control.IsClientCommand(programArgs) {
			control.RunClient(programArgs)
			return
		}
		if first == "version" {
			fmt.Println("gopherd", version.Version)
			fmt.Println("built from:", version.Repo)
			fmt.Println("commit date:", version.CommitDate)
			return
		}
		if first == "tag" {
			fmt.Println(version.Tag)
			return
		}
		// Passthrough: exec the command directly, replacing this process.
		path, err := exec.LookPath(first)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gopherd: %q not found (not a client command and not on PATH)\n", first)
			fmt.Fprintf(os.Stderr, "Client commands: %s\n", strings.Join(control.ClientCommandList(), ", "))
			os.Exit(1)
		}
		// Re-append entrypoint args for passthrough.
		execArgs := programArgs
		if len(entrypointArgs) > 0 {
			execArgs = append(execArgs, entrypointArgs...)
		}
		if err := syscall.Exec(path, execArgs, os.Environ()); err != nil {
			log.Fatalf("exec %s: %v", path, err)
		}
	}

	os.Exit(run(entrypointArgs))
}
