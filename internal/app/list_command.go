package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/yggy-tech/yggy-cli/internal/pick"
)

var documentStatuses = []string{"pending", "processing", "completed", "failed", "stored"}

// listOrgs runs `yggy orgs`. The selected organization is marked with *.
func (a *App) listOrgs() int {
	file, code := a.load()
	if code != 0 {
		return code
	}
	orgs, _, err := a.client(file).Account(context.Background())
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	if len(orgs) == 0 {
		fmt.Fprintln(a.Err, "this key has no organizations")
		return 0
	}
	a.printItems(orgItems(orgs), file.Org.ID)
	return 0
}

// listProjects runs `yggy projects`. The selected project is marked with *.
func (a *App) listProjects() int {
	file, code := a.orgContext()
	if code != 0 {
		return code
	}
	projects, err := a.client(file).Projects(context.Background(), file.Org.Header())
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	if len(projects) == 0 {
		fmt.Fprintln(a.Err, "this organization has no projects you can see")
		return 0
	}
	items := make([]pick.Item, len(projects))
	for i, project := range projects {
		items[i] = pick.Item{ID: project.ID, Slug: project.Slug, Name: project.Name}
	}
	a.printItems(items, file.Project.ID)
	return 0
}

func (a *App) printItems(items []pick.Item, selectedID string) {
	w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	for _, item := range items {
		mark := " "
		if item.ID == selectedID {
			mark = "*"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", mark, firstNonEmpty(item.Slug, item.ID), item.Name)
	}
	w.Flush()
}

// listDocuments runs `yggy documents list`, newest first.
func (a *App) listDocuments(args []string) int {
	fs := a.newFlags("documents")
	status := fs.String("status", "", "pending, processing, completed, failed or stored")
	limit := fs.Int("limit", 20, "how many documents to show, 1 to 500")
	asJSON := fs.Bool("json", false, "print the API response")
	rest, err := parseFlags(fs, args)
	if err != nil {
		return flagStatus(err)
	}
	if len(rest) > 0 {
		return a.misuse("documents")
	}
	*status = strings.ToLower(*status)
	if *status != "" && !slices.Contains(documentStatuses, *status) {
		fmt.Fprintf(a.Err, "--status is one of %s\n", strings.Join(documentStatuses, ", "))
		return 2
	}
	if *limit < 1 || *limit > 500 {
		fmt.Fprintln(a.Err, "--limit is between 1 and 500")
		return 2
	}
	file, code := a.projectContext()
	if code != 0 {
		return code
	}
	list, err := a.client(file).Documents(context.Background(), file.Org.Header(), file.Project.ID, *status, *limit)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	if *asJSON {
		return a.printJSON(list.Raw)
	}
	if len(list.Documents) == 0 {
		fmt.Fprintln(a.Err, "no documents")
		return 0
	}
	w := tabwriter.NewWriter(a.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tTRIPLES\tTITLE")
	for _, document := range list.Documents {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", document.ID, strings.ToLower(document.Status), document.TripleCount, oneLine(document.Title))
	}
	w.Flush()
	if list.Total > len(list.Documents) {
		fmt.Fprintf(a.Err, "showing %d of %d. Use --limit for more\n", len(list.Documents), list.Total)
	}
	return 0
}
