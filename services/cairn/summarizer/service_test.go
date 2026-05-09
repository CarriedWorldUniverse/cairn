// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"context"
	"errors"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

type mockClient struct {
	calls    int
	response string
}

func (m *mockClient) Complete(_ context.Context, _, _ string) (*AIResponse, error) {
	m.calls++
	return &AIResponse{Content: m.response, ModelID: "mock", TokenCount: 10}, nil
}

func TestEnsureSummary_GeneratesOnFirstCall(t *testing.T) {
	eng := cairntest.NewEngine(t)
	cli := &mockClient{response: "first summary"}
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		return cli, &cairnmodels.SummarizerConfig{Enabled: true, EndpointURL: "x"}, nil
	})

	prCtx := PRContext{Title: "Test PR", FilePaths: []string{"a.go"}}
	got, err := svc.EnsureSummary(context.Background(), 100, 1, 200, prCtx, cairnmodels.DataScopeMetadata)
	if err != nil {
		t.Fatalf("EnsureSummary: %v", err)
	}
	if got.SummaryMD != "first summary" {
		t.Errorf("summary = %q", got.SummaryMD)
	}
	if cli.calls != 1 {
		t.Errorf("client calls = %d, want 1", cli.calls)
	}
}

func TestEnsureSummary_CachesByContentHash(t *testing.T) {
	eng := cairntest.NewEngine(t)
	cli := &mockClient{response: "cached"}
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		return cli, &cairnmodels.SummarizerConfig{Enabled: true, EndpointURL: "x"}, nil
	})

	prCtx := PRContext{Title: "Same PR", FilePaths: []string{"a.go"}}
	_, _ = svc.EnsureSummary(context.Background(), 100, 1, 200, prCtx, cairnmodels.DataScopeMetadata)
	_, _ = svc.EnsureSummary(context.Background(), 100, 1, 200, prCtx, cairnmodels.DataScopeMetadata)
	if cli.calls != 1 {
		t.Errorf("client calls = %d, want 1 (second call should hit cache)", cli.calls)
	}
}

func TestEnsureSummary_RegeneratesOnContentChange(t *testing.T) {
	eng := cairntest.NewEngine(t)
	cli := &mockClient{response: "v"}
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		return cli, &cairnmodels.SummarizerConfig{Enabled: true, EndpointURL: "x"}, nil
	})

	_, _ = svc.EnsureSummary(context.Background(), 100, 1, 200, PRContext{Title: "v1"}, cairnmodels.DataScopeMetadata)
	_, _ = svc.EnsureSummary(context.Background(), 100, 1, 200, PRContext{Title: "v2"}, cairnmodels.DataScopeMetadata)
	if cli.calls != 2 {
		t.Errorf("client calls = %d, want 2 (content changed)", cli.calls)
	}
}

func TestGetCachedSummary_ReturnsErrNoSummaryIfMissing(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := NewService(eng, nil)
	_, err := svc.GetCachedSummary(context.Background(), 999, 999)
	if !errors.Is(err, ErrNoSummary) {
		t.Errorf("err = %v, want ErrNoSummary", err)
	}
}

func TestEnsureSummary_NoConfigReturnsErr(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		return nil, &cairnmodels.SummarizerConfig{Enabled: false}, nil
	})
	_, err := svc.EnsureSummary(context.Background(), 100, 1, 200, PRContext{Title: "x"}, cairnmodels.DataScopeMetadata)
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}

func TestGlobalSetAndLoad(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := NewService(eng, nil)
	SetGlobal(svc)
	got := Global()
	if got != svc {
		t.Errorf("Global() returned different service")
	}
	SetGlobal(nil)
}
