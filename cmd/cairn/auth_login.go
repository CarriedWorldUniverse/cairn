package cairn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

	body, _ := json.Marshal(map[string]string{
		"name": tokenName,
	})

	endpoint := instanceURL + "/api/v1/users/" + url.PathEscape(username) + "/tokens"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(body))
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
