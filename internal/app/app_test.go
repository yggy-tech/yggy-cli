package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/yggy-tech/yggy-cli/internal/api"
	"github.com/yggy-tech/yggy-cli/internal/config"
	"github.com/yggy-tech/yggy-cli/internal/devicelogin"
)

type fakeAPI struct {
	orgs       []api.Org
	projects   []api.Project
	user       string
	documentID string
	status     api.DocumentStatus
	triples    []byte
	answer     string
	contexts   []api.QueryContext
	documents  []api.Document
	total      int
	err        error
	calls      *[]api.Request
	queries    *[]api.QueryRequest
	asked      *[]string
}

func (f fakeAPI) Call(_ context.Context, req api.Request, out io.Writer) error {
	if f.err != nil {
		return f.err
	}
	if f.calls != nil {
		*f.calls = append(*f.calls, req)
	}
	_, err := io.WriteString(out, "{}\n")
	return err
}

func (f fakeAPI) Account(context.Context) ([]api.Org, string, error) {
	return f.orgs, "", f.err
}

func (f fakeAPI) User(context.Context) (string, error) { return f.user, nil }

func (f fakeAPI) Projects(context.Context, string) ([]api.Project, error) {
	return f.projects, f.err
}

func (f fakeAPI) Upload(context.Context, string, string, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.documentID == "" {
		return "doc-1", nil
	}
	return f.documentID, nil
}

func (f fakeAPI) DocumentStatus(context.Context, string, string, string) (api.DocumentStatus, error) {
	if f.err != nil {
		return api.DocumentStatus{}, f.err
	}
	status := f.status
	if !status.Completed && !status.Failed {
		status.Completed = true
	}
	return status, nil
}

func (f fakeAPI) Triples(context.Context, string, string, string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.triples == nil {
		return []byte(`{"triples":[],"count":0}`), nil
	}
	return f.triples, nil
}

func (f fakeAPI) Query(_ context.Context, _, _ string, req api.QueryRequest) (api.QueryResult, error) {
	if f.err != nil {
		return api.QueryResult{}, f.err
	}
	if f.queries != nil {
		*f.queries = append(*f.queries, req)
	}
	contexts := f.contexts
	if contexts == nil {
		contexts = []api.QueryContext{{DocumentID: "doc-1", Title: "Q4 report", Snippet: "Q4 changed.", Score: 1}}
	}
	return api.QueryResult{Answer: f.answer, Contexts: contexts, Raw: []byte(`{"answer":""}`)}, nil
}

func (f fakeAPI) Chat(_ context.Context, _, projectID, message, tier string) (api.ChatResult, error) {
	if f.err != nil {
		return api.ChatResult{}, f.err
	}
	if f.asked != nil {
		*f.asked = append(*f.asked, projectID+"|"+message+"|"+tier)
	}
	return api.ChatResult{
		Response: f.answer,
		Citations: []api.ChatCitation{
			{DocumentID: "doc-1", Source: "q4-report", Snippet: "revenue grew"},
			{DocumentID: "doc-1", Snippet: "again"},
			{DocumentID: "doc-2"},
		},
	}, nil
}

func (f fakeAPI) Documents(context.Context, string, string, string, int) (api.DocumentList, error) {
	if f.err != nil {
		return api.DocumentList{}, f.err
	}
	return api.DocumentList{Documents: f.documents, Total: f.total}, nil
}

func TestLoginSelectsTheOnlyOrg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var out, err bytes.Buffer
	application := &App{
		In:         strings.NewReader(""),
		Out:        &out,
		Err:        &err,
		ConfigPath: path,
		API: fakeAPI{orgs: []api.Org{
			{ID: "org-1", Slug: "acme-corp", Name: "Acme"},
		}},
	}

	code := application.Run([]string{"auth", "login", "--base-url", "https://api.yggy.tech", "--api-key", "ygg_org_test"})
	if code != 0 {
		t.Fatalf("code = %d stderr = %s", code, err.String())
	}
	if !strings.Contains(out.String(), "org acme-corp (Acme)") {
		t.Fatalf("stdout = %q", out.String())
	}
	file, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if file.APIKey != "ygg_org_test" || file.Org.Slug != "acme-corp" {
		t.Fatalf("file = %#v", file)
	}
}

func TestLoginWithSeveralOrgsWaitsForUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	var out, err bytes.Buffer
	application := &App{
		Out:        &out,
		Err:        &err,
		ConfigPath: path,
		API: fakeAPI{orgs: []api.Org{
			{ID: "org-1", Slug: "acme-corp", Name: "Acme"},
			{ID: "org-2", Slug: "other", Name: "Other"},
		}},
	}
	code := application.Run([]string{"auth", "login", "--api-key", "ygg_personal", "--base-url", "https://api.dev.yggy.tech"})
	if code != 0 {
		t.Fatalf("code = %d stderr = %s", code, err.String())
	}
	file, loadErr := config.Load(path)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if file.Org.ID != "" {
		t.Fatalf("org = %#v", file.Org)
	}
	if !strings.Contains(out.String(), "yggy use org") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestUseOrgClearsProjectAndRejectsUnknownNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	file := config.File{BaseURL: "https://api.yggy.tech", APIKey: "ygg_personal"}
	file.SetOrg("org-1", "acme-corp", "Acme")
	file.SetProject("p1", "diligence", "Diligence")
	if err := config.Save(path, file); err != nil {
		t.Fatal(err)
	}

	application := &App{
		Out:        &bytes.Buffer{},
		Err:        &bytes.Buffer{},
		ConfigPath: path,
		API: fakeAPI{orgs: []api.Org{
			{ID: "org-2", Slug: "other", Name: "Other"},
			{ID: "org-1", Slug: "acme-corp", Name: "Acme"},
		}},
	}
	if code := application.Run([]string{"use", "org", "other"}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Org.Slug != "other" || !loaded.Project.Empty() {
		t.Fatalf("file = %#v", loaded)
	}

	var stderr bytes.Buffer
	application.Err = &stderr
	if code := application.Run([]string{"use", "org", "missing"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stderr.String(), `not an organization`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestUseProjectRequiresOrgAndSelectsByName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, config.File{BaseURL: "https://api.yggy.tech", APIKey: "ygg_personal"}); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	application := &App{
		Out:        &bytes.Buffer{},
		Err:        &stderr,
		ConfigPath: path,
		API:        fakeAPI{},
	}
	if code := application.Run([]string{"use", "project"}); code != 2 {
		t.Fatalf("code = %d stderr = %s", code, stderr.String())
	}

	file := config.File{BaseURL: "https://api.yggy.tech", APIKey: "ygg_personal"}
	file.SetOrg("org-1", "acme-corp", "Acme")
	if err := config.Save(path, file); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	application.Out = &stdout
	application.Err = &bytes.Buffer{}
	application.API = fakeAPI{projects: []api.Project{
		{ID: "p1", Slug: "diligence", Name: "Diligence"},
	}}
	if code := application.Run([]string{"use", "project"}); code != 0 {
		t.Fatal(code)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Project.Slug != "diligence" {
		t.Fatalf("project = %#v", loaded.Project)
	}
	if !strings.Contains(stdout.String(), "project diligence (Diligence)") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestContextHidesTheKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	file := config.File{BaseURL: "https://api.yggy.tech", APIKey: "ygg_org_supersecret"}
	file.SetOrg("org-1", "acme-corp", "Acme")
	if err := config.Save(path, file); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	application := &App{Out: &stdout, Err: &bytes.Buffer{}, ConfigPath: path}
	if code := application.Run([]string{"context"}); code != 0 {
		t.Fatal(code)
	}
	text := stdout.String()
	if strings.Contains(text, "supersecret") {
		t.Fatalf("stdout leaked the key: %s", text)
	}
	if !strings.Contains(text, "ygg_org_…") || !strings.Contains(text, "acme-corp (Acme)") || !strings.Contains(text, "project  (none)") {
		t.Fatalf("stdout = %s", text)
	}
}

func TestUseOrgListsNamesWhenSeveralMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, config.File{BaseURL: "https://api.yggy.tech", APIKey: "ygg_personal"}); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	application := &App{
		Out:        &bytes.Buffer{},
		Err:        &stderr,
		ConfigPath: path,
		API: fakeAPI{orgs: []api.Org{
			{ID: "org-1", Slug: "acme-corp", Name: "Acme"},
			{ID: "org-2", Slug: "other", Name: "Other"},
		}},
	}
	if code := application.Run([]string{"use", "org"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stderr.String(), "acme-corp") || !strings.Contains(stderr.String(), "yggy use org <slug>") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestUploadAndQueryUseTheSavedContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	file := config.File{BaseURL: "https://api.yggy.tech", APIKey: "ygg_org_test"}
	file.SetOrg("org-1", "acme-corp", "Acme")
	file.SetProject("proj-1", "diligence", "Diligence")
	if err := config.Save(path, file); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	application := &App{
		Out:        &stdout,
		Err:        &bytes.Buffer{},
		ConfigPath: path,
		API:        fakeAPI{documentID: "doc-9", answer: "Q4 changed."},
	}
	if code := application.Run([]string{"documents", "upload", "report.pdf"}); code != 0 {
		t.Fatal(code)
	}
	if code := application.Run([]string{"query", "what changed in Q4?"}); code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(stdout.String(), "doc-9") || !strings.Contains(stdout.String(), "Q4 changed.") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

type fakeUpdater struct {
	latest    string
	err       error
	installed *[]string
}

func (f fakeUpdater) Latest(context.Context) (string, error) { return f.latest, f.err }

func (f fakeUpdater) Install(_ context.Context, version, exePath string) error {
	if f.installed != nil {
		*f.installed = append(*f.installed, version+"@"+exePath)
	}
	return f.err
}

func updateApp(version string, updater Updater) (*App, *bytes.Buffer, *bytes.Buffer) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return &App{
		Out:     out,
		Err:     errOut,
		Version: version,
		Updater: updater,
		ExePath: func() (string, error) { return "/usr/local/bin/yggy", nil },
	}, out, errOut
}

func TestVersionPrintsTheRelease(t *testing.T) {
	a, out, _ := updateApp("1.2.3", nil)
	if code := a.Run([]string{"version"}); code != 0 || out.String() != "yggy 1.2.3\n" {
		t.Fatalf("code %d, out %q", code, out.String())
	}
}

func TestUpdateInstallsTheLatestRelease(t *testing.T) {
	var installed []string
	a, out, _ := updateApp("0.1.0", fakeUpdater{latest: "0.2.0", installed: &installed})
	if code := a.Run([]string{"update"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if len(installed) != 1 || installed[0] != "0.2.0@/usr/local/bin/yggy" {
		t.Fatalf("installed %v", installed)
	}
	if !strings.Contains(out.String(), "updated yggy 0.1.0 -> 0.2.0") {
		t.Fatalf("out %q", out.String())
	}
}

func TestUpdateDoesNothingWhenCurrent(t *testing.T) {
	var installed []string
	a, out, _ := updateApp("0.2.0", fakeUpdater{latest: "0.2.0", installed: &installed})
	if code := a.Run([]string{"update"}); code != 0 || len(installed) != 0 {
		t.Fatalf("code %d, installed %v", code, installed)
	}
	if !strings.Contains(out.String(), "up to date") {
		t.Fatalf("out %q", out.String())
	}
}

func TestUpdateCheckOnlyReports(t *testing.T) {
	var installed []string
	a, out, _ := updateApp("0.1.0", fakeUpdater{latest: "0.2.0", installed: &installed})
	if code := a.Run([]string{"update", "--check"}); code != 0 || len(installed) != 0 {
		t.Fatalf("code %d, installed %v", code, installed)
	}
	if !strings.Contains(out.String(), "update available: 0.1.0 -> 0.2.0") {
		t.Fatalf("out %q", out.String())
	}
}

func TestUpdateSpecificVersionSkipsTheLookup(t *testing.T) {
	var installed []string
	a, _, _ := updateApp("0.3.0", fakeUpdater{err: nil, installed: &installed})
	if code := a.Run([]string{"update", "--version", "v0.2.0"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if len(installed) != 1 || !strings.HasPrefix(installed[0], "0.2.0@") {
		t.Fatalf("installed %v", installed)
	}
}

func TestUpdateRefusesToOverwriteASourceBuild(t *testing.T) {
	var installed []string
	a, _, errOut := updateApp("dev", fakeUpdater{latest: "0.2.0", installed: &installed})
	if code := a.Run([]string{"update"}); code != 1 || len(installed) != 0 {
		t.Fatalf("code %d, installed %v", code, installed)
	}
	if !strings.Contains(errOut.String(), "built from source") {
		t.Fatalf("err %q", errOut.String())
	}
}

func TestUpdateReportsFailures(t *testing.T) {
	a, _, errOut := updateApp("0.1.0", fakeUpdater{err: errors.New("no network")})
	if code := a.Run([]string{"update"}); code != 1 || !strings.Contains(errOut.String(), "no network") {
		t.Fatalf("code %d, err %q", code, errOut.String())
	}
}

type fakeDevice struct {
	key      string
	startErr error
	waitErr  error
	baseURL  string
}

func (f *fakeDevice) Start(context.Context) (devicelogin.Code, error) {
	return devicelogin.Code{
		DeviceCode: "dc", UserCode: "ABCD-EFGH",
		VerificationURI:         "https://app.yggy.tech/device",
		VerificationURIComplete: "https://app.yggy.tech/device?user_code=ABCD-EFGH",
	}, f.startErr
}

func (f *fakeDevice) Wait(context.Context, devicelogin.Code) (string, error) { return f.key, f.waitErr }

func webApp(t *testing.T, device *fakeDevice, opened *[]string) (*App, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return &App{
		In:         strings.NewReader(""),
		Out:        out,
		Err:        errOut,
		ConfigPath: path,
		API:        fakeAPI{user: "ada@example.com", orgs: []api.Org{{ID: "org-1", Slug: "acme-corp", Name: "Acme"}}},
		NewDeviceLogin: func(baseURL string) DeviceLogin {
			device.baseURL = baseURL
			return device
		},
		OpenBrowser: func(url string) error { *opened = append(*opened, url); return nil },
	}, path, out, errOut
}

func TestWebLoginOpensTheBrowserAndStoresTheIssuedKey(t *testing.T) {
	var opened []string
	device := &fakeDevice{key: "ygg_from_browser"}
	a, path, out, errOut := webApp(t, device, &opened)
	if code := a.Run([]string{"auth", "login", "--web", "--base-url", "https://api.dev.yggy.tech/"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errOut.String())
	}
	if device.baseURL != "https://api.dev.yggy.tech" {
		t.Fatalf("base url = %q", device.baseURL)
	}
	if len(opened) != 1 || opened[0] != "https://app.yggy.tech/device?user_code=ABCD-EFGH" {
		t.Fatalf("opened %v", opened)
	}
	for _, want := range []string{"ABCD-EFGH", "Do not approve a code you did not start"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("stderr %q lacks %q", errOut.String(), want)
		}
	}
	if !strings.Contains(out.String(), "logged in as ada@example.com") || !strings.Contains(out.String(), "org acme-corp (Acme)") {
		t.Fatalf("stdout %q", out.String())
	}
	file, err := config.Load(path)
	if err != nil || file.APIKey != "ygg_from_browser" || file.BaseURL != "https://api.dev.yggy.tech" {
		t.Fatalf("file %#v err %v", file, err)
	}
}

func TestWebLoginWithNoBrowserOnlyPrintsTheAddress(t *testing.T) {
	var opened []string
	a, _, _, errOut := webApp(t, &fakeDevice{key: "ygg_k"}, &opened)
	if code := a.Run([]string{"auth", "login", "--web", "--no-browser", "--base-url", "https://api.yggy.tech"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if len(opened) != 0 || !strings.Contains(errOut.String(), "Open https://app.yggy.tech/device?user_code=ABCD-EFGH in your browser") {
		t.Fatalf("opened %v stderr %q", opened, errOut.String())
	}
}

func TestWebLoginIgnoresTheKeyVariableAndRefusesAKeyFlag(t *testing.T) {
	var opened []string
	a, path, _, _ := webApp(t, &fakeDevice{key: "ygg_from_browser"}, &opened)
	a.Env = func(name string) string {
		if name == "YGGY_API_KEY" {
			return "ygg_from_env"
		}
		return ""
	}
	if code := a.Run([]string{"auth", "login", "--web", "--base-url", "https://api.yggy.tech"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if file, _ := config.Load(path); file.APIKey != "ygg_from_browser" {
		t.Fatalf("stored %q, want the browser key", file.APIKey)
	}
	if code := a.Run([]string{"auth", "login", "--web", "--api-key", "ygg_x"}); code != 2 {
		t.Fatalf("--web with --api-key: code %d, want 2", code)
	}
}

func TestWebLoginReportsDenialAndStoresNothing(t *testing.T) {
	var opened []string
	a, path, _, errOut := webApp(t, &fakeDevice{waitErr: devicelogin.ErrDenied}, &opened)
	if code := a.Run([]string{"auth", "login", "--web", "--base-url", "https://api.yggy.tech"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errOut.String(), "denied") {
		t.Fatalf("stderr %q", errOut.String())
	}
	if file, _ := config.Load(path); file.APIKey != "" {
		t.Fatalf("a key %q was stored for a denied login", file.APIKey)
	}
}

func TestInteractiveLoginOffersTheBrowserFirst(t *testing.T) {
	var opened []string
	a, path, _, errOut := webApp(t, &fakeDevice{key: "ygg_from_browser"}, &opened)
	a.Interactive = true
	a.In = iotest.OneByteReader(strings.NewReader("\n"))
	if code := a.Run([]string{"auth", "login", "--base-url", "https://api.yggy.tech"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "How do you want to log in?") {
		t.Fatalf("stderr %q", errOut.String())
	}
	if file, _ := config.Load(path); file.APIKey != "ygg_from_browser" {
		t.Fatalf("stored %q", file.APIKey)
	}
}

func TestInteractiveLoginCanStillUseAPastedKey(t *testing.T) {
	var opened []string
	device := &fakeDevice{key: "ygg_from_browser"}
	a, path, _, _ := webApp(t, device, &opened)
	a.Interactive = true
	// One byte per read, like a terminal, so each prompt takes only its own line.
	a.In = iotest.OneByteReader(strings.NewReader("2\nygg_pasted\n"))
	if code := a.Run([]string{"auth", "login", "--base-url", "https://api.yggy.tech"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if file, _ := config.Load(path); file.APIKey != "ygg_pasted" || len(opened) != 0 {
		t.Fatalf("stored %q opened %v", file.APIKey, opened)
	}
}

func contextApp(t *testing.T, fake fakeAPI, withProject bool) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	file := config.File{BaseURL: "https://api.yggy.tech", APIKey: "ygg_org_test"}
	file.SetOrg("org-1", "acme-corp", "Acme")
	if withProject {
		file.SetProject("proj-1", "diligence", "Diligence")
	}
	if err := config.Save(path, file); err != nil {
		t.Fatal(err)
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return &App{Out: out, Err: errOut, ConfigPath: path, API: fake}, out, errOut
}

func TestQueryListsTheRankedDocumentsAndTakesFlagsAnywhere(t *testing.T) {
	var queries []api.QueryRequest
	a, out, errOut := contextApp(t, fakeAPI{answer: "Q4 changed.", queries: &queries}, true)
	if code := a.Run([]string{"query", "what", "changed", "--top-k", "3", "in", "Q4?", "--include", "entities, triples"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errOut.String())
	}
	want := api.QueryRequest{Question: "what changed in Q4?", TopK: 3, Include: []string{"entities", "triples"}}
	if len(queries) != 1 || queries[0].Question != want.Question || queries[0].TopK != 3 || strings.Join(queries[0].Include, ",") != "entities,triples" {
		t.Fatalf("queries %#v", queries)
	}
	for _, line := range []string{"Q4 changed.", "Documents", "1. Q4 report  1.00  doc-1"} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("stdout %q lacks %q", out.String(), line)
		}
	}
}

func TestQueryRejectsBadFlagsBeforeCallingTheAPI(t *testing.T) {
	var queries []api.QueryRequest
	a, _, errOut := contextApp(t, fakeAPI{queries: &queries}, true)
	for _, args := range [][]string{
		{"query", "x", "--mode", "fuzzy"},
		{"query", "x", "--include", "everything"},
		{"query", "x", "--top-k", "99"},
		{"query", "x", "--nope"},
	} {
		if code := a.Run(args); code != 2 {
			t.Fatalf("%v: code %d", args, code)
		}
	}
	if len(queries) != 0 {
		t.Fatalf("called the API with %v", queries)
	}
	if !strings.Contains(errOut.String(), "usage: yggy query <question>") {
		t.Fatalf("stderr %q", errOut.String())
	}
}

func TestQueryWithNothingFoundPointsToAsk(t *testing.T) {
	a, out, errOut := contextApp(t, fakeAPI{contexts: []api.QueryContext{}}, false)
	if code := a.Run([]string{"query", "what can you do"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "yggy ask") {
		t.Fatalf("stdout %q stderr %q", out.String(), errOut.String())
	}
}

func TestQueryJSONPrintsTheAPIResponse(t *testing.T) {
	a, out, _ := contextApp(t, fakeAPI{}, true)
	if code := a.Run([]string{"query", "--json", "x"}); code != 0 || out.String() != "{\n  \"answer\": \"\"\n}\n" {
		t.Fatalf("code %d stdout %q", code, out.String())
	}
}

func TestAskAnswersWithItsSourcesOncePerDocument(t *testing.T) {
	var asked []string
	a, out, errOut := contextApp(t, fakeAPI{answer: "Revenue grew 12%.\n", asked: &asked}, false)
	if code := a.Run([]string{"ask", "--tier", "fast", "how", "did", "revenue", "change?"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errOut.String())
	}
	if len(asked) != 1 || asked[0] != "|how did revenue change?|fast" {
		t.Fatalf("asked %v", asked)
	}
	want := "Revenue grew 12%.\n\nSources\n  1. q4-report\n     revenue grew\n  2. doc-2\n"
	if out.String() != want {
		t.Fatalf("stdout %q, want %q", out.String(), want)
	}
}

func TestAskWithoutAQuestionPrintsItsUsage(t *testing.T) {
	a, out, errOut := contextApp(t, fakeAPI{}, false)
	if code := a.Run([]string{"ask"}); code != 2 {
		t.Fatalf("code %d", code)
	}
	if errOut.String() != "usage: yggy ask <question> [--tier fast|balanced|accurate] [--json]\n" || out.Len() != 0 {
		t.Fatalf("stdout %q stderr %q", out.String(), errOut.String())
	}
	if code := a.Run([]string{"ask", "x", "--tier", "slow"}); code != 2 {
		t.Fatalf("bad tier: code %d", code)
	}
}

func TestCommandHelpPrintsOnlyThatCommand(t *testing.T) {
	a, out, errOut := contextApp(t, fakeAPI{}, false)
	if code := a.Run([]string{"query", "--help"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if !strings.HasPrefix(out.String(), "usage: yggy query <question>") || strings.Contains(out.String(), "yggy auth login") || errOut.Len() != 0 {
		t.Fatalf("stdout %q stderr %q", out.String(), errOut.String())
	}
	out.Reset()
	if code := a.Run([]string{"documents"}); code != 2 || !strings.Contains(errOut.String(), "yggy documents list") || strings.Contains(errOut.String(), "yggy auth login") {
		t.Fatalf("code %d stderr %q", code, errOut.String())
	}
}

func TestOrgsAndProjectsMarkTheSelection(t *testing.T) {
	a, out, _ := contextApp(t, fakeAPI{
		orgs:     []api.Org{{ID: "org-1", Slug: "acme-corp", Name: "Acme"}, {ID: "org-2", Slug: "other", Name: "Other"}},
		projects: []api.Project{{ID: "proj-1", Slug: "diligence", Name: "Diligence"}, {ID: "proj-2", Slug: "hr", Name: "HR"}},
	}, true)
	if code := a.Run([]string{"orgs"}); code != 0 {
		t.Fatalf("orgs code %d", code)
	}
	if code := a.Run([]string{"projects"}); code != 0 {
		t.Fatalf("projects code %d", code)
	}
	want := "*  acme-corp  Acme\n   other      Other\n*  diligence  Diligence\n   hr         HR\n"
	if out.String() != want {
		t.Fatalf("stdout %q, want %q", out.String(), want)
	}
}

func TestDocumentsListShowsATableAndTheRemainder(t *testing.T) {
	a, out, errOut := contextApp(t, fakeAPI{
		documents: []api.Document{{ID: "doc-1", Title: "Q4 report", Status: "COMPLETED", TripleCount: 42}},
		total:     3,
	}, true)
	if code := a.Run([]string{"documents", "list", "--limit", "1"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errOut.String())
	}
	if out.String() != "ID     STATUS     TRIPLES  TITLE\ndoc-1  completed  42       Q4 report\n" {
		t.Fatalf("stdout %q", out.String())
	}
	if !strings.Contains(errOut.String(), "showing 1 of 3") {
		t.Fatalf("stderr %q", errOut.String())
	}
	if code := a.Run([]string{"documents", "list", "--status", "done"}); code != 2 {
		t.Fatalf("bad status: code %d", code)
	}
}
