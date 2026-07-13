package grpcapi

// ChangeService is the Phase-1 in-process facade over the internal/change
// engine. It forwards each engine call verbatim and maps the engine's Go
// errors to gRPC status codes, so Phase 2 can wire actual proto messages and
// transport on top of a stable, code-mapped seam without re-touching the
// engine.
//
// This is a thin forwarding facade: no business logic, no auth. Identity
// (author/tagger) is herald-stamped at the transport boundary in Phase 2; for
// now the caller supplies it. The in-process engine API stays the primary
// path — this facade only adds the error-code mapping seam.

import (
	"context"
	"errors"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ChangeService wraps a *change.Engine with gRPC error-code mapping.
type ChangeService struct {
	eng *change.Engine
}

// NewChangeService builds the facade over an open engine.
func NewChangeService(e *change.Engine) *ChangeService { return &ChangeService{eng: e} }

// mapErr translates engine errors into gRPC status errors. nil passes through;
// sentinel errors map to their semantic code; anything else is Internal.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, change.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, change.ErrHasConflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// LineByName resolves a line name to its row through the error-mapped facade.
func (s *ChangeService) LineByName(ctx context.Context, name string) (change.Line, error) {
	l, err := s.eng.LineByName(name)
	return l, mapErr(err)
}

// CreateLine forks a new line off parent.
func (s *ChangeService) CreateLine(ctx context.Context, name, parent string) (change.Line, error) {
	ln, err := s.eng.CreateLine(name, parent)
	return ln, mapErr(err)
}

// FoldLine merges a line back into its parent.
func (s *ChangeService) FoldLine(ctx context.Context, lineID string) error {
	return mapErr(s.eng.FoldLine(lineID))
}

// AbandonLine discards a line without folding it.
func (s *ChangeService) AbandonLine(ctx context.Context, lineID string) error {
	return mapErr(s.eng.AbandonLine(lineID))
}

// GetLineage returns the root-to-line chain.
func (s *ChangeService) GetLineage(ctx context.Context, lineID string) ([]change.Line, error) {
	chain, err := s.eng.GetLineage(lineID)
	return chain, mapErr(err)
}

// GetLineTree returns the full line tree.
func (s *ChangeService) GetLineTree(ctx context.Context) ([]change.LineNode, error) {
	tree, err := s.eng.GetLineTree()
	return tree, mapErr(err)
}

// CreateChange opens a new change on a line.
func (s *ChangeService) CreateChange(ctx context.Context, lineID, author string) (change.Change, error) {
	c, err := s.eng.CreateChange(lineID, author)
	return c, mapErr(err)
}

// Commit snapshots files into a change.
func (s *ChangeService) Commit(ctx context.Context, changeID string, files map[string][]byte, message string) (change.CommitResult, error) {
	res, err := s.eng.Commit(changeID, files, nil, message)
	return res, mapErr(err)
}

// GetChange fetches a change by id.
func (s *ChangeService) GetChange(ctx context.Context, id string) (change.Change, error) {
	c, err := s.eng.GetChange(id)
	return c, mapErr(err)
}

// Conflicts lists open conflicts on a change.
func (s *ChangeService) Conflicts(ctx context.Context, changeID string) ([]change.Conflict, error) {
	cs, err := s.eng.Conflicts(changeID)
	return cs, mapErr(err)
}

// ResolveConflict records the resolved contents for a conflicting path.
// NOTE: unlike worktree.Resolve, this facade does NOT refuse content that still
// contains diff3 conflict markers (#114) — when it gets wired to a live
// transport, add the diff3.HasMarkers guard (with an explicit force override).
func (s *ChangeService) ResolveConflict(ctx context.Context, changeID, path string, resolved []byte) error {
	return mapErr(s.eng.ResolveConflict(changeID, path, resolved))
}

// Tag names a commit.
func (s *ChangeService) Tag(ctx context.Context, name, commit, tagger string) error {
	return mapErr(s.eng.Tag(name, commit, tagger))
}

// ListTags lists all tags.
func (s *ChangeService) ListTags(ctx context.Context) ([]change.Tag, error) {
	tags, err := s.eng.ListTags()
	return tags, mapErr(err)
}

// OperationLog returns the operation history.
func (s *ChangeService) OperationLog(ctx context.Context) ([]change.Operation, error) {
	ops, err := s.eng.OperationLog()
	return ops, mapErr(err)
}

// Undo reverts the last operation.
func (s *ChangeService) Undo(ctx context.Context) error {
	return mapErr(s.eng.Undo())
}

// Export materializes engine state into git refs.
func (s *ChangeService) Export(ctx context.Context) error {
	return mapErr(s.eng.Export())
}
