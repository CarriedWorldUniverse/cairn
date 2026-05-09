// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import (
	"context"

	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
)

// FilterApprovers returns the subset of approvers that count toward Forgejo's
// "X approving reviews required" gate, given the review policy at ownerID.
//
// When RequireHumanOnly is true:
//   - All agent users are dropped (their approvals don't count toward the gate).
//   - The literal PR-author-cluster owner is dropped (no self-approval). Pass
//     prAuthorOwnerID = the human owner of the PR's author (the author user ID
//     itself if the author is human; the agent's owner_id if the author is an
//     agent).
//
// When RequireHumanOnly is false: approvers passes through unchanged — that's
// Forgejo's vanilla gate behavior.
//
// If the Service was constructed without an AgentLookup, FilterApprovers can
// still drop the literal owner-cluster self-approver (defensive minimum).
func (s *Service) FilterApprovers(ctx context.Context, ownerID, prAuthorOwnerID int64, approvers []*user_model.User) []*user_model.User {
	if !s.RequireHumanOnly(ctx, ownerID) {
		return approvers
	}
	if s.agents == nil {
		// No lookup configured — can only filter the literal owner.
		out := make([]*user_model.User, 0, len(approvers))
		for _, u := range approvers {
			if u == nil {
				continue
			}
			if u.ID == prAuthorOwnerID {
				continue
			}
			out = append(out, u)
		}
		return out
	}
	out := make([]*user_model.User, 0, len(approvers))
	for _, u := range approvers {
		if u == nil {
			continue
		}
		// Drop the literal PR-author-cluster owner (self-approval block).
		if u.ID == prAuthorOwnerID {
			continue
		}
		// Drop any agent user — their approvals never count under
		// RequireHumanOnly, regardless of which owner-cluster they belong to.
		isAgent, _ := s.agents.IsAgentUser(ctx, u.ID)
		if isAgent {
			continue
		}
		out = append(out, u)
	}
	return out
}
