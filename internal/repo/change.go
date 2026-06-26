package repo

import (
	"context"
	"fmt"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// ChangeGraphSummary is a server-side read of a hosted repo's convergence graph,
// reconstructed from the pushed refs/cairn/meta.
type ChangeGraphSummary struct {
	Lines         []string // line (branch) names in the change-graph
	OpenConflicts int
}

// OpenChangeEngine opens a read-only convergence engine over a hosted repo's bare
// store and reconstructs its change-graph from refs/cairn/meta. The caller must
// Close the returned engine.
//
// This is the seam that makes cairn-server convergence-AWARE: today the server is
// a faithful but blind git locker (it stores refs/cairn/* opaquely); this lets it
// READ the change-graph it hosts — the foundation for change-graph inspection,
// the privacy/embargo tier, and the multi-agent hub. A plain-git or not-yet-
// pushed repo opens fine with no lines.
func (s *Service) OpenChangeEngine(ctx context.Context, orgID, slug string) (*change.Engine, error) {
	r, err := s.GetRepo(ctx, orgID, slug)
	if err != nil {
		return nil, err
	}
	e, err := change.OpenBare(r.StoragePath)
	if err != nil {
		return nil, fmt.Errorf("repo.OpenChangeEngine: %w", err)
	}
	if _, err := e.LoadFromMeta(); err != nil {
		_ = e.Close()
		return nil, fmt.Errorf("repo.OpenChangeEngine: %w", err)
	}
	return e, nil
}

// ChangeGraph summarizes a hosted repo's convergence graph (line tree + open
// conflict count). Empty for a plain-git / not-yet-pushed repo.
func (s *Service) ChangeGraph(ctx context.Context, orgID, slug string) (ChangeGraphSummary, error) {
	e, err := s.OpenChangeEngine(ctx, orgID, slug)
	if err != nil {
		return ChangeGraphSummary{}, err
	}
	defer e.Close()
	nodes, err := e.GetLineTree()
	if err != nil {
		return ChangeGraphSummary{}, fmt.Errorf("repo.ChangeGraph: %w", err)
	}
	lines := make([]string, 0, len(nodes))
	for _, n := range nodes {
		lines = append(lines, n.Line.Name)
	}
	conflicts, err := e.OpenConflictCount()
	if err != nil {
		return ChangeGraphSummary{}, fmt.Errorf("repo.ChangeGraph: %w", err)
	}
	return ChangeGraphSummary{Lines: lines, OpenConflicts: conflicts}, nil
}
