//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// restart runs the updated binary as a child, because Windows cannot replace a running process.
func restart(exe string, args []string) int {
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not start the updated yggy, run the command again: %v\n", err)
		return 1
	}
	return 0
}
