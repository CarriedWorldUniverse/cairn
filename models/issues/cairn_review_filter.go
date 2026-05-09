// Cairn-specific code; AGPLv3. See LICENSING.md.
//
// Hook variable that lets the Cairn review-policy service filter the set of
// reviewer IDs whose approvals count toward Forgejo's "X approving reviews
// required" gate. Lives in models/issues so that GetGrantedApprovalsCount
// (Forgejo upstream code) can call into Cairn without models/issues importing
// services/cairn — that would invert Forgejo's models→services dependency
// direction and create import cycles.
//
// services/cairn/reviewpolicy.Init registers the production filter at startup.
// When unset (Cairn disabled, init skipped, tests), the filter is a no-op and
// approval counting behaves exactly like vanilla Forgejo.

package issues

import (
	"context"
	"sync/atomic"
)

// CairnApprovalFilterFunc is the signature of the hook that filters the
// reviewer-ID set for a PR's approval count. The function receives the
// PR's BaseRepo OwnerID, the PR-author-cluster owner (the human owner of
// the PR's poster — the poster itself if human, the agent's owner if the
// poster is an agent), and the list of reviewer IDs whose approvals would
// otherwise count. It returns the filtered list.
type CairnApprovalFilterFunc func(ctx context.Context, repoOwnerID, prAuthorClusterOwnerID int64, reviewerIDs []int64) []int64

var cairnApprovalFilter atomic.Pointer[CairnApprovalFilterFunc]

// SetCairnApprovalFilter installs the process-wide filter. Pass nil to clear.
// Called by services/cairn/reviewpolicy.Init at startup.
func SetCairnApprovalFilter(f CairnApprovalFilterFunc) {
	if f == nil {
		cairnApprovalFilter.Store(nil)
		return
	}
	cairnApprovalFilter.Store(&f)
}

// cairnFilterReviewerIDs applies the registered filter (if any) to ids.
// When no filter is registered, returns ids unchanged.
func cairnFilterReviewerIDs(ctx context.Context, repoOwnerID, prAuthorClusterOwnerID int64, ids []int64) []int64 {
	p := cairnApprovalFilter.Load()
	if p == nil {
		return ids
	}
	return (*p)(ctx, repoOwnerID, prAuthorClusterOwnerID, ids)
}

// CairnPRAuthorClusterResolverFunc resolves a Forgejo poster user ID to its
// "owner cluster" — the human owner controlling that user. For a human user,
// that's the user themselves. For an agent user (per the identity service),
// that's the agent's registered owner.
type CairnPRAuthorClusterResolverFunc func(ctx context.Context, posterID int64) int64

var cairnPRAuthorClusterResolver atomic.Pointer[CairnPRAuthorClusterResolverFunc]

// SetCairnPRAuthorClusterResolver installs the resolver. Pass nil to clear.
func SetCairnPRAuthorClusterResolver(f CairnPRAuthorClusterResolverFunc) {
	if f == nil {
		cairnPRAuthorClusterResolver.Store(nil)
		return
	}
	cairnPRAuthorClusterResolver.Store(&f)
}

// cairnResolvePRAuthorCluster returns prAuthor's owner-cluster owner ID, or
// posterID itself if no resolver is registered.
func cairnResolvePRAuthorCluster(ctx context.Context, posterID int64) int64 {
	p := cairnPRAuthorClusterResolver.Load()
	if p == nil {
		return posterID
	}
	return (*p)(ctx, posterID)
}
