//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"context"
	"testing"

	"github.com/CarriedWorldUniverse/bridle/fake"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

func TestSummarizer_RunsOneTurnViaBridle(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "summary out"})

	s, err := NewSummarizerWithProvider(provider, "fake-model")
	if err != nil {
		t.Fatalf("NewSummarizerWithProvider: %v", err)
	}

	resp, err := s.Complete(context.Background(), "system", "user")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "summary out" {
		t.Errorf("content = %q, want %q", resp.Content, "summary out")
	}
	if resp.ModelID != "fake-model" {
		t.Errorf("model = %q, want fake-model", resp.ModelID)
	}
}

func TestNewSummarizerWithProvider_RejectsNilProvider(t *testing.T) {
	if _, err := NewSummarizerWithProvider(nil, "x"); err == nil {
		t.Error("expected error for nil provider")
	}
}

func TestNewSummarizerWithProvider_RejectsEmptyModel(t *testing.T) {
	provider := fake.NewProvider(fake.Step{Text: "x"})
	if _, err := NewSummarizerWithProvider(provider, ""); err == nil {
		t.Error("expected error for empty model")
	}
}

func TestBuildBridleProviderFromConfig_DispatchesByProvider(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"claudecode", false},
		{"openai-api", false},
		{"unknown", true},
		{"", true},
	}
	for _, tc := range cases {
		cfg := &cairnmodels.SummarizerConfig{
			Provider:    tc.name,
			ModelID:     "x",
			EndpointURL: "https://example",
		}
		p, err := BuildBridleProviderFromConfig(cfg, []byte("api-key"))
		if tc.wantErr {
			if err == nil {
				t.Errorf("provider=%q: expected error, got nil", tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("provider=%q: unexpected error: %v", tc.name, err)
			continue
		}
		if p == nil {
			t.Errorf("provider=%q: nil provider with no error", tc.name)
		}
	}
}

func TestBuildBridleProviderFromConfig_NilConfig(t *testing.T) {
	if _, err := BuildBridleProviderFromConfig(nil, nil); err == nil {
		t.Error("expected error for nil config")
	}
}
