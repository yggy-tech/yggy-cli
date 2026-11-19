package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yggy-tech/yggy-cli/internal/update"
)

// updateCheckEvery is how long the newest release found stays good before yggy asks GitHub again.
const updateCheckEvery = 24 * time.Hour

// updateCheckTimeout bounds the release lookup, so a slow network never holds up a command.
const updateCheckTimeout = 2 * time.Second

// Commands that never get the update notice: they are the update itself, or print only facts.
var noUpdateNotice = map[string]bool{
	"update": true, "upgrade": true, "version": true, "-v": true, "--version": true,
	"help": true, "-h": true, "--help": true,
}

type updateCheck struct {
	CheckedAt time.Time `json:"checkedAt"`
	Latest    string    `json:"latest"`
}

// offerUpdate says when a newer release exists. In a terminal it offers to install it right away and, after an
// update, runs the command again on the new release. It returns the exit code of that run, or -1 to go on as usual.
func (a *App) offerUpdate(args []string) int {
	if a.Updater == nil || a.Version == "" || a.Version == "dev" || noUpdateNotice[args[0]] {
		return -1
	}
	if a.env("YGGY_NO_UPDATE_CHECK") != "" || a.env("CI") != "" {
		return -1
	}
	latest := a.latestRelease()
	if latest == "" || !update.Newer(a.Version, latest) {
		return -1
	}
	if !a.Interactive {
		fmt.Fprintf(a.Err, "yggy %s is available (you have %s). Run yggy update\n", latest, a.Version)
		return -1
	}
	answer, err := a.promptLine(fmt.Sprintf("yggy %s is available (you have %s). Update now? [Y/n]: ", latest, a.Version))
	if err != nil || (answer != "" && !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes")) {
		return -1
	}
	exe, err := a.exePath()
	if err == nil {
		err = a.Updater.Install(context.Background(), latest, exe)
	}
	if err != nil {
		fmt.Fprintf(a.Err, "update failed, going on with %s: %v\n", a.Version, err)
		return -1
	}
	fmt.Fprintf(a.Err, "updated yggy %s -> %s\n", a.Version, latest)
	if a.Restart == nil {
		return -1
	}
	return a.Restart(exe, args)
}

// latestRelease returns the newest release, from the cache next to the config when it is fresh. Any failure is
// silent: the notice is a convenience and never stops a command.
func (a *App) latestRelease() string {
	cache := ""
	if a.ConfigPath != "" {
		cache = filepath.Join(filepath.Dir(a.ConfigPath), "update-check.json")
		if data, err := os.ReadFile(cache); err == nil {
			var held updateCheck
			if json.Unmarshal(data, &held) == nil && a.now().Sub(held.CheckedAt) < updateCheckEvery {
				return held.Latest
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	latest, err := a.Updater.Latest(ctx)
	if err != nil {
		return ""
	}
	if cache != "" {
		if data, err := json.Marshal(updateCheck{CheckedAt: a.now(), Latest: latest}); err == nil {
			_ = os.MkdirAll(filepath.Dir(cache), 0o700)
			_ = os.WriteFile(cache, data, 0o600)
		}
	}
	return latest
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}
