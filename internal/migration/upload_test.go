package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const base = "/api/v1/projects/p1/migration/snapshots"

type fakeUpload struct {
	path    string
	sha256  string
	size    int64
	data    []byte
	polls   int
	patches []int64 // offsets of PATCH requests, stored or not
	stored  []int64 // offsets of parts that were stored
}

// fakeTus is a small tus 1.0.0 server for the migration routes.
type fakeTus struct {
	t  *testing.T
	mu sync.Mutex

	snapshots []Snapshot
	uploads   map[string]*fakeUpload
	creates   int
	closed    int
	badKey    atomic.Int32

	// dropPart drops the connection mid-body the first time a part at this offset arrives for a path.
	dropPart map[string]int64
	dropped  map[string]bool
	// rejectPath answers 400 on create for these paths.
	rejectPath map[string]bool
	// existing paths answer 409 on create, as a snapshot that already holds the file does.
	existing map[string]bool
	// conflictPart answers 409 on the part at this offset for a path, as for a part with other bytes.
	conflictPart map[string]int64
	deleted      []string
	// failPartsFrom answers 500 for parts at or past this offset, for a path, while set.
	failPartsFrom map[string]int64
	// patchDelay holds each PATCH, so parallel uploads overlap.
	patchDelay time.Duration
	inFlight   atomic.Int32
	maxFlight  atomic.Int32
}

func newFakeTus(t *testing.T) (*fakeTus, *Client) {
	f := &fakeTus{t: t, uploads: map[string]*fakeUpload{}, dropPart: map[string]int64{}, dropped: map[string]bool{},
		rejectPath: map[string]bool{}, existing: map[string]bool{}, conflictPart: map[string]int64{}, failPartsFrom: map[string]int64{}}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	return f, &Client{BaseURL: server.URL, APIKey: "ygg_org_test", Org: "acme", ProjectID: "p1"}
}

func (f *fakeTus) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeTus) fail(w http.ResponseWriter, status int, message string) {
	f.writeJSON(w, status, map[string]any{"error": map[string]string{"message": message, "code": "ERR"}})
}

func (f *fakeTus) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-api-key") != "ygg_org_test" || r.Header.Get(orgHeader) != "acme" {
		f.badKey.Add(1)
		f.fail(w, 401, "Unauthorized")
		return
	}
	path := r.URL.Path
	switch {
	case path == base && r.Method == http.MethodGet:
		f.mu.Lock()
		defer f.mu.Unlock()
		f.writeJSON(w, 200, map[string]any{"snapshots": f.snapshots})
	case path == base && r.Method == http.MethodPost:
		var body Snapshot
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		snap := Snapshot{ID: fmt.Sprintf("s%d", len(f.snapshots)+1), Label: body.Label, CapturedOn: body.CapturedOn, Scope: body.Scope, State: "open"}
		f.snapshots = append(f.snapshots, snap)
		f.writeJSON(w, 201, snap)
	case strings.HasSuffix(path, "/close") && r.Method == http.MethodPost:
		f.mu.Lock()
		defer f.mu.Unlock()
		f.closed++
		f.snapshots[0].State = "closed"
		f.writeJSON(w, 200, f.snapshots[0])
	case strings.HasSuffix(path, "/uploads") && r.Method == http.MethodPost:
		f.create(w, r)
	case strings.Contains(path, "/uploads/"):
		id := path[strings.LastIndex(path, "/")+1:]
		f.mu.Lock()
		up := f.uploads[id]
		f.mu.Unlock()
		if up == nil {
			f.fail(w, 404, "Upload not found")
			return
		}
		switch r.Method {
		case http.MethodGet:
			f.get(w, up)
		case http.MethodDelete:
			f.mu.Lock()
			delete(f.uploads, id)
			f.deleted = append(f.deleted, id)
			f.mu.Unlock()
			w.WriteHeader(204)
		default:
			f.patch(w, r, up)
		}
	default:
		f.fail(w, 404, "no route "+path)
	}
}

func (f *fakeTus) create(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Tus-Resumable") != "1.0.0" {
		f.fail(w, 412, "Tus-Resumable must be 1.0.0")
		return
	}
	size, _ := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	meta := map[string]string{}
	for _, pair := range strings.Split(r.Header.Get("Upload-Metadata"), ",") {
		key, value, _ := strings.Cut(strings.TrimSpace(pair), " ")
		decoded, _ := base64.StdEncoding.DecodeString(value)
		meta[key] = string(decoded)
	}
	name := meta["path"]
	if f.existing[name] {
		f.fail(w, 409, "The snapshot already holds a file at "+name)
		return
	}
	if f.rejectPath[name] {
		f.fail(w, 400, "The file path is not allowed")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	id := fmt.Sprintf("u%d", f.creates)
	f.uploads[id] = &fakeUpload{path: name, size: size, sha256: meta["sha256"]}
	w.Header().Set("Location", r.URL.Path+"/"+id)
	w.Header().Set("Upload-Offset", "0")
	w.WriteHeader(201)
}

func (f *fakeTus) get(w http.ResponseWriter, up *fakeUpload) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := "uploading"
	if int64(len(up.data)) == up.size {
		up.polls++
		state = "assembling"
		if up.polls > 1 {
			state = "complete"
		}
	}
	f.writeJSON(w, 200, map[string]any{"id": up.path, "path": up.path, "sizeBytes": up.size, "offsetBytes": len(up.data), "state": state})
}

func (f *fakeTus) patch(w http.ResponseWriter, r *http.Request, up *fakeUpload) {
	now := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		peak := f.maxFlight.Load()
		if now <= peak || f.maxFlight.CompareAndSwap(peak, now) {
			break
		}
	}
	if f.patchDelay > 0 {
		time.Sleep(f.patchDelay)
	}
	offset, _ := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	f.mu.Lock()
	up.patches = append(up.patches, offset)
	dropAt, willDrop := f.dropPart[up.path]
	willDrop = willDrop && dropAt == offset && !f.dropped[up.path]
	if willDrop {
		f.dropped[up.path] = true
	}
	failFrom, failing := f.failPartsFrom[up.path]
	failing = failing && offset >= failFrom
	f.mu.Unlock()

	if willDrop {
		// Read half of the body, then cut the connection: the part is not stored.
		buf := make([]byte, 100)
		_, _ = r.Body.Read(buf)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	if at, ok := f.conflictPart[up.path]; ok && at == offset {
		f.fail(w, 409, "Chunk 1 of this upload was already received with different bytes")
		return
	}
	if failing {
		f.fail(w, 500, "storage unavailable")
		return
	}
	var body bytes.Buffer
	_, _ = body.ReadFrom(r.Body)
	sum := sha256.Sum256(body.Bytes())
	if r.Header.Get("Upload-Checksum") != "sha256 "+base64.StdEncoding.EncodeToString(sum[:]) {
		f.fail(w, 460, "checksum mismatch")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if offset != int64(len(up.data)) {
		f.fail(w, 409, "wrong offset")
		return
	}
	up.data = append(up.data, body.Bytes()...)
	up.stored = append(up.stored, offset)
	w.Header().Set("Upload-Offset", strconv.Itoa(len(up.data)))
	w.WriteHeader(204)
}

func (f *fakeTus) byPath(path string) *fakeUpload {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, up := range f.uploads {
		if up.path == path {
			return up
		}
	}
	return nil
}

func writeFiles(t *testing.T, files map[string]int) string {
	t.Helper()
	dir := t.TempDir()
	for name, size := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(i*7 + len(name))
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func testOptions(dir string) Options {
	return Options{
		Dir: dir, Snapshot: "March", CapturedOn: "2026-03-31", PartSize: 1024,
		RetryBase: time.Millisecond, PollEvery: time.Millisecond, PollTimeout: 5 * time.Second,
	}
}

func TestUploadSendsChecksummedPartsAndSkipsHiddenFiles(t *testing.T) {
	fake, client := newFakeTus(t)
	dir := writeFiles(t, map[string]int{
		"a.csv": 2500, "sub/b.csv": 10, ".hidden": 5, ".uploads/x.part": 5, "sub/.secret": 5,
	})
	result, err := Run(context.Background(), client, testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Files) != 2 || result.Failed() != 0 {
		t.Fatalf("result = %+v", result.Files)
	}
	a := fake.byPath("a.csv")
	if a == nil || len(a.data) != 2500 || !slices.Equal(a.stored, []int64{0, 1024, 2048}) {
		t.Fatalf("a.csv parts = %+v", a)
	}
	if fake.byPath("sub/b.csv") == nil || fake.byPath(".hidden") != nil || fake.byPath("sub/.secret") != nil {
		t.Fatal("unexpected set of uploaded paths")
	}
	if fake.badKey.Load() != 0 {
		t.Fatal("a request went out without the API key or organization")
	}
	if got := fake.snapshots[0]; got.Label != "March" || got.CapturedOn != "2026-03-31" {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestUploadResumesAfterDroppedConnectionWithoutResendingFinishedParts(t *testing.T) {
	fake, client := newFakeTus(t)
	fake.dropPart["big.csv"] = 1024
	dir := writeFiles(t, map[string]int{"big.csv": 3000})

	result, err := Run(context.Background(), client, testOptions(dir))
	if err != nil || result.Failed() != 0 {
		t.Fatalf("err = %v, result = %+v", err, result)
	}
	up := fake.byPath("big.csv")
	if !slices.Equal(up.stored, []int64{0, 1024, 2048}) {
		t.Fatalf("stored parts = %v", up.stored)
	}
	// Part 0 once, the dropped part twice (dropped, then stored), the last part once.
	if !slices.Equal(up.patches, []int64{0, 1024, 1024, 2048}) {
		t.Fatalf("PATCH offsets = %v", up.patches)
	}
	if fake.creates != 1 {
		t.Fatalf("created %d uploads", fake.creates)
	}
}

func TestUploadResumesFromStateFileInANewRun(t *testing.T) {
	fake, client := newFakeTus(t)
	fake.failPartsFrom["big.csv"] = 2048
	dir := writeFiles(t, map[string]int{"big.csv": 3000, "small.csv": 100})

	first, err := Run(context.Background(), client, testOptions(dir))
	if err != nil {
		t.Fatal(err)
	}
	if first.Failed() != 1 {
		t.Fatalf("first run = %+v", first.Files)
	}
	if _, err := os.Stat(filepath.Join(dir, StateFileName)); err != nil {
		t.Fatalf("no state file: %v", err)
	}

	delete(fake.failPartsFrom, "big.csv")
	second, err := Run(context.Background(), client, testOptions(dir))
	if err != nil || second.Failed() != 0 {
		t.Fatalf("err = %v, second run = %+v", err, second.Files)
	}
	big := fake.byPath("big.csv")
	if fake.creates != 2 {
		t.Fatalf("created %d uploads, want one per file", fake.creates)
	}
	if got := big.stored; !slices.Equal(got, []int64{0, 1024, 2048}) {
		t.Fatalf("stored parts = %v", got)
	}
	var resumed, skipped bool
	for _, f := range second.Files {
		resumed = resumed || (f.Path == "big.csv" && f.Resumed)
		skipped = skipped || (f.Path == "small.csv" && f.Status == StatusSkipped)
	}
	if !resumed || !skipped {
		t.Fatalf("second run = %+v", second.Files)
	}

	patchesBefore := len(big.patches)
	third, err := Run(context.Background(), client, testOptions(dir))
	if err != nil || third.Failed() != 0 {
		t.Fatal(err)
	}
	if fake.creates != 2 || len(big.patches) != patchesBefore {
		t.Fatalf("third run sent more: creates=%d patches=%v", fake.creates, big.patches)
	}
}

func TestChangedFileStartsANewUpload(t *testing.T) {
	fake, client := newFakeTus(t)
	dir := writeFiles(t, map[string]int{"a.csv": 100})
	if _, err := Run(context.Background(), client, testOptions(dir)); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "a.csv"), later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), client, testOptions(dir)); err != nil {
		t.Fatal(err)
	}
	if fake.creates != 2 {
		t.Fatalf("created %d uploads, want a new one for the changed file", fake.creates)
	}
}

func TestUploadsFilesInParallel(t *testing.T) {
	fake, client := newFakeTus(t)
	fake.patchDelay = 80 * time.Millisecond
	files := map[string]int{}
	for i := range 6 {
		files[fmt.Sprintf("f%d.csv", i)] = 100
	}
	dir := writeFiles(t, files)

	opts := testOptions(dir)
	opts.Parallel = 3
	if _, err := Run(context.Background(), client, opts); err != nil {
		t.Fatal(err)
	}
	if peak := fake.maxFlight.Load(); peak < 2 || peak > 3 {
		t.Fatalf("peak parallel PATCH = %d, want 2 or 3", peak)
	}

	fake2, client2 := newFakeTus(t)
	fake2.patchDelay = 10 * time.Millisecond
	opts.Parallel = 1
	opts.Dir = writeFiles(t, files)
	if _, err := Run(context.Background(), client2, opts); err != nil {
		t.Fatal(err)
	}
	if peak := fake2.maxFlight.Load(); peak != 1 {
		t.Fatalf("peak with --parallel 1 = %d", peak)
	}
}

func TestCloseOnlyWhenEveryFileIsComplete(t *testing.T) {
	fake, client := newFakeTus(t)
	fake.rejectPath["bad.csv"] = true
	dir := writeFiles(t, map[string]int{"good.csv": 100, "bad.csv": 100})
	opts := testOptions(dir)
	opts.Close = true

	result, err := Run(context.Background(), client, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Closed || fake.closed != 0 || result.CloseSkipped == "" {
		t.Fatalf("closed with a failed file: %+v closes=%d", result, fake.closed)
	}
	if result.Failed() != 1 || !strings.Contains(result.Files[0].Error, "not allowed") {
		t.Fatalf("files = %+v", result.Files)
	}

	delete(fake.rejectPath, "bad.csv")
	result, err = Run(context.Background(), client, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Closed || fake.closed != 1 {
		t.Fatalf("not closed: %+v closes=%d", result, fake.closed)
	}
}

func TestUploadWithoutCloseLeavesTheDropOpen(t *testing.T) {
	fake, client := newFakeTus(t)
	dir := writeFiles(t, map[string]int{"a.csv": 10})
	result, err := Run(context.Background(), client, testOptions(dir))
	if err != nil || result.Closed || fake.closed != 0 {
		t.Fatalf("err = %v, result = %+v", err, result)
	}
}

func TestReusesAnOpenSnapshotAndRefusesAClosedOne(t *testing.T) {
	fake, client := newFakeTus(t)
	fake.snapshots = []Snapshot{{ID: "s9", Label: "March", State: "open"}}
	dir := writeFiles(t, map[string]int{"a.csv": 10})
	result, err := Run(context.Background(), client, testOptions(dir))
	if err != nil || result.Snapshot.ID != "s9" || len(fake.snapshots) != 1 {
		t.Fatalf("err = %v, snapshot = %+v", err, result.Snapshot)
	}

	fake.snapshots[0].State = "closed"
	opts := testOptions(dir)
	opts.Snapshot = "s9"
	if _, err := Run(context.Background(), client, opts); err == nil || !strings.Contains(err.Error(), "takes no more files") {
		t.Fatalf("err = %v", err)
	}
}

func TestRecreatesAnUploadTheServerForgot(t *testing.T) {
	fake, client := newFakeTus(t)
	dir := writeFiles(t, map[string]int{"a.csv": 100})
	if _, err := Run(context.Background(), client, testOptions(dir)); err != nil {
		t.Fatal(err)
	}
	// Forget the completed flag but keep the URL, and make the server forget the upload.
	raw, _ := os.ReadFile(filepath.Join(dir, StateFileName))
	raw = bytes.ReplaceAll(raw, []byte(`"complete": true`), []byte(`"complete": false`))
	_ = os.WriteFile(filepath.Join(dir, StateFileName), raw, 0o600)
	fake.mu.Lock()
	fake.uploads = map[string]*fakeUpload{}
	fake.mu.Unlock()

	result, err := Run(context.Background(), client, testOptions(dir))
	if err != nil || result.Failed() != 0 || fake.creates != 2 {
		t.Fatalf("err = %v, result = %+v, creates = %d", err, result.Files, fake.creates)
	}
}

func TestRejectsABadCaptureDate(t *testing.T) {
	_, client := newFakeTus(t)
	opts := testOptions(writeFiles(t, map[string]int{"a.csv": 1}))
	opts.CapturedOn = "31.03.2026"
	if _, err := Run(context.Background(), client, opts); err == nil || !strings.Contains(err.Error(), "--capture-date") {
		t.Fatalf("err = %v", err)
	}
}

func TestEditedFileWithSameSizeAndMtimeStartsFresh(t *testing.T) {
	fake, client := newFakeTus(t)
	fake.failPartsFrom["a.csv"] = 1024
	dir := writeFiles(t, map[string]int{"a.csv": 3000})
	path := filepath.Join(dir, "a.csv")
	info, _ := os.Stat(path)
	if first, _ := Run(context.Background(), client, testOptions(dir)); first.Failed() != 1 {
		t.Fatalf("first = %+v", first.Files)
	}
	oldSha := fake.byPath("a.csv").sha256
	if len(oldSha) != 64 {
		t.Fatalf("sha256 metadata = %q", oldSha)
	}

	// Same size, same mtime, different bytes.
	data, _ := os.ReadFile(path)
	data[2999] ^= 0xff
	_ = os.WriteFile(path, data, 0o644)
	_ = os.Chtimes(path, info.ModTime(), info.ModTime())
	delete(fake.failPartsFrom, "a.csv")

	result, err := Run(context.Background(), client, testOptions(dir))
	if err != nil || result.Failed() != 0 {
		t.Fatalf("err = %v, %+v", err, result.Files)
	}
	if len(fake.deleted) != 1 || fake.creates != 2 {
		t.Fatalf("deleted = %v, creates = %d", fake.deleted, fake.creates)
	}
	fresh := fake.byPath("a.csv")
	if fresh.sha256 == oldSha || !bytes.Equal(fresh.data, data) {
		t.Fatal("the new upload does not hold the edited file")
	}
}

func TestVanishedUploadIsTerminatedThenRecreated(t *testing.T) {
	fake, client := newFakeTus(t)
	dir := writeFiles(t, map[string]int{"a.csv": 100})
	if _, err := Run(context.Background(), client, testOptions(dir)); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, StateFileName))
	_ = os.WriteFile(filepath.Join(dir, StateFileName), bytes.ReplaceAll(raw, []byte(`"complete": true`), []byte(`"complete": false`)), 0o600)
	fake.mu.Lock()
	fake.uploads = map[string]*fakeUpload{}
	fake.mu.Unlock()
	result, err := Run(context.Background(), client, testOptions(dir))
	if err != nil || result.Failed() != 0 {
		t.Fatalf("err = %v, %+v", err, result.Files)
	}
	// The DELETE of the vanished upload is answered 404 and ignored.
	if fake.creates != 2 {
		t.Fatalf("creates = %d", fake.creates)
	}
}

func TestFileAlreadyInSnapshotIsReportedInPlainWords(t *testing.T) {
	fake, client := newFakeTus(t)
	fake.existing["a.csv"] = true
	dir := writeFiles(t, map[string]int{"a.csv": 100})
	result, err := Run(context.Background(), client, testOptions(dir))
	if err != nil || result.Failed() != 1 || result.Files[0].Error != "a.csv is already in this snapshot" {
		t.Fatalf("err = %v, %+v", err, result.Files)
	}
}

func TestConflictAtTheServerOffsetFailsAtOnce(t *testing.T) {
	fake, client := newFakeTus(t)
	fake.conflictPart["a.csv"] = 1024
	dir := writeFiles(t, map[string]int{"a.csv": 3000})
	result, err := Run(context.Background(), client, testOptions(dir))
	if err != nil || result.Failed() != 1 || !strings.Contains(result.Files[0].Error, "changed since the earlier upload") {
		t.Fatalf("err = %v, %+v", err, result.Files)
	}
	if got := fake.byPath("a.csv").patches; !slices.Equal(got, []int64{0, 1024}) {
		t.Fatalf("PATCH offsets = %v, want no retry of the refused part", got)
	}
}

func TestDefaultClientDoesNotFollowRedirectsWithTheKey(t *testing.T) {
	var leaked atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Add(1)
		w.WriteHeader(200)
	}))
	defer elsewhere.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL, http.StatusFound)
	}))
	defer origin.Close()
	client := &Client{BaseURL: origin.URL, APIKey: "ygg_org_test", ProjectID: "p1"}
	if _, err := client.ListSnapshots(context.Background()); err == nil || leaked.Load() != 0 {
		t.Fatalf("err = %v, followed = %d", err, leaked.Load())
	}
}

func TestStateFileLeavesNoTempFiles(t *testing.T) {
	_, client := newFakeTus(t)
	dir := writeFiles(t, map[string]int{"a.csv": 10})
	if _, err := Run(context.Background(), client, testOptions(dir)); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("left %s", e.Name())
		}
	}
}
