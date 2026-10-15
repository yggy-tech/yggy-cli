package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestAccountNarrowsOrgKeyAndListsProjects(t *testing.T) {
	var sawOrg string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "ygg_org_test" {
			t.Errorf("api key header = %q", r.Header.Get("x-api-key"))
		}
		switch r.URL.Path {
		case "/api/v1/auth/get-session":
			_, _ = w.Write([]byte(`{"session":{"activeOrganizationId":"org-1"},"user":{"id":"user-1"}}`))
		case "/api/v1/auth/organization/list":
			_, _ = w.Write([]byte(`[{"id":"org-1","slug":"acme-corp","name":"Acme"},{"id":"org-2","slug":"other","name":"Other"}]`))
		case "/api/v1/projects":
			sawOrg = r.Header.Get("x-yggy-organization-id")
			_, _ = w.Write([]byte(`{"success":true,"projects":[{"id":"p1","slug":"diligence","name":"Diligence"}],"total":1,"limit":100,"offset":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL, APIKey: "ygg_org_test", HTTP: server.Client()}
	orgs, bound, err := client.Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bound != "org-1" || len(orgs) != 1 || orgs[0].Slug != "acme-corp" {
		t.Fatalf("orgs = %#v bound = %s", orgs, bound)
	}

	projects, err := client.Projects(context.Background(), "acme-corp")
	if err != nil {
		t.Fatal(err)
	}
	if sawOrg != "acme-corp" || len(projects) != 1 || projects[0].Slug != "diligence" {
		t.Fatalf("projects = %#v header = %s", projects, sawOrg)
	}
}

func TestPersonalKeyKeepsEveryOrg(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/organization/list" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"org-1","slug":"acme-corp","name":"Acme"},{"id":"org-2","slug":"other","name":"Other"}]`))
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL, APIKey: "ygg_personal", HTTP: server.Client()}
	orgs, bound, err := client.Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bound != "" || len(orgs) != 2 {
		t.Fatalf("orgs = %#v bound = %s", orgs, bound)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"error":{"message":"You do not have permission.","code":"FORBIDDEN"}}`))
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL, APIKey: "ygg_personal", HTTP: server.Client()}
	_, _, err := client.Account(context.Background())
	apiErr, ok := err.(*Error)
	if !ok || apiErr.Code != "FORBIDDEN" || apiErr.Message != "You do not have permission." {
		t.Fatalf("err = %#v", err)
	}
}

func TestUploadAndQueryPaths(t *testing.T) {
	dir := t.TempDir()
	filePath := dir + "/report.pdf"
	if err := os.WriteFile(filePath, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	var sawUpload, sawQuery bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-yggy-organization-id") != "acme-corp" {
			t.Errorf("org = %q", r.Header.Get("x-yggy-organization-id"))
		}
		switch r.URL.Path {
		case "/api/v1/projects/proj-1/documents/upload":
			sawUpload = true
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"documentId":"doc-9","status":"queued"}`))
		case "/api/v1/projects/proj-1/query":
			sawQuery = true
			_, _ = w.Write([]byte(`{"answer":"Q4 changed.","contexts":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &Client{BaseURL: server.URL, APIKey: "ygg_org_test", HTTP: server.Client()}
	id, err := client.Upload(context.Background(), "acme-corp", "proj-1", filePath)
	if err != nil || id != "doc-9" || !sawUpload {
		t.Fatalf("id = %s err = %v saw = %v", id, err, sawUpload)
	}
	answer, err := client.Query(context.Background(), "acme-corp", "proj-1", "what changed?")
	if err != nil || answer != "Q4 changed." || !sawQuery {
		t.Fatalf("answer = %s err = %v saw = %v", answer, err, sawQuery)
	}
}

func TestCallIndentsJSONAndReportsErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "ygg_k" || r.Header.Get("x-yggy-organization-id") != "org-1" {
			t.Errorf("headers = %v", r.Header)
		}
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"not_found","message":"no such document"}}`))
			return
		}
		if r.URL.Query().Get("limit") != "2" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"a":1}`))
	}))
	defer server.Close()
	client := &Client{BaseURL: server.URL, APIKey: "ygg_k"}
	var out strings.Builder
	err := client.Call(context.Background(), Request{Method: "GET", Path: "/ok", Query: url.Values{"limit": {"2"}}, Org: "org-1"}, &out)
	if err != nil || out.String() != "{\n  \"a\": 1\n}\n" {
		t.Fatalf("out = %q err = %v", out.String(), err)
	}
	err = client.Call(context.Background(), Request{Method: "GET", Path: "/fail", Org: "org-1"}, &out)
	if err == nil || err.Error() != "no such document" {
		t.Fatalf("err = %v", err)
	}
}
