package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/yggy-tech/yggy-cli/internal/migration"
	"github.com/yggy-tech/yggy-cli/internal/pick"
)

func (a *App) migrationCommand(args []string) int {
	if len(args) == 0 || args[0] != "upload" {
		return a.misuse("migration")
	}
	return a.migrationUpload(args[1:])
}

// migrationUpload runs `yggy migration upload <project> <dir>`.
func (a *App) migrationUpload(args []string) int {
	fs := a.newFlags("migration")
	snapshot := fs.String("snapshot", "", "snapshot label or id; opened when it does not exist")
	capturedOn := fs.String("capture-date", "", "the day the export was taken, YYYY-MM-DD (default today)")
	scope := fs.String("scope", "", "what the export covers, in words")
	closeDrop := fs.Bool("close", false, "close the drop when every file is complete")
	parallel := fs.Int("parallel", migration.DefaultParallel, "files to upload at once, 1 to 8 (about 64 MiB of memory each)")
	asJSON := fs.Bool("json", false, "print the result as JSON")
	quiet := fs.Bool("quiet", false, "print only errors")
	rest, err := parseFlags(fs, args)
	if err != nil {
		return flagStatus(err)
	}
	if len(rest) != 2 {
		return a.misuse("migration")
	}
	if *parallel < 1 || *parallel > 8 {
		fmt.Fprintln(a.Err, "--parallel is between 1 and 8")
		return 2
	}
	file, code := a.orgContext()
	if code != 0 {
		return code
	}
	projects, err := a.client(file).Projects(context.Background(), file.Org.Header())
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	items := make([]pick.Item, len(projects))
	for i, project := range projects {
		items[i] = pick.Item{ID: project.ID, Slug: project.Slug, Name: project.Name}
	}
	project, err := a.resolve(items, rest[0], "project", "slug")
	if err != nil {
		return choiceStatus(err)
	}

	progress := func(line string) {
		if !*quiet && !*asJSON {
			fmt.Fprintln(a.Err, line)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first signal the default behaviour returns, so a second Ctrl-C ends the program at once.
	go func() {
		<-ctx.Done()
		stop()
	}()
	client := &migration.Client{BaseURL: file.BaseURL, APIKey: file.APIKey, Org: file.Org.Header(), ProjectID: project.ID}
	result, err := migration.Run(ctx, client, migration.Options{
		Dir:        rest[1],
		Snapshot:   *snapshot,
		CapturedOn: *capturedOn,
		Scope:      *scope,
		Close:      *closeDrop,
		Parallel:   *parallel,
		Progress:   progress,
	})
	if len(result.Files) == 0 {
		if err != nil {
			fmt.Fprintln(a.Err, err)
			return 1
		}
		return 0
	}
	status := a.reportUpload(result, *asJSON, *quiet)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	return status
}

// reportUpload prints the summary and returns the exit status.
func (a *App) reportUpload(result migration.Result, asJSON, quiet bool) int {
	failed := result.Failed()
	if asJSON {
		raw, _ := json.Marshal(result)
		if code := a.printJSON(raw); code != 0 {
			return code
		}
	} else if !quiet {
		uploaded := 0
		for _, f := range result.Files {
			if f.Status == migration.StatusUploaded {
				uploaded++
			}
		}
		fmt.Fprintf(a.Out, "snapshot %s: %d of %d files uploaded", result.Snapshot.Label, uploaded, len(result.Files))
		if rest := len(result.Files) - uploaded - failed; rest > 0 {
			fmt.Fprintf(a.Out, ", %d already there or empty", rest)
		}
		if failed > 0 {
			fmt.Fprintf(a.Out, ", %d failed", failed)
		}
		fmt.Fprintln(a.Out)
		for _, f := range result.Files {
			if f.Status == migration.StatusFailed {
				fmt.Fprintf(a.Out, "  failed  %s  %s\n", f.Path, f.Error)
			}
		}
		switch {
		case result.Closed:
			fmt.Fprintln(a.Out, "drop closed")
		case result.CloseSkipped != "":
			fmt.Fprintf(a.Out, "drop left open: %s\n", result.CloseSkipped)
		}
		if failed > 0 {
			fmt.Fprintln(a.Out, "run the same command again to continue where it stopped")
		}
	}
	if failed > 0 || strings.TrimSpace(result.CloseSkipped) != "" {
		return 1
	}
	return 0
}
