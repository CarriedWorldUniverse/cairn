// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import (
	"context"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
	git_model "github.com/CarriedWorldUniverse/cairn/models/git"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
)

// fakeBranchProtector is an in-memory BranchProtector for tests.
// Avoids dragging the full repo/user/access scaffolding into a unit test —
// the load-bearing semantics being verified here are policy-gate + idempotency,
// not Forgejo's branch-protection internals.
type fakeBranchProtector struct {
	rules    map[string]*git_model.ProtectedBranch // key: "repoID/ruleName"
	getErr   error
	createrr error
	creates  []string // ruleNames created, in order
}

func newFakeBP() *fakeBranchProtector {
	return &fakeBranchProtector{rules: map[string]*git_model.ProtectedBranch{}}
}

func (f *fakeBranchProtector) key(repoID int64, name string) string {
	return name // tests use a single repo, so name alone is unique enough
}

func (f *fakeBranchProtector) GetRule(_ context.Context, repoID int64, ruleName string) (*git_model.ProtectedBranch, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.rules[f.key(repoID, ruleName)], nil
}

func (f *fakeBranchProtector) CreateRule(_ context.Context, repo *repo_model.Repository, rule *git_model.ProtectedBranch) error {
	if f.createrr != nil {
		return f.createrr
	}
	f.rules[f.key(repo.ID, rule.RuleName)] = rule
	f.creates = append(f.creates, rule.RuleName)
	return nil
}

func TestAutoApplyDefaultProtection_RequireHumanOnly_CreatesRules(t *testing.T) {
	eng := cairntest.NewEngine(t)
	// Default policy (no row) is RequireHumanOnly=true.
	svc := NewService(eng, nil)
	bp := newFakeBP()
	svc.SetBranchProtector(bp)

	repo := &repo_model.Repository{ID: 100, OwnerID: 42}
	if err := svc.AutoApplyDefaultProtection(context.Background(), repo); err != nil {
		t.Fatalf("AutoApplyDefaultProtection: %v", err)
	}

	if len(bp.creates) != 2 {
		t.Fatalf("expected 2 rules created (main, master), got %d: %v", len(bp.creates), bp.creates)
	}
	mainRule := bp.rules["main"]
	if mainRule == nil {
		t.Fatal("main rule not created")
	}
	if mainRule.RequiredApprovals != 1 {
		t.Errorf("main RequiredApprovals: want 1, got %d", mainRule.RequiredApprovals)
	}
	if !mainRule.BlockOnRejectedReviews {
		t.Error("main BlockOnRejectedReviews: want true")
	}
	if mainRule.RepoID != 100 {
		t.Errorf("main RepoID: want 100, got %d", mainRule.RepoID)
	}
	if bp.rules["master"] == nil {
		t.Error("master rule not created")
	}
}

func TestAutoApplyDefaultProtection_PolicyDisabled_NoRules(t *testing.T) {
	eng := cairntest.NewEngine(t)
	// Seed an explicit RequireHumanOnly=false row.
	if _, err := eng.Insert(&cairnmodels.ReviewPolicy{OwnerID: 42, RequireHumanOnly: false}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := NewService(eng, nil)
	bp := newFakeBP()
	svc.SetBranchProtector(bp)

	repo := &repo_model.Repository{ID: 100, OwnerID: 42}
	if err := svc.AutoApplyDefaultProtection(context.Background(), repo); err != nil {
		t.Fatalf("AutoApplyDefaultProtection: %v", err)
	}

	if len(bp.creates) != 0 {
		t.Errorf("policy disabled — expected NO rules created, got %d: %v", len(bp.creates), bp.creates)
	}
}

func TestAutoApplyDefaultProtection_ExistingRule_NotOverwritten(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := NewService(eng, nil) // default RequireHumanOnly=true
	bp := newFakeBP()
	// Pre-seed an operator-set rule on main with different settings.
	preexisting := &git_model.ProtectedBranch{
		RepoID:            100,
		RuleName:          "main",
		RequiredApprovals: 5, // operator wanted 5 approvals — must not be lowered to 1
	}
	bp.rules["main"] = preexisting
	svc.SetBranchProtector(bp)

	repo := &repo_model.Repository{ID: 100, OwnerID: 42}
	if err := svc.AutoApplyDefaultProtection(context.Background(), repo); err != nil {
		t.Fatalf("AutoApplyDefaultProtection: %v", err)
	}

	// main was preexisting → only master should have been created.
	if len(bp.creates) != 1 || bp.creates[0] != "master" {
		t.Errorf("expected only master created, got: %v", bp.creates)
	}
	// main's preexisting settings unchanged.
	if got := bp.rules["main"]; got != preexisting || got.RequiredApprovals != 5 {
		t.Errorf("main rule was overwritten: %+v", got)
	}
}

func TestAutoApplyDefaultProtection_NilRepo_NoOp(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := NewService(eng, nil)
	bp := newFakeBP()
	svc.SetBranchProtector(bp)

	if err := svc.AutoApplyDefaultProtection(context.Background(), nil); err != nil {
		t.Fatalf("nil repo: %v", err)
	}
	if len(bp.creates) != 0 {
		t.Error("nil repo should be no-op")
	}
}

func TestAutoApplyDefaultProtection_NoBranchProtectorWired_NoError(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := NewService(eng, nil) // no SetBranchProtector

	repo := &repo_model.Repository{ID: 100, OwnerID: 42}
	if err := svc.AutoApplyDefaultProtection(context.Background(), repo); err != nil {
		t.Errorf("missing protector should be a soft no-op, got err: %v", err)
	}
}
