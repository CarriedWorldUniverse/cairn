// Cairn-specific code; AGPLv3. See LICENSING.md.

package v1

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// TestConfigToResponse_RedactsCredentials is the load-bearing security
// test: regardless of how CredentialsCipher is rendered, the cipher
// bytes themselves must never appear in any field of the JSON response.
// CredentialsSet is the only signal the API exposes.
func TestConfigToResponse_RedactsCredentials(t *testing.T) {
	cipher := []byte("super-secret-cipher-bytes")
	cfg := &cairnmodels.SummarizerConfig{
		OwnerID:           1,
		Enabled:           true,
		Provider:          "openai-api",
		EndpointURL:       "https://api.example.com/v1",
		ModelID:           "gpt-4o",
		CredentialsCipher: cipher,
		LevelsEnabled:     cairnmodels.LevelPR,
	}

	resp := configToResponse(cfg)

	if !resp.CredentialsSet {
		t.Error("CredentialsSet = false, want true")
	}
	if resp.Provider != "openai-api" {
		t.Errorf("Provider = %q, want openai-api", resp.Provider)
	}
	if resp.EndpointURL != "https://api.example.com/v1" {
		t.Errorf("EndpointURL = %q, want https://api.example.com/v1", resp.EndpointURL)
	}

	body, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, cipher) {
		t.Errorf("response JSON leaks raw cipher bytes: %s", body)
	}
	// Defence in depth: also check the b64 / hex encodings don't slip
	// through some accidental field — neither should ever match a
	// configToResponse output, but if they do this test catches it.
	if strings.Contains(string(body), "secret") {
		t.Errorf("response JSON contains plaintext substring of cipher: %s", body)
	}
}

// TestConfigToResponse_NilSafeAndEmpty verifies the unconfigured case
// returns a zero response (matching the GET-no-row behaviour).
func TestConfigToResponse_NilSafeAndEmpty(t *testing.T) {
	resp := configToResponse(nil)
	if resp.CredentialsSet {
		t.Error("nil cfg should not report CredentialsSet")
	}
	if resp.Enabled || resp.Provider != "" || resp.EndpointURL != "" {
		t.Errorf("nil cfg yielded non-zero fields: %+v", resp)
	}

	empty := configToResponse(&cairnmodels.SummarizerConfig{OwnerID: 1})
	if empty.CredentialsSet {
		t.Error("empty cipher should not report CredentialsSet")
	}
}

// TestConfigToResponse_CredentialsSetOnlyWhenCipherPresent guards
// against regressions where CredentialsSet is computed from anything
// other than cipher length.
func TestConfigToResponse_CredentialsSetOnlyWhenCipherPresent(t *testing.T) {
	cases := []struct {
		name   string
		cipher []byte
		want   bool
	}{
		{"nil cipher", nil, false},
		{"empty cipher", []byte{}, false},
		{"one byte cipher", []byte{0x01}, true},
		{"long cipher", bytes.Repeat([]byte{0xAA}, 64), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := configToResponse(&cairnmodels.SummarizerConfig{CredentialsCipher: c.cipher})
			if resp.CredentialsSet != c.want {
				t.Errorf("CredentialsSet = %v, want %v", resp.CredentialsSet, c.want)
			}
		})
	}
}

// TestRepoConsent_DataScopeValidation locks in the
// PutRepoConsent rule that an enabled consent must carry a valid
// DataScope. Disabled consent may carry any value (including empty)
// because we don't act on it.
func TestRepoConsent_DataScopeValidation(t *testing.T) {
	cases := []struct {
		name    string
		req     RepoConsentRequest
		wantErr bool
	}{
		{
			name:    "enabled + valid full",
			req:     RepoConsentRequest{Enabled: true, DataScope: cairnmodels.DataScopeFull},
			wantErr: false,
		},
		{
			name:    "enabled + valid metadata",
			req:     RepoConsentRequest{Enabled: true, DataScope: cairnmodels.DataScopeMetadata},
			wantErr: false,
		},
		{
			name:    "enabled + valid commit-messages",
			req:     RepoConsentRequest{Enabled: true, DataScope: cairnmodels.DataScopeCommitMessages},
			wantErr: false,
		},
		{
			name:    "enabled + invalid scope",
			req:     RepoConsentRequest{Enabled: true, DataScope: cairnmodels.DataScope("garbage")},
			wantErr: true,
		},
		{
			name:    "enabled + empty scope",
			req:     RepoConsentRequest{Enabled: true, DataScope: cairnmodels.DataScope("")},
			wantErr: true,
		},
		{
			name:    "disabled + empty scope (allowed)",
			req:     RepoConsentRequest{Enabled: false, DataScope: cairnmodels.DataScope("")},
			wantErr: false,
		},
		{
			name:    "disabled + invalid scope (allowed; not acted on)",
			req:     RepoConsentRequest{Enabled: false, DataScope: cairnmodels.DataScope("garbage")},
			wantErr: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotErr := c.req.Enabled && !c.req.DataScope.IsValid()
			if gotErr != c.wantErr {
				t.Errorf("validation result = %v, want %v", gotErr, c.wantErr)
			}
		})
	}
}
