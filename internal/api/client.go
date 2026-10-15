package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const orgHeader = "x-yggy-organization-id"

type Org struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type Project struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("request failed with status %d", e.Status)
}

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) base() string {
	return strings.TrimRight(c.BaseURL, "/")
}

// Account returns organizations this key can use. An organization key is
// narrowed to the org on its session when the API reports one.
func (c *Client) Account(ctx context.Context) ([]Org, string, error) {
	orgs, err := c.listOrganizations(ctx)
	if err != nil {
		return nil, "", err
	}
	bound := ""
	if strings.HasPrefix(c.APIKey, "ygg_org_") {
		bound, err = c.sessionOrgID(ctx)
		if err != nil {
			return nil, "", err
		}
	}
	if bound == "" {
		return orgs, "", nil
	}
	var filtered []Org
	for _, org := range orgs {
		if org.ID == bound {
			filtered = append(filtered, org)
		}
	}
	if len(filtered) == 0 {
		filtered = []Org{{ID: bound}}
	}
	return filtered, bound, nil
}

func (c *Client) Projects(ctx context.Context, orgHeaderValue string) ([]Project, error) {
	var all []Project
	offset := 0
	for {
		page, err := c.projectPage(ctx, orgHeaderValue, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Projects...)
		offset += len(page.Projects)
		if len(page.Projects) == 0 || offset >= page.Total {
			return all, nil
		}
	}
}

type projectPage struct {
	Projects []Project `json:"projects"`
	Total    int       `json:"total"`
}

func (c *Client) listOrganizations(ctx context.Context) ([]Org, error) {
	body, err := c.get(ctx, "/api/v1/auth/organization/list", "")
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || string(body) == "null" {
		return nil, nil
	}
	var orgs []Org
	if err := json.Unmarshal(body, &orgs); err != nil {
		return nil, fmt.Errorf("read organizations: %w", err)
	}
	return orgs, nil
}

func (c *Client) sessionOrgID(ctx context.Context) (string, error) {
	body, err := c.get(ctx, "/api/v1/auth/get-session", "")
	if err != nil {
		return "", err
	}
	if len(body) == 0 || string(body) == "null" {
		return "", nil
	}
	var payload struct {
		Session *struct {
			ActiveOrganizationID string `json:"activeOrganizationId"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("read session: %w", err)
	}
	if payload.Session == nil {
		return "", nil
	}
	return payload.Session.ActiveOrganizationID, nil
}

// User returns the email (or name) of the account the key belongs to, "" when the API reports none.
func (c *Client) User(ctx context.Context) (string, error) {
	body, err := c.get(ctx, "/api/v1/auth/get-session", "")
	if err != nil {
		return "", err
	}
	if len(body) == 0 || string(body) == "null" {
		return "", nil
	}
	var payload struct {
		User *struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("read session: %w", err)
	}
	if payload.User == nil {
		return "", nil
	}
	if payload.User.Email != "" {
		return payload.User.Email, nil
	}
	return payload.User.Name, nil
}

func (c *Client) projectPage(ctx context.Context, orgHeaderValue string, offset int) (projectPage, error) {
	path := fmt.Sprintf("/api/v1/projects?limit=100&offset=%d&sortBy=createdAt&sortDir=asc", offset)
	body, err := c.get(ctx, path, orgHeaderValue)
	if err != nil {
		return projectPage{}, err
	}
	var page projectPage
	if err := json.Unmarshal(body, &page); err != nil {
		return projectPage{}, fmt.Errorf("read projects: %w", err)
	}
	return page, nil
}

func (c *Client) get(ctx context.Context, path, organization string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, path, organization, "", nil)
}

func (c *Client) do(ctx context.Context, method, path, organization, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	if organization != "" {
		req.Header.Set(orgHeader, organization)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, apiError(res.StatusCode, payload)
	}
	return payload, nil
}

func apiError(status int, body []byte) error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	return &Error{Status: status, Code: payload.Error.Code, Message: payload.Error.Message}
}

type DocumentStatus struct {
	DocumentID string `json:"documentId"`
	Completed  bool   `json:"completed"`
	Failed     bool   `json:"failed"`
}

func (c *Client) Upload(ctx context.Context, organization, projectID, filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	path := "/api/v1/projects/" + url.PathEscape(projectID) + "/documents/upload"
	payload, err := c.do(ctx, http.MethodPost, path, organization, writer.FormDataContentType(), &body)
	if err != nil {
		return "", err
	}
	var result struct {
		DocumentID string `json:"documentId"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return "", fmt.Errorf("read upload: %w", err)
	}
	if result.DocumentID == "" {
		return "", fmt.Errorf("upload response did not include a document id")
	}
	return result.DocumentID, nil
}

func (c *Client) DocumentStatus(ctx context.Context, organization, projectID, documentID string) (DocumentStatus, error) {
	path := "/api/v1/projects/" + url.PathEscape(projectID) + "/documents/" + url.PathEscape(documentID) + "/status"
	payload, err := c.get(ctx, path, organization)
	if err != nil {
		return DocumentStatus{}, err
	}
	var status DocumentStatus
	if err := json.Unmarshal(payload, &status); err != nil {
		return DocumentStatus{}, fmt.Errorf("read status: %w", err)
	}
	return status, nil
}

func (c *Client) Triples(ctx context.Context, organization, projectID, documentID string) ([]byte, error) {
	path := "/api/v1/projects/" + url.PathEscape(projectID) + "/documents/" + url.PathEscape(documentID) + "/triples"
	return c.get(ctx, path, organization)
}

func (c *Client) Query(ctx context.Context, organization, projectID, question string) (string, error) {
	path := "/api/v1/query"
	if projectID != "" {
		path = "/api/v1/projects/" + url.PathEscape(projectID) + "/query"
	}
	payload, err := json.Marshal(map[string]string{"question": question})
	if err != nil {
		return "", err
	}
	body, err := c.do(ctx, http.MethodPost, path, organization, "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	var result struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("read query: %w", err)
	}
	return result.Answer, nil
}

// Request is one raw call against the API, as `yggy api` builds it.
type Request struct {
	Method      string
	Path        string
	Query       url.Values
	ContentType string
	Body        io.Reader
	// Org is sent as the organization header when set.
	Org string
}

// Call sends req and copies the response body to out. JSON is indented;
// anything else (event streams, downloads) is copied as it arrives.
func (c *Client) Call(ctx context.Context, req Request, out io.Writer) error {
	target := c.base() + req.Path
	if len(req.Query) > 0 {
		target += "?" + req.Query.Encode()
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, target, req.Body)
	if err != nil {
		return err
	}
	httpReq.Header.Set("x-api-key", c.APIKey)
	if req.Org != "" {
		httpReq.Header.Set(orgHeader, req.Org)
	}
	if req.ContentType != "" {
		httpReq.Header.Set("Content-Type", req.ContentType)
	}
	client := c.httpClient()
	if c.HTTP == nil {
		// Streams and downloads outlive the default timeout; ctx bounds the call.
		client = &http.Client{}
	}
	res, err := client.Do(httpReq)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		return apiError(res.StatusCode, payload)
	}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		payload, err := io.ReadAll(res.Body)
		if err != nil {
			return err
		}
		var indented bytes.Buffer
		if json.Indent(&indented, payload, "", "  ") == nil {
			indented.WriteByte('\n')
			_, err = indented.WriteTo(out)
			return err
		}
		_, err = out.Write(payload)
		return err
	}
	_, err = io.Copy(out, res.Body)
	return err
}
