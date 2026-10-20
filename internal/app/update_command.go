package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yggy-tech/yggy-cli/internal/update"
)

func (a *App) update(args []string) int {
	fs := a.newFlags("update")
	check := fs.Bool("check", false, "only report whether a newer release exists")
	target := fs.String("version", "", "install this release instead of the latest, for example 0.2.0")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(a.Err, "unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if a.Updater == nil {
		fmt.Fprintln(a.Err, "updating is not available in this build")
		return 1
	}

	ctx := context.Background()
	want := strings.TrimPrefix(strings.TrimSpace(*target), "v")
	if want == "" {
		latest, err := a.Updater.Latest(ctx)
		if err != nil {
			fmt.Fprintln(a.Err, err)
			return 1
		}
		want = latest
		if *check {
			if update.Newer(a.Version, latest) {
				fmt.Fprintf(a.Out, "update available: %s -> %s. Run yggy update\n", a.Version, latest)
			} else {
				fmt.Fprintf(a.Out, "yggy %s is up to date\n", a.Version)
			}
			return 0
		}
		if !update.Newer(a.Version, latest) {
			if a.Version == "dev" {
				fmt.Fprintf(a.Err, "this yggy was built from source. Install release %s with the install script, or pass --version to replace it\n", latest)
				return 1
			}
			fmt.Fprintf(a.Out, "yggy %s is up to date\n", a.Version)
			return 0
		}
	} else if *check {
		fmt.Fprintln(a.Err, "--check cannot be combined with --version")
		return 2
	}

	exe, err := a.exePath()
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	if err := a.Updater.Install(ctx, want, exe); err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	fmt.Fprintf(a.Out, "updated yggy %s -> %s\n", a.Version, want)
	return 0
}

func (a *App) exePath() (string, error) {
	find := a.ExePath
	if find == nil {
		find = os.Executable
	}
	path, err := find()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}
	return path, nil
}
