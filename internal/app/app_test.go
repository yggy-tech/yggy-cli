package app

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yggy-tech/yggy-cli/internal/api"
	"github.com/yggy-tech/yggy-cli/internal/config"
)

type fakeAPI struct {
	orgs       []api.Org
	projects   []api.Project
	documentID string
	status     api.DocumentStatus
	triples    []byte
	answer     string
	err        error
	calls      *[]api.Request
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

func (f fakeAPI) Query(context.Context, string, string, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	if f.answer == "" {
		return "an answer", nil
	}
	return f.answer, nil
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
