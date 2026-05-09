// Package summarizer wraps a bridle harness for one no-tools turn per
// PR summarization.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/CarriedWorldUniverse/bridle"
	"github.com/CarriedWorldUniverse/bridle/provider/claudecode"
	"github.com/CarriedWorldUniverse/bridle/provider/openai"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// AIResponse is what the simplifier needs from one AI turn.
type AIResponse struct {
	Content    string
	ModelID    string
	TokenCount int
}

// AIClient is the interface the service layer depends on. Implemented by
// *Summarizer in production; tests inject a mock.
type AIClient interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (*AIResponse, error)
}

// Summarizer wraps a bridle harness configured for one no-tools turn.
// Implements AIClient.
type Summarizer struct {
	harness  *bridle.Harness
	provider bridle.ProviderID
	model    string
}

// NewSummarizerWithProvider constructs a Summarizer around an existing bridle.Provider.
func NewSummarizerWithProvider(p bridle.Provider, model string) (*Summarizer, error) {
	if p == nil {
		return nil, errors.New("summarizer: nil provider")
	}
	if model == "" {
		return nil, errors.New("summarizer: empty model")
	}
	return &Summarizer{
		harness:  bridle.NewHarness(p),
		provider: p.Name(),
		model:    model,
	}, nil
}

// Complete runs one bridle turn with a single user message and no tools,
// returning the final text plus token usage.
func (s *Summarizer) Complete(ctx context.Context, systemPrompt, userPrompt string) (*AIResponse, error) {
	req := bridle.TurnRequest{
		AspectID:     "cairn-simplifier",
		SystemPrompt: systemPrompt,
		UserMessage:  userPrompt,
		Provider:     s.provider,
		Model:        s.model,
		MaxSteps:     1,
	}
	result, err := s.harness.RunTurn(ctx, req, noopToolRunner{}, noopEventSink{})
	if err != nil {
		return nil, fmt.Errorf("summarizer: bridle: %w", err)
	}
	return &AIResponse{
		Content:    result.FinalText,
		ModelID:    s.model,
		TokenCount: result.Usage.InputTokens + result.Usage.OutputTokens,
	}, nil
}

// noopEventSink discards bridle events. Production summarization does not
// need event observation.
type noopEventSink struct{}

func (noopEventSink) Emit(bridle.Event) {}

// noopToolRunner returns an error if invoked. The simplifier turn declares
// no tools, so the runner should never be called; if a misbehaving provider
// emits a tool call, surface it as a turn error rather than panic.
type noopToolRunner struct{}

func (noopToolRunner) Run(_ context.Context, call bridle.ToolCall) (json.RawMessage, error) {
	return nil, fmt.Errorf("summarizer: unexpected tool call %q in no-tools turn", call.Name)
}

// BuildBridleProviderFromConfig dispatches on cfg.Provider to construct the
// matching bridle.Provider implementation, threading the decrypted API key
// through where the provider needs it.
//
// MVP supports:
//
//   - "claude-code"  — Claude via the claude-code CLI subprocess
//   - "openai-api"   — OpenAI-compatible chat completions
//
// Other bridle providers (claude-api native, bedrock, ollama-local) are
// recognized in cfg but not yet wired here; they will be enabled as their
// bridle implementations stabilize.
func BuildBridleProviderFromConfig(cfg *cairnmodels.SummarizerConfig, apiKey []byte) (bridle.Provider, error) {
	if cfg == nil {
		return nil, errors.New("summarizer: nil config")
	}
	switch cfg.Provider {
	case "":
		return nil, errors.New("summarizer: no provider configured")
	case "claude-code":
		return claudecode.New(), nil
	case "openai-api":
		return openai.New(string(apiKey)), nil
	default:
		return nil, fmt.Errorf("summarizer: unsupported provider %q (MVP supports: claude-code, openai-api)", cfg.Provider)
	}
}
