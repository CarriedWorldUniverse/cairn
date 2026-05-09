// Cairn-specific code; AGPLv3. See LICENSING.md.

package cairn

import (
	"context"

	"github.com/CarriedWorldUniverse/cairn/services/cairn/reviewpolicy"
)

// AgentApprovalDoesNotCount reports whether a given reviewer's approval
// would be filtered out by Cairn's review-policy gate (and therefore
// shouldn't visually look like it advances the merge gate). Used by the
// PR page to render a "doesn't count toward gate" badge next to the
// reviewer's entry, so humans aren't confused by visible approvals that
// aren't actually being counted.
//
// Inputs mirror what the template has on hand:
//   - ownerID: repo owner ID (for the policy lookup; matches what
//     models/issues passes into FilterApproverIDs).
//   - prPosterID: PR author user ID (Issue.PosterID). This may be a human
//     or an agent — the function resolves the PR-author-cluster owner
//     internally, the same way the model-layer resolver does at init.
//   - reviewerID: the reviewer (Review.ReviewerID).
//
// Returns false if no review-policy service is wired (tests, shutdown,
// or a non-Cairn build) or if the policy doesn't require human-only.
func AgentApprovalDoesNotCount(ctx context.Context, ownerID, prPosterID, reviewerID int64) bool {
	svc := reviewpolicy.Global()
	if svc == nil {
		return false
	}
	if !svc.RequireHumanOnly(ctx, ownerID) {
		return false
	}
	// Resolve the PR-author-cluster owner internally — for a human poster
	// that's the poster ID itself; for an agent poster it's the owner of
	// the agent's cluster. This mirrors the resolver registered at init.
	prAuthorOwnerID := prPosterID
	if isAgent, agentOwner := svc.IsAgentUser(ctx, prPosterID); isAgent {
		prAuthorOwnerID = agentOwner
	}
	// Reviewer is the literal owner of the agent's PR-author cluster →
	// owner-cluster self-approval, dropped by the filter.
	if reviewerID == prAuthorOwnerID {
		return true
	}
	// Reviewer is an agent user → dropped by the filter regardless of
	// which cluster they belong to.
	isAgent, _ := svc.IsAgentUser(ctx, reviewerID)
	return isAgent
}
