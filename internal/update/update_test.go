package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write([]byte("hi"))
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(body)
	_ = zw.Close()
	return buf.Bytes()
}

func checksums(files map[string][]byte) string {
	var b strings.Builder
	for name, data := range files {
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	return b.String()
}

// release serves one fake release: /releases/latest redirects to the tag, assets are static.
func release(t *testing.T, version string, assets map[string][]byte, sums string) *Updater {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/"+Repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/"+Repo+"/releases/tag/v"+version, http.StatusFound)
	})
	for name, data := range assets {
		mux.HandleFunc("/"+Repo+"/releases/download/v"+version+"/"+name, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(data)
		})
	}
	mux.HandleFunc("/"+Repo+"/releases/download/v"+version+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sums))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Updater{Repo: Repo, BaseURL: srv.URL, HTTP: srv.Client(), GOOS: "linux", GOARCH: "arm64"}
}

func TestLatestReadsTheReleaseRedirect(t *testing.T) {
	u := release(t, "1.4.2", nil, "")
	got, err := u.Latest(context.Background())
	if err != nil || got != "1.4.2" {
		t.Fatalf("Latest() = %q, %v", got, err)
	}
}

func TestLatestFailsWhenNothingIsPublished(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	u := &Updater{Repo: Repo, BaseURL: srv.URL, HTTP: srv.Client()}
	if _, err := u.Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("Latest() error = %v", err)
	}
}

func TestNewer(t *testing.T) {
	cases := []struct {
		current, latest string
		want            bool
	}{
		{"0.1.0", "0.1.1", true},
		{"0.1.9", "0.2.0", true},
		{"1.0.0", "0.9.9", false},
		{"0.2.0", "0.2.0", false},
		{"0.10.0", "0.9.0", false},
		{"dev", "9.9.9", false},
		{"0.1.0", "garbage", false},
		{"v0.1.0", "v0.1.1", true},
	}
	for _, c := range cases {
		if got := Newer(c.current, c.latest); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
		}
	}
}

func TestInstallReplacesTheBinary(t *testing.T) {
	name := "yggy_2.0.0_linux_arm64.tar.gz"
	archive := tarGz(t, "yggy", []byte("new binary"))
	u := release(t, "2.0.0", map[string][]byte{name: archive}, checksums(map[string][]byte{name: archive}))

	exe := filepath.Join(t.TempDir(), "yggy")
	if err := os.WriteFile(exe, []byte("old binary"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := u.Install(context.Background(), "2.0.0", exe); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "new binary" {
		t.Fatalf("binary = %q", got)
	}
	info, _ := os.Stat(exe)
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("mode = %v, want the old mode kept", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(exe))
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestInstallRefusesAChecksumMismatch(t *testing.T) {
	name := "yggy_2.0.0_linux_arm64.tar.gz"
	archive := tarGz(t, "yggy", []byte("new binary"))
	wrong := checksums(map[string][]byte{name: []byte("something else")})
	u := release(t, "2.0.0", map[string][]byte{name: archive}, wrong)

	exe := filepath.Join(t.TempDir(), "yggy")
	_ = os.WriteFile(exe, []byte("old binary"), 0o755)
	err := u.Install(context.Background(), "2.0.0", exe)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Install() error = %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary" {
		t.Fatalf("binary was replaced despite the mismatch: %q", got)
	}
}

func TestInstallRefusesAMissingChecksumLine(t *testing.T) {
	name := "yggy_2.0.0_linux_arm64.tar.gz"
	archive := tarGz(t, "yggy", []byte("new binary"))
	u := release(t, "2.0.0", map[string][]byte{name: archive}, checksums(map[string][]byte{"other.tar.gz": archive}))
	exe := filepath.Join(t.TempDir(), "yggy")
	_ = os.WriteFile(exe, []byte("old binary"), 0o755)
	if err := u.Install(context.Background(), "2.0.0", exe); err == nil || !strings.Contains(err.Error(), "no checksum published") {
		t.Fatalf("Install() error = %v", err)
	}
}

func TestInstallFailsWhenTheArchiveHasNoBinary(t *testing.T) {
	name := "yggy_2.0.0_linux_arm64.tar.gz"
	archive := tarGz(t, "not-yggy", []byte("x"))
	u := release(t, "2.0.0", map[string][]byte{name: archive}, checksums(map[string][]byte{name: archive}))
	exe := filepath.Join(t.TempDir(), "yggy")
	_ = os.WriteFile(exe, []byte("old binary"), 0o755)
	if err := u.Install(context.Background(), "2.0.0", exe); err == nil || !strings.Contains(err.Error(), "no yggy") {
		t.Fatalf("Install() error = %v", err)
	}
}

func TestWindowsUsesAZipAndKeepsTheOldBinaryAside(t *testing.T) {
	u := &Updater{GOOS: "windows", GOARCH: "amd64"}
	if got := u.ArchiveName("1.0.0"); got != "yggy_1.0.0_windows_amd64.zip" {
		t.Fatalf("ArchiveName = %q", got)
	}
	binary, err := extract(zipOf(t, "yggy.exe", []byte("exe")), "windows")
	if err != nil || string(binary) != "exe" {
		t.Fatalf("extract = %q, %v", binary, err)
	}
	exe := filepath.Join(t.TempDir(), "yggy.exe")
	_ = os.WriteFile(exe, []byte("old"), 0o755)
	if err := replace(exe, []byte("new"), "windows"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "new" {
		t.Fatalf("exe = %q", got)
	}
	if got, _ := os.ReadFile(exe + ".old"); string(got) != "old" {
		t.Fatalf(".old = %q", got)
	}
}
