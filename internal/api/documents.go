package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

type Document struct {
	ID          string `json:"documentId"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	TripleCount int    `json:"tripleCount"`
	CreatedAt   string `json:"createdAt"`
}

// DocumentList is one page of a project's documents, newest first. Raw keeps the body as the API sent it.
type DocumentList struct {
	Documents []Document `json:"documents"`
	Total     int        `json:"total"`
	Raw       []byte     `json:"-"`
}

// Documents lists up to limit documents of a project. status filters by processing status when set.
func (c *Client) Documents(ctx context.Context, organization, projectID, status string, limit int) (DocumentList, error) {
	query := url.Values{"limit": {strconv.Itoa(limit)}, "sortBy": {"createdAt"}, "sortDir": {"desc"}}
	if status != "" {
		query.Set("status", status)
	}
	path := "/api/v1/projects/" + url.PathEscape(projectID) + "/documents?" + query.Encode()
	body, err := c.get(ctx, path, organization)
	if err != nil {
		return DocumentList{}, err
	}
	var list DocumentList
	if err := json.Unmarshal(body, &list); err != nil {
		return DocumentList{}, fmt.Errorf("read documents: %w", err)
	}
	list.Raw = body
	return list, nil
}
