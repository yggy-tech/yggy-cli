package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// chatTimeout bounds one answer. The agent searches the graph before it writes, which outlasts the default timeout.
const chatTimeout = 5 * time.Minute

// ChatMessage is one earlier turn of a conversation. Role is user or assistant.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is one question. History carries the earlier turns, oldest first. An empty Tier uses the API default.
type ChatRequest struct {
	Message string
	History []ChatMessage
	Tier    string
}

type ChatCitation struct {
	DocumentID string `json:"documentId"`
	Source     string `json:"source"`
	Snippet    string `json:"snippet"`
}

// ChatResult is the agent's answer. Raw keeps the body as the API sent it.
type ChatResult struct {
	Response  string
	Citations []ChatCitation
	Raw       []byte
}

// Chat asks the graph agent a question in one project, or across the organization when projectID is empty.
func (c *Client) Chat(ctx context.Context, organization, projectID string, req ChatRequest) (ChatResult, error) {
	path := "/api/v1/chat"
	if projectID != "" {
		path = "/api/v1/projects/" + url.PathEscape(projectID) + "/chat"
	}
	request := map[string]any{"message": req.Message}
	if len(req.History) > 0 {
		request["history"] = req.History
	}
	if req.Tier != "" {
		request["options"] = map[string]string{"tier": req.Tier}
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return ChatResult{}, err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: chatTimeout}
	}
	body, err := c.send(ctx, client, http.MethodPost, path, organization, "application/json", bytes.NewReader(payload))
	if err != nil {
		return ChatResult{}, err
	}
	var envelope struct {
		Data struct {
			Response  string         `json:"response"`
			Citations []ChatCitation `json:"citations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ChatResult{}, fmt.Errorf("read answer: %w", err)
	}
	return ChatResult{Response: envelope.Data.Response, Citations: envelope.Data.Citations, Raw: body}, nil
}
