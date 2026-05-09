package cairn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AuthLogin obtains an API token from the Cairn instance using HTTP
// basic auth (username + password) against Forgejo's standard token
// creation endpoint, then stores the token at the per-host token path.
//
// On success the file at <hostdir>/token contains the token text and
// has mode 0600. On any non-2xx response or network error, no file is
// written.
func AuthLogin(instanceURL, username, password, tokenName string) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]string{
		"name": tokenName,
	})
	if err != nil {
		return err
	}

	endpoint := instanceURL + "/api/v1/users/" + url.PathEscape(username) + "/tokens"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(username, password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("cairn auth: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Read up to 1KB of the response body for diagnostic context.
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		snippet := strings.TrimSpace(string(bodyBytes))
		if snippet != "" {
			return fmt.Errorf("cairn auth: token creation failed: HTTP %d: %s", resp.StatusCode, snippet)
		}
		return fmt.Errorf("cairn auth: token creation failed: HTTP %d", resp.StatusCode)
	}

	var out struct {
		SHA1 string `json:"sha1"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("cairn auth: decode response: %w", err)
	}
	if out.SHA1 == "" {
		return fmt.Errorf("cairn auth: empty token in response")
	}

	return paths.WriteToken(out.SHA1)
}
