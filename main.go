package main

import (
	"fmt"
	"os"
	"runtime"

	"github.com/yggy-tech/yggy-cli/internal/app"
	"github.com/yggy-tech/yggy-cli/internal/browser"
	"github.com/yggy-tech/yggy-cli/internal/config"
	"github.com/yggy-tech/yggy-cli/internal/update"
	"github.com/yggy-tech/yggy-cli/internal/version"
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
		Styled:      term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == "",
		Version:     version.Version,
		Updater:     update.New(runtime.GOOS, runtime.GOARCH),
		OpenBrowser: browser.Open,
	}).Run(os.Args[1:]))
}
