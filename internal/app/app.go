package app

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yggy-tech/yggy-cli/internal/api"
	"github.com/yggy-tech/yggy-cli/internal/config"
	"github.com/yggy-tech/yggy-cli/internal/pick"
	"golang.org/x/term"
)

const defaultBaseURL = "https://api.yggy.tech"

type API interface {
	Account(ctx context.Context) ([]api.Org, string, error)
	User(ctx context.Context) (string, error)
	Projects(ctx context.Context, orgHeader string) ([]api.Project, error)
	Upload(ctx context.Context, organization, projectID, filePath string) (string, error)
	DocumentStatus(ctx context.Context, organization, projectID, documentID string) (api.DocumentStatus, error)
	Triples(ctx context.Context, organization, projectID, documentID string) ([]byte, error)
	Query(ctx context.Context, organization, projectID, question string) (string, error)
	Call(ctx context.Context, req api.Request, out io.Writer) error
}

// Updater finds and installs yggy releases.
type Updater interface {
	Latest(ctx context.Context) (string, error)
	Install(ctx context.Context, version, exePath string) error
}

type App struct {
	In          io.Reader
	Out         io.Writer
	Err         io.Writer
	InFile      *os.File
	ConfigPath  string
	API         API
	Env         func(string) string
	Interactive bool
	// Version is the running release, or "dev" for a build from source.
	Version string
	Updater Updater
	// ExePath returns the path of the running binary. It defaults to os.Executable.
	ExePath func() (string, error)
	// NewDeviceLogin builds the browser login for an API base URL. It defaults to the real one.
	NewDeviceLogin func(baseURL string) DeviceLogin
	// OpenBrowser opens a page in the default browser. Without it the login only prints the address.
	OpenBrowser func(url string) error
}

func (a *App) Run(args []string) int {
	if len(args) == 0 {
		a.usage()
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		a.usage()
		return 0
	case "version", "-v", "--version":
		fmt.Fprintf(a.Out, "yggy %s\n", a.Version)
		return 0
	case "update":
		return a.update(args[1:])
	case "context":
		return a.showContext()
	case "auth":
		if len(args) < 2 || args[1] != "login" {
			a.usage()
			return 2
		}
		return a.login(args[2:])
	case "use":
		if len(args) < 2 {
			a.usage()
			return 2
		}
		name := ""
		if len(args) >= 3 {
			name = strings.Join(args[2:], " ")
		}
		switch args[1] {
		case "org":
			return a.useOrg(name)
		case "project":
			return a.useProject(name)
		default:
			a.usage()
			return 2
		}
	case "documents":
		return a.documents(args[1:])
	case "query":
		if len(args) < 2 {
			a.usage()
			return 2
		}
		return a.query(strings.Join(args[1:], " "))
	case "api":
		return a.apiCommand(args[1:])
	default:
		fmt.Fprintf(a.Err, "unknown command %q\n", args[0])
		a.usage()
		return 2
	}
}

func (a *App) usage() {
	fmt.Fprint(a.Err, `yggy — CLI for the yggy API

Usage:
  yggy auth login [--base-url URL] [--api-key KEY | --web] [--no-browser]
  yggy use org [name]
  yggy use project [name]
  yggy context

  yggy documents upload <file>
  yggy documents wait <id>
  yggy documents triples <id>
  yggy query <question>

  yggy api                       list the operations of the public API
  yggy api <operation> [args]    call one, e.g. yggy api getDocumentStatus id=<id>

  yggy update [--check] [--version X.Y.Z]   update yggy to the latest release
  yggy version

Login stores the API URL and key. In a terminal it offers the browser or an API key; --web
always uses the browser and prints the address when --no-browser is set. use selects an organization or project
that this key can already see. Document commands use the selected project.
query uses the selected project, or the whole organization when no project is selected.
`)
}

func (a *App) login(args []string) int {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	baseFlag := fs.String("base-url", "", "API base URL")
	keyFlag := fs.String("api-key", "", "API key")
	webFlag := fs.Bool("web", false, "log in with your browser instead of an API key")
	noBrowserFlag := fs.Bool("no-browser", false, "print the login address instead of opening it")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	base := firstNonEmpty(*baseFlag, a.env("YGGY_BASE_URL"))
	if base == "" {
		entered, err := a.promptLine(fmt.Sprintf("API base URL [%s]: ", defaultBaseURL))
		if err != nil {
			fmt.Fprintln(a.Err, err)
			return 1
		}
		base = firstNonEmpty(entered, defaultBaseURL)
	}
	if *webFlag && *keyFlag != "" {
		fmt.Fprintln(a.Err, "--web and --api-key cannot be combined")
		return 2
	}
	key := ""
	if !*webFlag {
		key = firstNonEmpty(*keyFlag, a.env("YGGY_API_KEY"))
	}
	useBrowser := *webFlag
	if key == "" && !useBrowser && a.Interactive {
		choice, err := a.promptLine("How do you want to log in?\n  1) in your browser\n  2) with an API key\nChoose [1]: ")
		if err != nil {
			fmt.Fprintln(a.Err, err)
			return 1
		}
		switch strings.TrimSpace(choice) {
		case "", "1":
			useBrowser = true
		case "2":
		default:
			fmt.Fprintln(a.Err, "choose 1 or 2")
			return 2
		}
	}
	if useBrowser {
		granted, code := a.browserLogin(context.Background(), strings.TrimRight(base, "/"), *noBrowserFlag)
		if code != 0 {
			return code
		}
		key = granted
	}
	if key == "" {
		entered, err := a.promptSecret("API key: ")
		if err != nil {
			fmt.Fprintln(a.Err, err)
			return 1
		}
		key = entered
	}
	if key == "" {
		fmt.Fprintln(a.Err, "an API key is required")
		return 2
	}

	file := config.File{BaseURL: strings.TrimRight(base, "/"), APIKey: key}
	if useBrowser {
		// Whoever approves a code gets the key, so name the account the key belongs to.
		if who, err := a.client(file).User(context.Background()); err == nil && who != "" {
			fmt.Fprintf(a.Out, "logged in as %s\n", who)
		}
	}
	orgs, _, err := a.client(file).Account(context.Background())
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	chosen, err := pick.Pick(orgItems(orgs), "")
	switch {
	case err == nil:
		file.SetOrg(chosen.ID, chosen.Slug, chosen.Name)
	case errors.Is(err, pick.ErrChoose):
		// Several orgs. Leave the org empty until `yggy use org`.
	case errors.Is(err, pick.ErrEmpty):
		fmt.Fprintln(a.Err, "this key has no organizations")
		return 1
	default:
		fmt.Fprintln(a.Err, err)
		return 1
	}
	if err := config.Save(a.ConfigPath, file); err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	if file.Org.ID != "" {
		fmt.Fprintf(a.Out, "org %s\n", file.Org.Label())
		return 0
	}
	fmt.Fprintln(a.Out, "logged in. Run yggy use org")
	return 0
}

func (a *App) useOrg(name string) int {
	file, code := a.load()
	if code != 0 {
		return code
	}
	orgs, _, err := a.client(file).Account(context.Background())
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	chosen, err := a.resolve(orgItems(orgs), name, "org", "slug")
	if err != nil {
		return choiceStatus(err)
	}
	file.SetOrg(chosen.ID, chosen.Slug, chosen.Name)
	if err := config.Save(a.ConfigPath, file); err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	fmt.Fprintf(a.Out, "org %s\n", file.Org.Label())
	return 0
}

func (a *App) useProject(name string) int {
	file, code := a.load()
	if code != 0 {
		return code
	}
	if file.Org.Header() == "" {
		fmt.Fprintln(a.Err, "select an organization first: yggy use org")
		return 2
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
	chosen, err := a.resolve(items, name, "project", "slug")
	if err != nil {
		return choiceStatus(err)
	}
	file.SetProject(chosen.ID, chosen.Slug, chosen.Name)
	if err := config.Save(a.ConfigPath, file); err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	fmt.Fprintf(a.Out, "project %s\n", file.Project.Label())
	return 0
}

func (a *App) documents(args []string) int {
	if len(args) < 1 {
		a.usage()
		return 2
	}
	switch args[0] {
	case "upload":
		if len(args) != 2 {
			a.usage()
			return 2
		}
		return a.upload(args[1])
	case "wait":
		if len(args) != 2 {
			a.usage()
			return 2
		}
		return a.wait(args[1])
	case "triples":
		if len(args) != 2 {
			a.usage()
			return 2
		}
		return a.triples(args[1])
	default:
		a.usage()
		return 2
	}
}

func (a *App) upload(filePath string) int {
	file, code := a.projectContext()
	if code != 0 {
		return code
	}
	documentID, err := a.client(file).Upload(context.Background(), file.Org.Header(), file.Project.ID, filePath)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	fmt.Fprintln(a.Out, documentID)
	return 0
}

func (a *App) wait(documentID string) int {
	file, code := a.projectContext()
	if code != 0 {
		return code
	}
	deadline := time.Now().Add(15 * time.Minute)
	for {
		status, err := a.client(file).DocumentStatus(context.Background(), file.Org.Header(), file.Project.ID, documentID)
		if err != nil {
			fmt.Fprintln(a.Err, err)
			return 1
		}
		if status.Failed {
			fmt.Fprintln(a.Err, "document failed")
			return 1
		}
		if status.Completed {
			fmt.Fprintln(a.Out, documentID)
			return 0
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(a.Err, "still processing")
			return 1
		}
		time.Sleep(2 * time.Second)
	}
}

func (a *App) triples(documentID string) int {
	file, code := a.projectContext()
	if code != 0 {
		return code
	}
	body, err := a.client(file).Triples(context.Background(), file.Org.Header(), file.Project.ID, documentID)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	fmt.Fprintf(a.Out, "%s\n", body)
	return 0
}

func (a *App) query(question string) int {
	file, code := a.load()
	if code != 0 {
		return code
	}
	if file.Org.Header() == "" {
		fmt.Fprintln(a.Err, "select an organization first: yggy use org")
		return 2
	}
	answer, err := a.client(file).Query(context.Background(), file.Org.Header(), file.Project.ID, question)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	fmt.Fprintln(a.Out, answer)
	return 0
}

func (a *App) projectContext() (config.File, int) {
	file, code := a.load()
	if code != 0 {
		return file, code
	}
	if file.Org.Header() == "" {
		fmt.Fprintln(a.Err, "select an organization first: yggy use org")
		return file, 2
	}
	if file.Project.ID == "" {
		fmt.Fprintln(a.Err, "select a project first: yggy use project")
		return file, 2
	}
	return file, 0
}

func (a *App) showContext() int {
	file, err := config.Load(a.ConfigPath)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	if file.APIKey == "" {
		fmt.Fprintln(a.Err, "log in first: yggy auth login")
		return 1
	}
	fmt.Fprintf(a.Out, "api      %s\n", file.BaseURL)
	fmt.Fprintf(a.Out, "key      %s\n", maskKey(file.APIKey))
	fmt.Fprintf(a.Out, "org      %s\n", emptyLabel(file.Org.Label()))
	project := ""
	if !file.Project.Empty() {
		project = file.Project.Label()
	}
	fmt.Fprintf(a.Out, "project  %s\n", emptyLabel(project))
	return 0
}

func (a *App) resolve(items []pick.Item, name, noun, example string) (pick.Item, error) {
	chosen, err := pick.Pick(items, name)
	if err == nil {
		return chosen, nil
	}
	if errors.Is(err, pick.ErrChoose) && name == "" {
		return a.choose(items, noun, example)
	}
	if errors.Is(err, pick.ErrNotFound) {
		fmt.Fprintf(a.Err, "%q is not %s on this account\n", name, singular(noun))
		return pick.Item{}, err
	}
	if errors.Is(err, pick.ErrEmpty) {
		fmt.Fprintf(a.Err, "this account has no %s\n", plural(noun))
		return pick.Item{}, err
	}
	if errors.Is(err, pick.ErrAmbiguous) {
		fmt.Fprintf(a.Err, "%q matches more than one %s\n", name, noun)
		return pick.Item{}, err
	}
	return pick.Item{}, err
}

func (a *App) choose(items []pick.Item, noun, example string) (pick.Item, error) {
	for i, item := range items {
		fmt.Fprintf(a.Err, "%d  %s\n", i+1, itemLabel(item))
	}
	if !a.Interactive {
		fmt.Fprintf(a.Err, "pass a name: yggy use %s <%s>\n", noun, example)
		return pick.Item{}, pick.ErrChoose
	}
	fmt.Fprint(a.Err, "Select a number: ")
	line, err := a.readLine()
	if err != nil {
		return pick.Item{}, err
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(items) {
		fmt.Fprintln(a.Err, "that number is not in the list")
		return pick.Item{}, pick.ErrChoose
	}
	return items[n-1], nil
}

func (a *App) load() (config.File, int) {
	file, err := config.Load(a.ConfigPath)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return config.File{}, 1
	}
	if file.APIKey == "" || file.BaseURL == "" {
		fmt.Fprintln(a.Err, "log in first: yggy auth login")
		return config.File{}, 1
	}
	return file, 0
}

func (a *App) client(file config.File) API {
	if a.API != nil {
		return a.API
	}
	return &api.Client{BaseURL: file.BaseURL, APIKey: file.APIKey}
}

func (a *App) env(key string) string {
	if a.Env == nil {
		return ""
	}
	return a.Env(key)
}

func (a *App) promptLine(prompt string) (string, error) {
	fmt.Fprint(a.Err, prompt)
	return a.readLine()
}

func (a *App) promptSecret(prompt string) (string, error) {
	fmt.Fprint(a.Err, prompt)
	if a.Interactive && a.InFile != nil && term.IsTerminal(int(a.InFile.Fd())) {
		buf, err := term.ReadPassword(int(a.InFile.Fd()))
		fmt.Fprintln(a.Err)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(buf)), nil
	}
	return a.readLine()
}

func (a *App) readLine() (string, error) {
	line, err := bufio.NewReader(a.In).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func choiceStatus(err error) int {
	if errors.Is(err, pick.ErrChoose) || errors.Is(err, pick.ErrNotFound) || errors.Is(err, pick.ErrEmpty) || errors.Is(err, pick.ErrAmbiguous) {
		return 2
	}
	return 1
}

func singular(noun string) string {
	if noun == "org" {
		return "an organization"
	}
	return "a " + noun
}

func plural(noun string) string {
	if noun == "org" {
		return "organizations"
	}
	return noun + "s"
}

func orgItems(orgs []api.Org) []pick.Item {
	items := make([]pick.Item, len(orgs))
	for i, org := range orgs {
		items[i] = pick.Item{ID: org.ID, Slug: org.Slug, Name: org.Name}
	}
	return items
}

func itemLabel(item pick.Item) string {
	if item.Slug != "" && item.Name != "" {
		return item.Slug + "  " + item.Name
	}
	if item.Slug != "" {
		return item.Slug
	}
	if item.Name != "" {
		return item.Name
	}
	return item.ID
}

func emptyLabel(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}

func maskKey(key string) string {
	switch {
	case strings.HasPrefix(key, "ygg_org_"):
		return "ygg_org_…"
	case strings.HasPrefix(key, "ygg_"):
		return "ygg_…"
	default:
		return "…"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
