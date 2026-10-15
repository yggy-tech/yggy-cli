// Package devicelogin signs the CLI in through the browser with the OAuth 2.0 device authorization
// grant (RFC 8628). The server answers an approved login with a personal API key.
package devicelogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// ClientID is the only client the server accepts for CLI logins.
	ClientID  = "yggy-cli"
	grantType = "urn:ietf:params:oauth:grant-type:device_code"

	codePath = "/api/v1/auth/device/code"
	keyPath  = "/api/v1/auth/device/key"

	// slowDownStep is how much longer to wait between polls after the server says slow_down (RFC 8628 3.5).
	slowDownStep = 5 * time.Second
	maxBody      = 1 << 20
)

// Errors a caller can tell apart from a failed request.
var (
	ErrExpired = errors.New("the login code expired. Run yggy auth login again")
	ErrDenied  = errors.New("the login was denied in the browser")
)

// Code is what the server returns when a login starts.
type Code struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// URL is the page to open: the one with the code filled in when the server offers it.
func (c Code) URL() string {
	if c.VerificationURIComplete != "" {
		return c.VerificationURIComplete
	}
	return c.VerificationURI
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
	// Sleep waits between polls. It defaults to a context-aware timer.
	Sleep func(ctx context.Context, d time.Duration) error
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Start asks the server for a one-time code.
func (c *Client) Start(ctx context.Context) (Code, error) {
	var code Code
	status, body, err := c.post(ctx, codePath, map[string]string{"client_id": ClientID})
	if err != nil {
		return code, err
	}
	if status != http.StatusOK {
		return code, serverError(status, body)
	}
	if err := json.Unmarshal(body, &code); err != nil || code.DeviceCode == "" || code.UserCode == "" || code.URL() == "" {
		return code, errors.New("the server sent an unreadable login code")
	}
	return code, nil
}

// Wait polls until the member approves in the browser and returns the API key.
func (c *Client) Wait(ctx context.Context, code Code) (string, error) {
	interval := time.Duration(code.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Time{}
	if code.ExpiresIn > 0 {
		deadline = time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	}
	for {
		if err := c.sleep(ctx, interval); err != nil {
			return "", err
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return "", ErrExpired
		}
		status, body, err := c.post(ctx, keyPath, map[string]string{
			"grant_type":  grantType,
			"device_code": code.DeviceCode,
			"client_id":   ClientID,
		})
		if err != nil {
			return "", err
		}
		if status == http.StatusOK {
			var granted struct {
				AccessToken string `json:"access_token"`
			}
			if err := json.Unmarshal(body, &granted); err != nil || granted.AccessToken == "" {
				return "", errors.New("the server sent an unreadable API key")
			}
			return granted.AccessToken, nil
		}
		switch oauthError(body) {
		case "authorization_pending":
		case "slow_down":
			interval += slowDownStep
		case "expired_token":
			return "", ErrExpired
		case "access_denied":
			return "", ErrDenied
		default:
			return "", serverError(status, body)
		}
	}
}

func (c *Client) post(ctx context.Context, path string, payload any) (int, []byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("content-type", "application/json")
	res, err := c.httpClient().Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("login request failed: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody))
	if err != nil {
		return 0, nil, fmt.Errorf("login request failed: %w", err)
	}
	return res.StatusCode, body, nil
}

// oauthError reads the RFC 8628 error name from a response body.
func oauthError(body []byte) string {
	var parsed struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed)
	return parsed.Error
}

func serverError(status int, body []byte) error {
	var parsed struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
		Message     string `json:"message"`
	}
	_ = json.Unmarshal(body, &parsed)
	switch {
	case status == http.StatusTooManyRequests:
		return errors.New("too many login attempts. Wait a minute and try again")
	case parsed.Description != "":
		return fmt.Errorf("login failed: %s", parsed.Description)
	case parsed.Message != "":
		return fmt.Errorf("login failed: %s", parsed.Message)
	}
	return fmt.Errorf("login failed with status %d", status)
}
