package herald

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// HeraldClient is the real HeraldAgents: it calls NEX-412
// (GET /api/agents/by-fingerprint/{fp}) on herald. Until NEX-412 is deployed
// this compiles and is unit-tested against an httptest stub of that contract;
// going live is a config change (point baseURL at herald), not a code change.
type HeraldClient struct {
	baseURL string
	http    *http.Client
}

// NewHeraldClient builds a client against herald's base URL (e.g.
// "http://herald.cwb.svc:8099" or the gateway-fronted "{gateway}/herald").
func NewHeraldClient(baseURL string, hc *http.Client) *HeraldClient {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &HeraldClient{baseURL: strings.TrimRight(baseURL, "/"), http: hc}
}

type agentDTO struct {
	ID     string   `json:"id"`
	Org    string   `json:"org"`
	Active bool     `json:"active"`
	Scopes []string `json:"scopes"`
}

// LookupByFingerprint implements HeraldAgents against NEX-412.
func (c *HeraldClient) LookupByFingerprint(ctx context.Context, fp string) (Agent, error) {
	u := c.baseURL + "/api/agents/by-fingerprint/" + url.PathEscape(fp)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Agent{}, fmt.Errorf("herald.LookupByFingerprint: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Agent{}, fmt.Errorf("herald.LookupByFingerprint: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var dto agentDTO
		if err := json.NewDecoder(resp.Body).Decode(&dto); err != nil {
			return Agent{}, fmt.Errorf("herald.LookupByFingerprint: decode: %w", err)
		}
		return Agent{
			ID:          dto.ID,
			OrgID:       dto.Org,
			Active:      dto.Active,
			Scopes:      dto.Scopes,
			Fingerprint: fp,
		}, nil
	case http.StatusNotFound:
		return Agent{}, ErrAgentNotFound
	default:
		return Agent{}, fmt.Errorf("herald.LookupByFingerprint: unexpected status %d", resp.StatusCode)
	}
}
