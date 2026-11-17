package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yggy-tech/yggy-cli/internal/api"
	"github.com/yggy-tech/yggy-cli/internal/config"
)

// migrationServer accepts one snapshot and one-part uploads that complete at once.
func migrationServer(t *testing.T, closes *atomic.Int32) *httptest.Server {
	t.Helper()
	const snapshots = "/api/v1/projects/proj-1/migration/snapshots"
	var received atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == snapshots && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"snapshots":[]}`))
		case r.URL.Path == snapshots:
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"s1","label":"March","state":"open"}`))
		case strings.HasSuffix(r.URL.Path, "/close"):
			closes.Add(1)
			_, _ = w.Write([]byte(`{"id":"s1","label":"March","state":"closed"}`))
		case strings.HasSuffix(r.URL.Path, "/uploads"):
			w.Header().Set("Location", r.URL.Path+"/u1")
			w.WriteHeader(201)
		case r.Method == http.MethodPatch:
			n, _ := bytes.NewBuffer(nil).ReadFrom(r.Body)
			received.Add(n)
			w.Header().Set("Upload-Offset", "5")
			w.WriteHeader(204)
		default:
			_, _ = w.Write([]byte(`{"id":"u1","sizeBytes":5,"offsetBytes":` + map[bool]string{true: "5", false: "0"}[received.Load() > 0] + `,"state":"` + map[bool]string{true: "complete", false: "uploading"}[received.Load() > 0] + `"}`))
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func migrationApp(t *testing.T, baseURL string) (*App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	file := config.File{BaseURL: baseURL, APIKey: "ygg_org_test"}
	file.SetOrg("org-1", "acme", "Acme")
	if err := config.Save(path, file); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	fake := fakeAPI{projects: []api.Project{{ID: "proj-1", Slug: "sap-exit", Name: "SAP exit"}}}
	return &App{Out: &stdout, Err: &stderr, ConfigPath: path, API: fake}, &stdout, &stderr
}

func TestMigrationUploadClosesAndReports(t *testing.T) {
	var closes atomic.Int32
	server := migrationServer(t, &closes)
	application, stdout, stderr := migrationApp(t, server.URL)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.csv"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	code := application.Run([]string{"migration", "upload", "sap-exit", dir, "--snapshot", "March", "--close", "--parallel", "2"})
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	if closes.Load() != 1 || !strings.Contains(stdout.String(), "1 of 1 files uploaded") || !strings.Contains(stdout.String(), "drop closed") {
		t.Fatalf("closes = %d, stdout = %q", closes.Load(), stdout.String())
	}
	if !strings.Contains(stderr.String(), "a.csv: done") {
		t.Fatalf("progress = %q", stderr.String())
	}
}

func TestMigrationUploadJSONAndQuiet(t *testing.T) {
	var closes atomic.Int32
	server := migrationServer(t, &closes)
	application, stdout, stderr := migrationApp(t, server.URL)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.csv"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := application.Run([]string{"migration", "upload", "sap-exit", dir, "--json"}); code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, stderr.String())
	}
	var result struct {
		Closed bool `json:"closed"`
		Files  []struct{ Path, Status string }
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || len(result.Files) != 1 || result.Closed || closes.Load() != 0 {
		t.Fatalf("stdout = %q, err = %v", stdout.String(), err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("json mode wrote progress: %q", stderr.String())
	}

	stdout.Reset()
	if code := application.Run([]string{"migration", "upload", "sap-exit", dir, "--quiet"}); code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("quiet printed: %q %q", stdout.String(), stderr.String())
	}
}

func TestMigrationUploadUsageAndUnknownProject(t *testing.T) {
	application, _, stderr := migrationApp(t, "http://127.0.0.1:1")
	if code := application.Run([]string{"migration", "upload", "sap-exit"}); code != 2 || !strings.Contains(stderr.String(), "yggy migration upload") {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	stderr.Reset()
	if code := application.Run([]string{"migration", "upload", "nope", t.TempDir()}); code != 2 || !strings.Contains(stderr.String(), "not a project") {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
}
