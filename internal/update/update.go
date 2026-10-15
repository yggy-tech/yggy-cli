// Package update replaces the running yggy binary with a release from GitHub.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// Repo is the public repository the releases are published in.
	Repo = "yggy-tech/yggy-cli"
	// BaseURL is the host that serves release pages and assets.
	BaseURL = "https://github.com"

	checksumsAsset = "checksums.txt"
	// maxDownload bounds every download. A release archive is a few megabytes.
	maxDownload = 200 << 20
)

// ErrUpToDate is returned when the installed version is already the newest.
var ErrUpToDate = errors.New("already up to date")

// Updater finds and installs releases. The zero value needs HTTP, GOOS and GOARCH set by New.
type Updater struct {
	Repo    string
	BaseURL string
	HTTP    *http.Client
	GOOS    string
	GOARCH  string
}

// New returns an Updater for the public repository on the given platform.
func New(goos, goarch string) *Updater {
	return &Updater{
		Repo:    Repo,
		BaseURL: BaseURL,
		HTTP:    &http.Client{Timeout: 2 * time.Minute},
		GOOS:    goos,
		GOARCH:  goarch,
	}
}

// Latest returns the newest release version without the leading "v". It reads the redirect of
// /releases/latest, which is not rate limited the way the REST API is.
func (u *Updater) Latest(ctx context.Context) (string, error) {
	client := *u.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/%s/releases/latest", u.BaseURL, u.Repo), nil)
	if err != nil {
		return "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("check for updates: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 300 || res.StatusCode > 399 {
		return "", fmt.Errorf("check for updates: unexpected status %d (no release published yet?)", res.StatusCode)
	}
	location := res.Header.Get("Location")
	_, tag, found := strings.Cut(location, "/releases/tag/")
	if !found || tag == "" {
		return "", fmt.Errorf("check for updates: unexpected redirect %q", location)
	}
	return strings.TrimPrefix(tag, "v"), nil
}

// Newer reports whether latest is a higher release than current. A current of "dev" or any
// unparsable version is never newer, so a source build is not overwritten by accident.
func Newer(current, latest string) bool {
	cur, ok := parse(current)
	if !ok {
		return false
	}
	next, ok := parse(latest)
	if !ok {
		return false
	}
	for i := range cur {
		if next[i] != cur[i] {
			return next[i] > cur[i]
		}
	}
	return false
}

func parse(version string) ([3]int, bool) {
	var out [3]int
	core, _, _ := strings.Cut(strings.TrimPrefix(version, "v"), "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// ArchiveName is the release asset for a version on this platform.
func (u *Updater) ArchiveName(version string) string {
	ext := "tar.gz"
	if u.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("yggy_%s_%s_%s.%s", version, u.GOOS, u.GOARCH, ext)
}

func (u *Updater) assetURL(version, name string) string {
	return fmt.Sprintf("%s/%s/releases/download/v%s/%s", u.BaseURL, u.Repo, version, name)
}

// Install downloads the release, checks it against the published SHA-256 checksum and
// replaces the binary at exePath.
func (u *Updater) Install(ctx context.Context, version, exePath string) error {
	name := u.ArchiveName(version)
	archive, err := u.download(ctx, u.assetURL(version, name))
	if err != nil {
		return err
	}
	sums, err := u.download(ctx, u.assetURL(version, checksumsAsset))
	if err != nil {
		return err
	}
	if err := verify(archive, name, sums); err != nil {
		return err
	}
	binary, err := extract(archive, u.GOOS)
	if err != nil {
		return err
	}
	return replace(exePath, binary, u.GOOS)
}

func (u *Updater) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := u.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", filepath.Base(url), err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: status %d", filepath.Base(url), res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", filepath.Base(url), err)
	}
	if len(body) > maxDownload {
		return nil, fmt.Errorf("download %s: larger than %d MB", filepath.Base(url), maxDownload>>20)
	}
	return body, nil
}

// verify checks data against the line for name in a checksums file ("<sha256>  <name>").
func verify(data []byte, name string, sums []byte) error {
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum published for %s", name)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != want {
		return fmt.Errorf("checksum mismatch for %s: the download is corrupted or was changed", name)
	}
	return nil
}

// extract returns the yggy executable inside a release archive.
func extract(archive []byte, goos string) ([]byte, error) {
	want := "yggy"
	if goos == "windows" {
		want = "yggy.exe"
	}
	if goos == "windows" {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		for _, file := range zr.File {
			if filepath.Base(file.Name) != want || file.FileInfo().IsDir() {
				continue
			}
			rc, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("read archive: %w", err)
			}
			defer rc.Close()
			return readBinary(rc)
		}
		return nil, fmt.Errorf("archive has no %s", want)
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("archive has no %s", want)
		}
		if err != nil {
			return nil, fmt.Errorf("read archive: %w", err)
		}
		if header.Typeflag == tar.TypeReg && filepath.Base(header.Name) == want {
			return readBinary(tr)
		}
	}
}

func readBinary(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	if len(body) > maxDownload {
		return nil, errors.New("read archive: binary is too large")
	}
	return body, nil
}

// replace swaps the file at exePath for binary. The new file is written next to the old one
// and renamed into place, so a failure never leaves a half-written yggy behind.
func replace(exePath string, binary []byte, goos string) error {
	dir := filepath.Dir(exePath)
	mode := os.FileMode(0o755)
	if info, err := os.Stat(exePath); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".yggy-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w\nInstall again with the install script, or run yggy update with permission to write there", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(binary); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	if goos == "windows" {
		// A running .exe cannot be overwritten but it can be renamed.
		old := exePath + ".old"
		_ = os.Remove(old)
		if err := os.Rename(exePath, old); err != nil {
			return err
		}
		if err := os.Rename(tmpPath, exePath); err != nil {
			_ = os.Rename(old, exePath)
			return err
		}
		return nil
	}
	return os.Rename(tmpPath, exePath)
}
