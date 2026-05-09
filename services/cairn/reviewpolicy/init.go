// Cairn-specific code; AGPLv3. See LICENSING.md.
//
// Init constructs the production review-policy Service, registers it as the
// process-wide global, AND wires the model-layer hook that
// models/issues.GetGrantedApprovalsCount calls. The model-layer hook is the
// load-bearing integration point — without it the service is dead code.

package reviewpolicy

import (
	"context"

	"xorm.io/xorm"

	git_model "github.com/CarriedWorldUniverse/cairn/models/git"
	issues_model "github.com/CarriedWorldUniverse/cairn/models/issues"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
)

// productionBranchProtector wires BranchProtector to git_model.
type productionBranchProtector struct{}

func (productionBranchProtector) GetRule(ctx context.Context, repoID int64, ruleName string) (*git_model.ProtectedBranch, error) {
	return git_model.GetProtectedBranchRuleByName(ctx, repoID, ruleName)
}

func (productionBranchProtector) CreateRule(ctx context.Context, repo *repo_model.Repository, rule *git_model.ProtectedBranch) error {
	return git_model.UpdateProtectBranch(ctx, repo, rule, git_model.WhitelistOptions{})
}

// Init constructs the Service, sets it as global, and registers the
// approval-count + PR-author-cluster hooks in models/issues.
//
// Wired into routers/init.go::initCairn alongside the summarizer init.
func Init(engine *xorm.Engine) {
	adapter := newProductionAdapter()
	svc := NewService(engine, adapter)
	svc.SetBranchProtector(productionBranchProtector{})
	SetGlobal(svc)

	// Register the approval-count filter hook. The hook closes over the
	// global service rather than the local handle so that a future
	// SetGlobal (test reset, hot-reload) takes effect without re-registering.
	issues_model.SetCairnApprovalFilter(func(ctx context.Context, repoOwnerID, prAuthorClusterOwnerID int64, ids []int64) []int64 {
		s := Global()
		if s == nil {
			return ids
		}
		return s.FilterApproverIDs(ctx, repoOwnerID, prAuthorClusterOwnerID, ids)
	})

	// Register the PR-author-cluster resolver hook. For a human poster this
	// returns the poster ID; for an agent-user poster, this returns the
	// agent's owner ID (the human controlling that agent's cluster).
	issues_model.SetCairnPRAuthorClusterResolver(func(ctx context.Context, posterID int64) int64 {
		s := Global()
		if s == nil {
			return posterID
		}
		isAgent, ownerID := s.IsAgentUser(ctx, posterID)
		if isAgent {
			return ownerID
		}
		return posterID
	})
}

// Shutdown clears the registered model hooks. Intended for tests; in
// production the process exits and there's nothing to unwind.
func Shutdown() {
	issues_model.SetCairnApprovalFilter(nil)
	issues_model.SetCairnPRAuthorClusterResolver(nil)
	SetGlobal(nil)
}
