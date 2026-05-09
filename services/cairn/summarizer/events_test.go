// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

// captureClient records the last user prompt seen by the AI client. Used to
// assert that scope filtering happened on the way through the queue.
type captureClient struct {
	calls      atomic.Int64
	lastPrompt atomic.Value // string
}

func (c *captureClient) Complete(_ context.Context, _, user string) (*AIResponse, error) {
	c.calls.Add(1)
	c.lastPrompt.Store(user)
	return &AIResponse{Content: "ok", ModelID: "mock", TokenCount: 1}, nil
}

func TestQueueDebounces(t *testing.T) {
	eng := cairntest.NewEngine(t)
	eng.DB().SetMaxOpenConns(1)
	var resolves atomic.Int64
	cli := &mockClient{response: "x"}
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		resolves.Add(1)
		return cli, &cairnmodels.SummarizerConfig{Enabled: true, EndpointURL: "x"}, nil
	})
	q := newQueue(svc, 100*time.Millisecond)

	for i := 0; i < 5; i++ {
		q.enqueue(Job{RepoID: 1, PRNumber: 1, OwnerID: 1, Context: PRContext{Title: "v"}, Scope: cairnmodels.DataScopeMetadata})
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if got := resolves.Load(); got != 1 {
		t.Errorf("resolver calls = %d, want 1 (rapid enqueues should debounce)", got)
	}
	if got := cli.calls.Load(); got != 1 {
		t.Errorf("client calls = %d, want 1", got)
	}
}

func TestQueueDifferentPRsNotCoalesced(t *testing.T) {
	eng := cairntest.NewEngine(t)
	eng.DB().SetMaxOpenConns(1)
	var resolves atomic.Int64
	cli := &mockClient{response: "x"}
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		resolves.Add(1)
		return cli, &cairnmodels.SummarizerConfig{Enabled: true, EndpointURL: "x"}, nil
	})
	q := newQueue(svc, 80*time.Millisecond)

	q.enqueue(Job{RepoID: 1, PRNumber: 1, OwnerID: 1, Context: PRContext{Title: "a"}, Scope: cairnmodels.DataScopeMetadata})
	q.enqueue(Job{RepoID: 1, PRNumber: 2, OwnerID: 1, Context: PRContext{Title: "b"}, Scope: cairnmodels.DataScopeMetadata})
	time.Sleep(250 * time.Millisecond)
	if got := resolves.Load(); got != 2 {
		t.Errorf("resolver calls = %d, want 2 (distinct PRs)", got)
	}
}

func TestQueueLatestJobWins(t *testing.T) {
	eng := cairntest.NewEngine(t)
	eng.DB().SetMaxOpenConns(1)
	cli := &captureClient{}
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		return cli, &cairnmodels.SummarizerConfig{Enabled: true, EndpointURL: "x"}, nil
	})
	q := newQueue(svc, 80*time.Millisecond)

	q.enqueue(Job{RepoID: 1, PRNumber: 1, OwnerID: 1, Context: PRContext{Title: "first"}, Scope: cairnmodels.DataScopeMetadata})
	time.Sleep(20 * time.Millisecond)
	q.enqueue(Job{RepoID: 1, PRNumber: 1, OwnerID: 1, Context: PRContext{Title: "winner"}, Scope: cairnmodels.DataScopeMetadata})
	time.Sleep(250 * time.Millisecond)
	if got := cli.calls.Load(); got != 1 {
		t.Errorf("client calls = %d, want 1", got)
	}
	prompt, _ := cli.lastPrompt.Load().(string)
	if prompt == "" {
		t.Fatal("no prompt captured")
	}
	if !contains(prompt, "winner") {
		t.Errorf("prompt = %q, expected to contain latest title", prompt)
	}
	if contains(prompt, "first") {
		t.Errorf("prompt = %q, expected superseded title to be absent", prompt)
	}
}

func TestQueueScopeFilterAppliedThroughEnsureSummary(t *testing.T) {
	eng := cairntest.NewEngine(t)
	eng.DB().SetMaxOpenConns(1)
	cli := &captureClient{}
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		return cli, &cairnmodels.SummarizerConfig{Enabled: true, EndpointURL: "x"}, nil
	})
	q := newQueue(svc, 50*time.Millisecond)

	q.enqueue(Job{
		RepoID: 1, PRNumber: 1, OwnerID: 1,
		Context: PRContext{
			Title:          "scope test",
			CommitMessages: []string{"secret commit"},
			Diff:           "secret diff",
			FilePaths:      []string{"a.go"},
		},
		Scope: cairnmodels.DataScopeMetadata,
	})
	time.Sleep(200 * time.Millisecond)
	if got := cli.calls.Load(); got != 1 {
		t.Fatalf("client calls = %d, want 1", got)
	}
	prompt, _ := cli.lastPrompt.Load().(string)
	if contains(prompt, "secret diff") {
		t.Errorf("metadata scope leaked diff: %q", prompt)
	}
	if contains(prompt, "secret commit") {
		t.Errorf("metadata scope leaked commit message: %q", prompt)
	}
	if !contains(prompt, "a.go") {
		t.Errorf("metadata scope dropped file paths: %q", prompt)
	}
}

func TestQueueNilServiceSafe(t *testing.T) {
	q := newQueue(nil, 30*time.Millisecond)
	q.enqueue(Job{RepoID: 1, PRNumber: 1})
	time.Sleep(120 * time.Millisecond)
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
