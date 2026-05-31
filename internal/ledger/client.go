// Package ledger is cairn's outbound client to the CWB issues/tracker service.
// cairn calls it in-cluster (ledger.cwb.svc) to open a tracking issue when a
// pull request is opened, FORWARDING the gateway-injected X-CWB-* identity of
// the opener so the issue is created on their behalf. Mirrors internal/herald.
package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client calls ledger's REST API.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient builds a client against ledger's base URL (e.g.
// "http://ledger.cwb.svc:8081").
func NewClient(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: hc}
}

// ExternalRef links the issue back to the cairn branch/PR.
type ExternalRef struct {
	Tracker     string `json:"tracker"`
	Key         string `json:"key"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
}

// IssueInput is the body of a create-issue request.
type IssueInput struct {
	Project          string        `json:"project"`
	Type             string        `json:"type"`
	Summary          string        `json:"summary"`
	Description      string        `json:"description,omitempty"`
	DefinitionOfDone string        `json:"definition_of_done,omitempty"`
	ExternalRefs     []ExternalRef `json:"external_refs,omitempty"`
}

// IssueResult is the decoded create-issue response (the parts cairn needs).
type IssueResult struct {
	Key string `json:"key"`
}

// APIError is a non-2xx ledger response. The handler mirrors Status back to the
// caller so a scope/validation failure surfaces unchanged.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ledger: status %d: %s", e.Status, e.Body)
}

// cwbHeaders are the trusted identity headers cairn forwards to ledger.
var cwbHeaders = []string{"X-Cwb-Subject", "X-Cwb-Org", "X-Cwb-Kind", "X-Cwb-Scopes", "X-Cwb-Responsible-Human"}

// CreateIssue POSTs /api/issues with the forwarded identity. A non-2xx response
// is returned as *APIError; a transport failure as a plain wrapped error.
func (c *Client) CreateIssue(ctx context.Context, fwd http.Header, in IssueInput) (IssueResult, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return IssueResult{}, fmt.Errorf("ledger.CreateIssue: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/issues", bytes.NewReader(body))
	if err != nil {
		return IssueResult{}, fmt.Errorf("ledger.CreateIssue: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, h := range cwbHeaders {
		if v := fwd.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return IssueResult{}, fmt.Errorf("ledger.CreateIssue: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return IssueResult{}, &APIError{Status: resp.StatusCode, Body: string(raw)}
	}
	var out IssueResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return IssueResult{}, fmt.Errorf("ledger.CreateIssue: decode: %w", err)
	}
	if out.Key == "" {
		return IssueResult{}, fmt.Errorf("ledger.CreateIssue: empty key in response: %s", raw)
	}
	return out, nil
}
