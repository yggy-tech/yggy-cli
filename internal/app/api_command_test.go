package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yggy-tech/yggy-cli/internal/api"
	"github.com/yggy-tech/yggy-cli/internal/config"
	"github.com/yggy-tech/yggy-cli/internal/ops"
)

func projectFile() config.File {
	file := config.File{BaseURL: "https://api.yggy.tech", APIKey: "ygg_personal"}
	file.SetOrg("org-1", "acme-corp", "Acme")
	file.SetProject("p1", "diligence", "Diligence")
	return file
}

func mustOp(t *testing.T, id string) ops.Operation {
	t.Helper()
	op, ok := ops.Find(id)
	if !ok {
		t.Fatalf("operation %s missing from the table", id)
	}
	return op
}

func TestBuildRequestFillsSelectedProjectAndPathParams(t *testing.T) {
	req, _, err := buildRequest(mustOp(t, "getDocumentStatus"), []string{"id=doc 1"}, projectFile())
	if err != nil {
		t.Fatal(err)
	}
	if req.Method != "GET" || req.Path != "/api/v1/projects/p1/documents/doc%201/status" || req.Org != projectFile().Org.Header() {
		t.Fatalf("req = %+v", req)
	}
	if _, _, err := buildRequest(mustOp(t, "getDocumentStatus"), nil, projectFile()); err == nil || !strings.Contains(err.Error(), "id=") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildRequestJSONBodyAndQuery(t *testing.T) {
	req, _, err := buildRequest(mustOp(t, "queryProject"), []string{"question=what changed?", "limit:=5"}, projectFile())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(req.Body)
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["question"] != "what changed?" || body["limit"] != float64(5) || req.ContentType != "application/json" {
		t.Fatalf("body = %v ct = %s", body, req.ContentType)
	}

	req, _, err = buildRequest(mustOp(t, "listDocuments"), []string{"status=completed", "limit=10"}, projectFile())
	if err != nil {
		t.Fatal(err)
	}
	if req.Query.Get("status") != "completed" || req.Query.Get("limit") != "10" || req.Body != nil {
		t.Fatalf("req = %+v", req)
	}
	if _, _, err := buildRequest(mustOp(t, "listDocuments"), []string{"bogus=1"}, projectFile()); err == nil {
		t.Fatal("want error for unknown parameter")
	}
}

func TestBuildRequestRequiresProjectAndRequiredQuery(t *testing.T) {
	if _, _, err := buildRequest(mustOp(t, "getEntityByIdentity"), nil, projectFile()); err == nil || !strings.Contains(err.Error(), "uri=") {
		t.Fatalf("err = %v", err)
	}
	noProject := config.File{BaseURL: "https://api.yggy.tech", APIKey: "ygg_personal"}
	noProject.SetOrg("org-1", "acme-corp", "Acme")
	if _, _, err := buildRequest(mustOp(t, "listDocuments"), nil, noProject); err == nil || !strings.Contains(err.Error(), "yggy use project") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildRequestMultipartUpload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	req, _, err := buildRequest(mustOp(t, "uploadDocument"), []string{"file=@" + path, "title=Report"}, projectFile())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(req.Body)
	if !strings.HasPrefix(req.ContentType, "multipart/form-data") || !bytes.Contains(raw, []byte("hello")) || !bytes.Contains(raw, []byte("Report")) {
		t.Fatalf("ct = %s body = %s", req.ContentType, raw)
	}
}

func TestAPICommandCallsAndLists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, projectFile()); err != nil {
		t.Fatal(err)
	}
	var calls []api.Request
	var stdout, stderr bytes.Buffer
	application := &App{Out: &stdout, Err: &stderr, ConfigPath: path, API: fakeAPI{calls: &calls}}
	if code := application.Run([]string{"api", "listdocuments"}); code != 0 {
		t.Fatalf("code = %d stderr = %s", code, stderr.String())
	}
	if len(calls) != 1 || calls[0].Path != "/api/v1/projects/p1/documents" {
		t.Fatalf("calls = %+v", calls)
	}
	stderr.Reset()
	if code := application.Run([]string{"api"}); code != 0 || !strings.Contains(stderr.String(), "uploadDocument") {
		t.Fatalf("code = %d list = %s", code, stderr.String())
	}
	if code := application.Run([]string{"api", "nope"}); code != 2 {
		t.Fatalf("code = %d", code)
	}
}
