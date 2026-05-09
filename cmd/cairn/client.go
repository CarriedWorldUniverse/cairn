package cairn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Client is a thin wrapper around the Cairn /api/cairn/v1/ endpoints.
//
// Constructed with the instance URL (e.g. "https://cairn.darksoft.co.nz")
// and an auth token. The token may be empty for endpoints that don't
// require auth (e.g. POST /agents anonymously, GET /agents/:fp/identity).
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient constructs a Client. Use *http.DefaultClient by default.
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: baseURL,
		Token:   token,
		HTTP:    http.DefaultClient,
	}
}

// APIError is returned when the Cairn server responds with a non-2xx
// status. Carries the HTTP status code, the structured error code from
// the response body, and the human message.
type APIError struct {
	StatusCode int
	ErrorCode  string
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("cairn api: %d %s: %s", e.StatusCode, e.ErrorCode, e.Message)
	}
	return fmt.Sprintf("cairn api: %d %s", e.StatusCode, e.ErrorCode)
}

// AgentResponse is the wire shape returned by the API. Mirrors
// AgentJSON in routers/api/cairn/v1/types.go.
type AgentResponse struct {
	Fingerprint  string `json:"fingerprint"`
	OwnerName    string `json:"owner"`
	Slug         string `json:"slug"`
	Domain       string `json:"domain"`
	PublicKeyHex string `json:"public_key"`
	Status       string `json:"status"`
	Blocked      bool   `json:"blocked"`
	CreatedAt    string `json:"created_at"`
	ActivatedAt  string `json:"activated_at,omitempty"`
}

// PostAgentRequest is the input to the registration endpoint.
type PostAgentRequest struct {
	ProposedOwner string
	Slug          string
	Domain        string
	PublicKey     ed25519.PublicKey
}

// PostAgent registers an agent. Returns the server's response on
// success; *APIError on non-2xx; other errors for transport/JSON
// failures.
func (c *Client) PostAgent(ctx context.Context, in PostAgentRequest) (*AgentResponse, error) {
	body, err := json.Marshal(map[string]string{
		"proposed_owner": in.ProposedOwner,
		"slug":           in.Slug,
		"domain":         in.Domain,
		"public_key":     hex.EncodeToString(in.PublicKey),
	})
	if err != nil {
		return nil, err
	}

	var out AgentResponse
	if err := c.do(ctx, http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetIdentity fetches an agent's public-facing metadata by fingerprint.
func (c *Client) GetIdentity(ctx context.Context, fingerprint string) (*AgentResponse, error) {
	path := "/api/cairn/v1/agents/" + url.PathEscape(fingerprint) + "/identity"
	var out AgentResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListAgents returns the current user's agents. status is "" for all,
// or "pending"/"active".
func (c *Client) ListAgents(ctx context.Context, status string) ([]AgentResponse, error) {
	path := "/api/cairn/v1/agents"
	if status != "" {
		path += "?status=" + url.QueryEscape(status)
	}
	var out []AgentResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Approve transitions a pending agent to active. Owner-only.
func (c *Client) Approve(ctx context.Context, fingerprint string) error {
	path := "/api/cairn/v1/agents/" + url.PathEscape(fingerprint) + "/approve"
	return c.do(ctx, http.MethodPost, path, nil, nil)
}

// Block adds an agent to the blocklist. Owner-only.
func (c *Client) Block(ctx context.Context, fingerprint, reason string) error {
	path := "/api/cairn/v1/agents/" + url.PathEscape(fingerprint) + "/block"
	body, _ := json.Marshal(map[string]string{"reason": reason})
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(body), nil)
}

// do is the shared request shape: build, set auth + content-type,
// execute, parse status + body. If decodeInto is non-nil, decode the
// response body into it.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, decodeInto any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "token "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return &APIError{
			StatusCode: resp.StatusCode,
			ErrorCode:  errBody.Error,
			Message:    errBody.Message,
		}
	}

	if decodeInto != nil {
		if err := json.NewDecoder(resp.Body).Decode(decodeInto); err != nil {
			return fmt.Errorf("cairn api: decode response: %w", err)
		}
	}
	return nil
}
