//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

// restart replaces this process with the updated binary, so the command runs on the new release.
func restart(exe string, args []string) int {
	err := syscall.Exec(exe, append([]string{exe}, args...), os.Environ())
	fmt.Fprintf(os.Stderr, "could not start the updated yggy, run the command again: %v\n", err)
	return 1
}
