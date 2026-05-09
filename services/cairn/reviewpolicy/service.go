// Package reviewpolicy implements Cairn's per-org "humans review only" policy:
// loads the policy row and exposes the agent-approval filter used by the
// approval-counting gate.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import (
	"context"
	"fmt"

	"xorm.io/xorm"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// AgentLookup answers "is this user an agent? if so, who owns it?" The
// production implementation lives in services/cairn/identity (wired in Task 4);
// tests inject a stub. Keeping this as an interface here decouples the filter
// from the identity package and avoids an import cycle.
type AgentLookup interface {
	IsAgentUser(ctx context.Context, userID int64) (isAgent bool, ownerID int64)
}

// Service loads ReviewPolicy rows and filters approver lists.
type Service struct {
	engine   *xorm.Engine
	agents   AgentLookup
	branches BranchProtector // optional; wired by Init via SetBranchProtector
}

// NewService constructs a Service. agents may be nil — in that case
// FilterApprovers can only filter the literal owner-cluster self-approver.
func NewService(engine *xorm.Engine, agents AgentLookup) *Service {
	return &Service{engine: engine, agents: agents}
}

// Load fetches the ReviewPolicy for ownerID. If no row exists, Load returns
// the default policy (RequireHumanOnly: true) — Cairn's AI-native default.
func (s *Service) Load(ctx context.Context, ownerID int64) (*cairnmodels.ReviewPolicy, error) {
	row := &cairnmodels.ReviewPolicy{}
	has, err := s.engine.Context(ctx).Where("owner_id = ?", ownerID).Get(row)
	if err != nil {
		return nil, fmt.Errorf("reviewpolicy: load: %w", err)
	}
	if !has {
		return &cairnmodels.ReviewPolicy{OwnerID: ownerID, RequireHumanOnly: true}, nil
	}
	return row, nil
}

// RequireHumanOnly is a convenience wrapper around Load.
//
// On error, RequireHumanOnly fails closed and returns true: if we can't read
// policy, default to enforcing — never silently allow agent self-approval due
// to a DB hiccup.
func (s *Service) RequireHumanOnly(ctx context.Context, ownerID int64) bool {
	p, err := s.Load(ctx, ownerID)
	if err != nil {
		// Fail closed — if we can't read policy, default to enforcing.
		// Never silently allow agent self-approval due to a DB hiccup.
		return true
	}
	return p.RequireHumanOnly
}
