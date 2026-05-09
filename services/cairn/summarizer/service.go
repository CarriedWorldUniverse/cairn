// Package summarizer service orchestration: cache-or-generate, regenerate,
// lookup. The Service is the entry point hooks and event listeners use to
// turn a PR into a cached markdown summary.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"xorm.io/xorm"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

var (
	// ErrNoSummary indicates no cached summary exists for the given PR.
	ErrNoSummary = errors.New("summarizer: no cached summary")
	// ErrNotConfigured indicates the org has no AI service enabled.
	ErrNotConfigured = errors.New("summarizer: org has no AI service configured")
)

// ConfigResolver looks up the AI client + config for an owner. Production
// wires this to read SummarizerConfig from the engine, decrypt credentials,
// and construct a bridle-backed Summarizer. Tests inject a mockClient.
type ConfigResolver func(ownerID int64) (AIClient, *cairnmodels.SummarizerConfig, error)

// Service is the simplifier orchestrator: cache-or-generate, regenerate, lookup.
type Service struct {
	engine   *xorm.Engine
	resolver ConfigResolver
}

// NewService constructs a Service bound to the given engine and resolver.
func NewService(engine *xorm.Engine, resolver ConfigResolver) *Service {
	return &Service{engine: engine, resolver: resolver}
}

// HashPRContext returns a deterministic content hash. Stable input -> stable hash.
func HashPRContext(ctx PRContext) string {
	h := sha256.New()
	fmt.Fprintf(h, "T:%s\nB:%s\nBB:%s\nHB:%s\n", ctx.Title, ctx.Body, ctx.BaseBranch, ctx.HeadBranch)
	for _, m := range ctx.CommitMessages {
		fmt.Fprintf(h, "C:%s\n", m)
	}
	for _, p := range ctx.FilePaths {
		fmt.Fprintf(h, "F:%s\n", p)
	}
	fmt.Fprintf(h, "D:%s\n", ctx.Diff)
	return hex.EncodeToString(h.Sum(nil))
}

// EnsureSummary returns the cached summary at the given content hash, or
// generates+stores a new one if absent.
func (s *Service) EnsureSummary(ctx context.Context, repoID, prNumber, ownerID int64, prCtx PRContext, scope cairnmodels.DataScope) (*cairnmodels.PRSummary, error) {
	scoped := SelectFields(scope, prCtx)
	hash := HashPRContext(scoped)

	cached := &cairnmodels.PRSummary{}
	has, err := s.engine.Context(ctx).Where("repo_id = ? AND pr_number = ? AND content_hash = ?", repoID, prNumber, hash).Get(cached)
	if err != nil {
		return nil, fmt.Errorf("summarizer: cache lookup: %w", err)
	}
	if has {
		return cached, nil
	}

	if s.resolver == nil {
		return nil, ErrNotConfigured
	}
	client, cfg, err := s.resolver(ownerID)
	if err != nil {
		return nil, fmt.Errorf("summarizer: resolve config: %w", err)
	}
	if cfg == nil || !cfg.Enabled || client == nil {
		return nil, ErrNotConfigured
	}

	resp, err := client.Complete(ctx, SystemPrompt, BuildUserPrompt(scoped))
	if err != nil {
		return nil, fmt.Errorf("summarizer: ai call: %w", err)
	}

	row := &cairnmodels.PRSummary{
		RepoID:      repoID,
		PRNumber:    prNumber,
		ContentHash: hash,
		SummaryMD:   resp.Content,
		ModelID:     resp.ModelID,
		TokenCount:  resp.TokenCount,
	}
	if _, err := s.engine.Context(ctx).Insert(row); err != nil {
		if isUniqueViolation(err) {
			// Lost the race — another goroutine inserted the same
			// (repo_id, pr_number, content_hash) row between our cache
			// check and insert. Re-read and return the winner's row.
			existing := &cairnmodels.PRSummary{}
			has, getErr := s.engine.Context(ctx).Where("repo_id = ? AND pr_number = ? AND content_hash = ?", repoID, prNumber, hash).Get(existing)
			if getErr != nil {
				return nil, fmt.Errorf("summarizer: post-race lookup: %w", getErr)
			}
			if has {
				return existing, nil
			}
			// Fall through if the row really didn't exist (shouldn't happen).
		}
		return nil, fmt.Errorf("summarizer: insert: %w", err)
	}
	return row, nil
}

// RegenerateSummary forces a new generation regardless of cache state. The
// new row is inserted; old rows for the same PR (different content hashes)
// are kept for audit.
//
// Note on UNIQUE(repo_id, pr_number, content_hash): if regeneration is
// requested for content identical to an already-cached row (e.g. the user
// clicks regenerate without any PR change), the insert will hit the
// composite unique constraint. In that case we return the existing row.
// Practically, the user sees a fresh AI call's result on the winning side
// and the previously-cached row on the losing/duplicate side; either way
// they get one valid summary for that content hash.
func (s *Service) RegenerateSummary(ctx context.Context, repoID, prNumber, ownerID int64, prCtx PRContext, scope cairnmodels.DataScope) (*cairnmodels.PRSummary, error) {
	if s.resolver == nil {
		return nil, ErrNotConfigured
	}
	client, cfg, err := s.resolver(ownerID)
	if err != nil {
		return nil, fmt.Errorf("summarizer: resolve config: %w", err)
	}
	if cfg == nil || !cfg.Enabled || client == nil {
		return nil, ErrNotConfigured
	}
	scoped := SelectFields(scope, prCtx)
	hash := HashPRContext(scoped)

	resp, err := client.Complete(ctx, SystemPrompt, BuildUserPrompt(scoped))
	if err != nil {
		return nil, fmt.Errorf("summarizer: ai call: %w", err)
	}
	row := &cairnmodels.PRSummary{
		RepoID:      repoID,
		PRNumber:    prNumber,
		ContentHash: hash,
		SummaryMD:   resp.Content,
		ModelID:     resp.ModelID,
		TokenCount:  resp.TokenCount,
	}
	if _, err := s.engine.Context(ctx).Insert(row); err != nil {
		if isUniqueViolation(err) {
			// A row already exists for this exact content hash (either
			// pre-existing or inserted by a concurrent caller). Return it.
			existing := &cairnmodels.PRSummary{}
			has, getErr := s.engine.Context(ctx).Where("repo_id = ? AND pr_number = ? AND content_hash = ?", repoID, prNumber, hash).Get(existing)
			if getErr != nil {
				return nil, fmt.Errorf("summarizer: post-race lookup: %w", getErr)
			}
			if has {
				return existing, nil
			}
		}
		return nil, fmt.Errorf("summarizer: insert: %w", err)
	}
	return row, nil
}

// GetCachedSummary returns the most-recent cached summary for a PR (any
// content hash). Returns ErrNoSummary if none exists.
func (s *Service) GetCachedSummary(ctx context.Context, repoID, prNumber int64) (*cairnmodels.PRSummary, error) {
	row := &cairnmodels.PRSummary{}
	has, err := s.engine.Context(ctx).Where("repo_id = ? AND pr_number = ?", repoID, prNumber).Desc("generated_unix").Get(row)
	if err != nil {
		return nil, fmt.Errorf("summarizer: cache lookup: %w", err)
	}
	if !has {
		return nil, ErrNoSummary
	}
	return row, nil
}
