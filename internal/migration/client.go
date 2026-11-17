// Package migration uploads a folder of exports into a migration snapshot over tus 1.0.0.
package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yggy-tech/yggy-cli/internal/api"
)

const (
	tusVersion       = "1.0.0"
	tusContentType   = "application/offset+octet-stream"
	statusChecksum   = 460
	orgHeader        = "x-yggy-organization-id"
	maxResponseBytes = 1 << 20
)

// Snapshot is one dated, scoped drop of exports.
type Snapshot struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	CapturedOn string `json:"capturedOn"`
	Scope      string `json:"scope"`
	State      string `json:"state"`
}

// Upload is the server's view of one file upload.
type Upload struct {
	ID            string `json:"id"`
	Path          string `json:"path"`
	SizeBytes     int64  `json:"sizeBytes"`
	OffsetBytes   int64  `json:"offsetBytes"`
	State         string `json:"state"`
	FailureReason string `json:"failureReason"`
}

// Client talks to the migration routes of one project with an API key.
type Client struct {
	BaseURL   string
	APIKey    string
	Org       string
	ProjectID string
	HTTP      *http.Client
}

// defaultHTTP stops a stalled server from hanging a part forever and never follows a redirect, so the API key
// is not sent anywhere the API did not name. Request timeouts are set per call.
var defaultHTTP = func() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}()

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

// requestTimeout bounds one call. A part gets time in proportion to its size (at least 128 KiB/s).
func requestTimeout(body []byte) time.Duration {
	return 60*time.Second + time.Duration(len(body)/(128<<10))*time.Second
}

func (c *Client) snapshotsPath() string {
	return "/api/v1/projects/" + url.PathEscape(c.ProjectID) + "/migration/snapshots"
}

// uploadsPath is the tus creation endpoint of a snapshot.
func (c *Client) uploadsPath(snapshotID string) string {
	return c.snapshotsPath() + "/" + url.PathEscape(snapshotID) + "/uploads"
}

type response struct {
	status int
	header http.Header
	body   []byte
}

// send makes one request. A status outside 2xx comes back as *api.Error; a transport failure as the raw error.
func (c *Client) send(ctx context.Context, method, target string, header http.Header, body []byte) (response, error) {
	if strings.HasPrefix(target, "/") {
		target = strings.TrimRight(c.BaseURL, "/") + target
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout(body))
	defer cancel()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	if c.Org != "" {
		req.Header.Set(orgHeader, c.Org)
	}
	for key, values := range header {
		req.Header[key] = values
	}
	res, err := c.httpClient().Do(req)
	if err != nil {
		return response{}, err
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return response{}, err
	}
	out := response{status: res.StatusCode, header: res.Header, body: payload}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return out, decodeError(res.StatusCode, payload)
	}
	return out, nil
}

func decodeError(status int, body []byte) *api.Error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	return &api.Error{Status: status, Code: payload.Error.Code, Message: payload.Error.Message}
}

func jsonHeader() http.Header {
	return http.Header{"Content-Type": {"application/json"}}
}

// ListSnapshots returns the snapshots of the project.
func (c *Client) ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	res, err := c.send(ctx, http.MethodGet, c.snapshotsPath(), nil, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Snapshots []Snapshot `json:"snapshots"`
	}
	if err := json.Unmarshal(res.body, &payload); err != nil {
		return nil, fmt.Errorf("read snapshots: %w", err)
	}
	return payload.Snapshots, nil
}

// OpenSnapshot opens a new snapshot.
func (c *Client) OpenSnapshot(ctx context.Context, label, capturedOn, scope string) (Snapshot, error) {
	body, _ := json.Marshal(map[string]string{"label": label, "capturedOn": capturedOn, "scope": scope})
	res, err := c.send(ctx, http.MethodPost, c.snapshotsPath(), jsonHeader(), body)
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(res.body, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("read snapshot: %w", err)
	}
	return snapshot, nil
}

// CloseDrop closes the drop of a snapshot.
func (c *Client) CloseDrop(ctx context.Context, snapshotID string) (Snapshot, error) {
	res, err := c.send(ctx, http.MethodPost, c.snapshotsPath()+"/"+url.PathEscape(snapshotID)+"/close", nil, nil)
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal(res.body, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("read snapshot: %w", err)
	}
	return snapshot, nil
}

func tusHeader() http.Header {
	return http.Header{"Tus-Resumable": {tusVersion}}
}

// CreateUpload starts a tus upload of a file of size bytes at path and returns its URL.
// sha256Hex is the whole file's digest, which the server checks after assembly.
func (c *Client) CreateUpload(ctx context.Context, snapshotID, path string, size int64, sha256Hex string) (string, error) {
	header := tusHeader()
	header.Set("Upload-Length", strconv.FormatInt(size, 10))
	header.Set("Upload-Metadata", "path "+base64.StdEncoding.EncodeToString([]byte(path))+
		",sha256 "+base64.StdEncoding.EncodeToString([]byte(sha256Hex)))
	res, err := c.send(ctx, http.MethodPost, c.uploadsPath(snapshotID), header, nil)
	if err != nil {
		if isStatus(err, http.StatusConflict) {
			return "", createConflict(path, err)
		}
		return "", err
	}
	location := res.header.Get("Location")
	if location == "" {
		return "", errors.New("the server did not say where to upload the file")
	}
	base, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + c.uploadsPath(snapshotID))
	if err != nil {
		return "", err
	}
	resolved, err := base.Parse(location)
	if err != nil {
		return "", err
	}
	return resolved.String(), nil
}

// createConflict turns the server's 409 on creation into plain words.
func createConflict(path string, err error) error {
	var apiErr *api.Error
	errors.As(err, &apiErr)
	switch {
	case strings.Contains(apiErr.Message, "already holds a file"):
		return fmt.Errorf("%s is already in this snapshot", path)
	case strings.Contains(apiErr.Message, "already running"):
		return fmt.Errorf("%s is already being uploaded by another run", path)
	}
	return err
}

// TerminateUpload cancels an upload and frees what it reserved. An upload that is gone or can no longer be
// cancelled (404, 409) counts as done.
func (c *Client) TerminateUpload(ctx context.Context, uploadURL string) error {
	_, err := c.send(ctx, http.MethodDelete, uploadURL, tusHeader(), nil)
	if isStatus(err, http.StatusNotFound) || isStatus(err, http.StatusConflict) {
		return nil
	}
	return err
}

// GetUpload reads the offset and state of an upload.
func (c *Client) GetUpload(ctx context.Context, uploadURL string) (Upload, error) {
	res, err := c.send(ctx, http.MethodGet, uploadURL, tusHeader(), nil)
	if err != nil {
		return Upload{}, err
	}
	var upload Upload
	if err := json.Unmarshal(res.body, &upload); err != nil {
		return Upload{}, fmt.Errorf("read upload: %w", err)
	}
	return upload, nil
}

// AppendPart sends one part at offset with its SHA-256 and returns the new offset.
func (c *Client) AppendPart(ctx context.Context, uploadURL string, offset int64, part []byte) (int64, error) {
	sum := sha256.Sum256(part)
	header := tusHeader()
	header.Set("Content-Type", tusContentType)
	header.Set("Upload-Offset", strconv.FormatInt(offset, 10))
	header.Set("Upload-Checksum", "sha256 "+base64.StdEncoding.EncodeToString(sum[:]))
	res, err := c.send(ctx, http.MethodPatch, uploadURL, header, part)
	if err != nil {
		return 0, err
	}
	next, err := strconv.ParseInt(res.header.Get("Upload-Offset"), 10, 64)
	if err != nil {
		return 0, errors.New("the server did not report the new offset")
	}
	return next, nil
}

// retryable reports whether a failed call is worth repeating: a dropped connection, a busy or failing server,
// or a part whose checksum did not arrive intact.
func retryable(err error) bool {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status >= 500 || apiErr.Status == http.StatusTooManyRequests ||
			apiErr.Status == http.StatusRequestTimeout || apiErr.Status == statusChecksum
	}
	return !errors.Is(err, context.Canceled)
}

func isStatus(err error, status int) bool {
	var apiErr *api.Error
	return errors.As(err, &apiErr) && apiErr.Status == status
}

// backoff is the wait before retry number attempt (0 based): base, 2*base, 4*base ... capped at 30 seconds.
func backoff(base time.Duration, attempt int) time.Duration {
	wait := base << min(attempt, 10)
	return min(wait, 30*time.Second)
}
