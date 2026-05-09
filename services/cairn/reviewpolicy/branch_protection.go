// Cairn-specific code; AGPLv3. See LICENSING.md.
//
// Auto-apply default branch protection on newly-created repositories when the
// owning org has RequireHumanOnly=true. This is the "AI-native default":
// new repos in an enforcing org get main/master locked behind 1 approving
// review automatically. Operator-set rules win — we never overwrite.

package reviewpolicy

import (
	"context"

	git_model "github.com/CarriedWorldUniverse/cairn/models/git"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
)

// BranchProtector is the model-layer surface AutoApplyDefaultProtection needs.
// Production is wired to git_model functions in init.go via a thin adapter;
// tests inject a fake to avoid pulling the full repo/user/access scaffolding
// into a unit test.
type BranchProtector interface {
	GetRule(ctx context.Context, repoID int64, ruleName string) (*git_model.ProtectedBranch, error)
	CreateRule(ctx context.Context, repo *repo_model.Repository, rule *git_model.ProtectedBranch) error
}

// SetBranchProtector installs the BranchProtector implementation on the
// service. Init wires the production adapter; tests override.
func (s *Service) SetBranchProtector(bp BranchProtector) { s.branches = bp }

// AutoApplyDefaultProtection adds branch-protection on main/master requiring
// 1 approving review when the org has RequireHumanOnly=true. Idempotent: if
// a rule already exists for the branch, this is a no-op (operator wins).
//
// Failure is the caller's to log-vs-propagate. The repo-creation hook logs
// and continues — branch-protection auto-apply must never block repo
// creation.
func (s *Service) AutoApplyDefaultProtection(ctx context.Context, repo *repo_model.Repository) error {
	if repo == nil {
		return nil
	}
	if !s.RequireHumanOnly(ctx, repo.OwnerID) {
		return nil
	}
	if s.branches == nil {
		// No protector wired (e.g. test that exercises only the policy gate).
		// Nothing more to do; the policy gate decision is the load-bearing bit.
		return nil
	}
	for _, branchName := range []string{"main", "master"} {
		existing, err := s.branches.GetRule(ctx, repo.ID, branchName)
		if err != nil {
			return err
		}
		if existing != nil {
			continue // operator-set rule (or earlier auto-apply) wins
		}
		rule := &git_model.ProtectedBranch{
			RepoID:                 repo.ID,
			RuleName:               branchName,
			RequiredApprovals:      1,
			BlockOnRejectedReviews: true,
		}
		if err := s.branches.CreateRule(ctx, repo, rule); err != nil {
			return err
		}
	}
	return nil
}
