package cairn

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCairnManifestHandler_ReturnsValidJSON(t *testing.T) {
	h := CairnManifestHandler("test-instance", "0.1.0", "15.0.1", map[string]any{
		"markdown_rendering": true,
		"agent_proposals":    true,
		"mcp_server":         false,
		"sdks":               []string{},
	})
	req := httptest.NewRequest(http.MethodGet, "/.well-known/cairn.json", nil)
	w := httptest.NewRecorder()
	h(w, req)

	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var m Manifest
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest not valid JSON: %v\nbody=%s", err, w.Body.String())
	}

	if m.CairnVersion != "0.1.0" {
		t.Errorf("CairnVersion = %q", m.CairnVersion)
	}
	if m.FingerprintAlgo != "HMAC-SHA256" {
		t.Errorf("FingerprintAlgo = %q", m.FingerprintAlgo)
	}
	if m.SigningAlgo != "Ed25519" {
		t.Errorf("SigningAlgo = %q", m.SigningAlgo)
	}
	if len(m.Trailers) != 3 {
		t.Errorf("Trailers length = %d, want 3", len(m.Trailers))
	}
}

func TestCairnManifestHandler_NeverExposesHMACKey(t *testing.T) {
	h := CairnManifestHandler("test", "0.1.0", "15.0.1", map[string]any{})
	req := httptest.NewRequest(http.MethodGet, "/.well-known/cairn.json", nil)
	w := httptest.NewRecorder()
	h(w, req)

	body := w.Body.String()
	for _, dangerous := range []string{
		"hmac_key",
		"hmacKey",
		"HMACKey",
		"private",
		"secret",
		"instance_hmac",
	} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(dangerous)) {
			t.Errorf("manifest contains %q (HMAC-key-related leak):\n%s", dangerous, body)
		}
	}
}

func TestLLMsTxtHandler_RendersMarkdown(t *testing.T) {
	h := LLMsTxtHandler("cairn.example.com", "0.1.0")
	req := httptest.NewRequest(http.MethodGet, "/.well-known/llms.txt", nil)
	w := httptest.NewRecorder()
	h(w, req)

	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown", got)
	}

	body := w.Body.String()
	for _, want := range []string{
		"# Cairn (cairn.example.com, v0.1.0)",
		"?format=md",
		"/.well-known/cairn.json",
		"HKDF-SHA256",
		"nexus-{slug}@{domain}",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody=%s", want, body)
		}
	}
}

func TestSecurityTxtHandler_ReturnsStaticContent(t *testing.T) {
	h := SecurityTxtHandler()
	req := httptest.NewRequest(http.MethodGet, "/.well-known/security.txt", nil)
	w := httptest.NewRecorder()
	h(w, req)

	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", got)
	}

	body := w.Body.String()
	for _, want := range []string{
		"Contact:",
		"Expires:",
		"Preferred-Languages:",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\nbody=%s", want, body)
		}
	}
}

func TestBuildManifest_FeaturesPreserved(t *testing.T) {
	feats := map[string]any{
		"markdown_rendering": true,
		"mcp_server":         false,
		"sdks":               []string{"go", "python"},
	}
	m := BuildManifest("inst", "v1", "f15", feats)
	if m.Features["markdown_rendering"] != true {
		t.Error("markdown_rendering not preserved")
	}
	if m.Features["mcp_server"] != false {
		t.Error("mcp_server not preserved")
	}
}

func TestBuildManifest_AdvertisesSimplifier(t *testing.T) {
	m := BuildManifest("test-instance", "0.0.0", "1.22.0", map[string]any{
		"simplifier_enabled": true,
	})
	v, ok := m.Features["simplifier_enabled"]
	if !ok {
		t.Fatal("manifest missing simplifier_enabled feature")
	}
	if v != true {
		t.Errorf("simplifier_enabled = %v, want true", v)
	}
}

func TestBuildManifest_AdvertisesReviewPolicy(t *testing.T) {
	m := BuildManifest("test-instance", "0.0.0", "1.22.0", map[string]any{
		"review_policy_enabled": true,
	})
	v, ok := m.Features["review_policy_enabled"]
	if !ok {
		t.Fatal("manifest missing review_policy_enabled feature")
	}
	if v != true {
		t.Errorf("review_policy_enabled = %v, want true", v)
	}
}
