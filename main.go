package main

import (
	"fmt"
	"os"

	"github.com/yggy-tech/yggy-cli/internal/app"
	"github.com/yggy-tech/yggy-cli/internal/config"
	"golang.org/x/term"
)

func main() {
	path, err := config.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit((&app.App{
		In:          os.Stdin,
		Out:         os.Stdout,
		Err:         os.Stderr,
		InFile:      os.Stdin,
		ConfigPath:  path,
		Env:         os.Getenv,
		Interactive: term.IsTerminal(int(os.Stdin.Fd())),
	}).Run(os.Args[1:]))
}
