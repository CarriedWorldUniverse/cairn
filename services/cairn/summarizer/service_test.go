// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

type mockClient struct {
	calls    atomic.Int64
	response string
}

func (m *mockClient) Complete(_ context.Context, _, _ string) (*AIResponse, error) {
	m.calls.Add(1)
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
	if got := cli.calls.Load(); got != 1 {
		t.Errorf("client calls = %d, want 1", got)
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
	if got := cli.calls.Load(); got != 1 {
		t.Errorf("client calls = %d, want 1 (second call should hit cache)", got)
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
	if got := cli.calls.Load(); got != 2 {
		t.Errorf("client calls = %d, want 2 (content changed)", got)
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

func TestEnsureSummary_ConcurrentSameStateDoesNotError(t *testing.T) {
	eng := cairntest.NewEngine(t)
	// In-memory SQLite gives each connection a distinct database; pin the
	// pool to a single connection so concurrent goroutines see the same
	// schema. Production uses real SQLite-on-disk or Postgres where this
	// constraint doesn't apply.
	eng.DB().SetMaxOpenConns(1)
	cli := &mockClient{response: "x"}
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		return cli, &cairnmodels.SummarizerConfig{Enabled: true, EndpointURL: "x"}, nil
	})
	prCtx := PRContext{Title: "race", FilePaths: []string{"a.go"}}

	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.EnsureSummary(context.Background(), 100, 1, 200, prCtx, cairnmodels.DataScopeMetadata)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent EnsureSummary returned error: %v", err)
		}
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
