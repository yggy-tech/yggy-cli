package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// QueryRequest is the body of a retrieve-only query. Zero fields fall back to the API defaults.
type QueryRequest struct {
	Question string   `json:"question"`
	TopK     int      `json:"topK,omitempty"`
	Mode     string   `json:"mode,omitempty"`
	Include  []string `json:"include,omitempty"`
}

type QueryContext struct {
	DocumentID string  `json:"documentId"`
	Source     string  `json:"source"`
	Title      string  `json:"title"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
}

type NamedRef struct {
	URI   string `json:"uri"`
	Label string `json:"label"`
}

type QueryEntity struct {
	URI   string  `json:"uri"`
	Label string  `json:"label"`
	Type  string  `json:"type"`
	Score float64 `json:"score"`
}

type QueryTriple struct {
	Subject   NamedRef `json:"subject"`
	Predicate NamedRef `json:"predicate"`
	Object    NamedRef `json:"object"`
}

// QueryResult is a ranked query response. Raw keeps the body as the API sent it.
type QueryResult struct {
	Answer   string         `json:"answer"`
	Contexts []QueryContext `json:"contexts"`
	Entities []QueryEntity  `json:"entities"`
	Triples  []QueryTriple  `json:"triples"`
	Raw      []byte         `json:"-"`
}

// Query ranks documents for a question in one project, or across the organization when projectID is empty.
func (c *Client) Query(ctx context.Context, organization, projectID string, req QueryRequest) (QueryResult, error) {
	path := "/api/v1/query"
	if projectID != "" {
		path = "/api/v1/projects/" + url.PathEscape(projectID) + "/query"
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return QueryResult{}, err
	}
	body, err := c.do(ctx, http.MethodPost, path, organization, "application/json", bytes.NewReader(payload))
	if err != nil {
		return QueryResult{}, err
	}
	var result QueryResult
	if err := json.Unmarshal(body, &result); err != nil {
		return QueryResult{}, fmt.Errorf("read query: %w", err)
	}
	result.Raw = body
	return result, nil
}
