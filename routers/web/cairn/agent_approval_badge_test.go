// Cairn-specific code; AGPLv3. See LICENSING.md.

package cairn

import (
	"context"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
	"github.com/CarriedWorldUniverse/cairn/services/cairn/reviewpolicy"
)

// stubAgentLookup answers "is this user an agent? if so who owns it?"
type stubAgentLookup struct {
	agents map[int64]int64 // userID -> ownerID
}

func (s *stubAgentLookup) IsAgentUser(_ context.Context, userID int64) (bool, int64) {
	owner, has := s.agents[userID]
	return has, owner
}

// installSvc wires a reviewpolicy.Service into the global handle for the
// duration of the test, restoring nil afterwards.
func installSvc(t *testing.T, lookup reviewpolicy.AgentLookup, disablePolicyForOwner int64) {
	t.Helper()
	eng := cairntest.NewEngine(t)
	if disablePolicyForOwner != 0 {
		if _, err := eng.Insert(&cairnmodels.ReviewPolicy{OwnerID: disablePolicyForOwner, RequireHumanOnly: false}); err != nil {
			t.Fatalf("seed policy: %v", err)
		}
	}
	svc := reviewpolicy.NewService(eng, lookup)
	reviewpolicy.SetGlobal(svc)
	t.Cleanup(func() { reviewpolicy.SetGlobal(nil) })
}

func TestAgentApprovalDoesNotCount_NilService(t *testing.T) {
	// No SetGlobal call — service handle is nil.
	reviewpolicy.SetGlobal(nil)
	if AgentApprovalDoesNotCount(context.Background(), 99, 1, 100) {
		t.Errorf("nil service should return false")
	}
}

func TestAgentApprovalDoesNotCount_PolicyDisabled(t *testing.T) {
	// Policy explicitly disabled for owner 99.
	installSvc(t, &stubAgentLookup{agents: map[int64]int64{100: 1}}, 99)

	// Even an agent reviewer should not be flagged when the policy is off.
	if AgentApprovalDoesNotCount(context.Background(), 99, 5 /*human poster*/, 100 /*agent reviewer*/) {
		t.Errorf("policy disabled: expected false")
	}
}

func TestAgentApprovalDoesNotCount_OwnerSelfApproval(t *testing.T) {
	// PR posted by user-1's agent (ID 100). Reviewer is user 1 themselves —
	// owner-cluster self-approval, which the filter drops.
	installSvc(t, &stubAgentLookup{agents: map[int64]int64{100: 1}}, 0)

	if !AgentApprovalDoesNotCount(context.Background(), 99 /*owner*/, 100 /*PR poster=agent*/, 1 /*reviewer=cluster owner*/) {
		t.Errorf("owner-cluster self-approval: expected true")
	}
}

func TestAgentApprovalDoesNotCount_OwnerSelfApprovalHumanPoster(t *testing.T) {
	// Reviewer is the human PR poster themselves — also a self-approval.
	installSvc(t, &stubAgentLookup{agents: map[int64]int64{}}, 0)

	if !AgentApprovalDoesNotCount(context.Background(), 99, 5 /*human poster*/, 5 /*reviewer=poster*/) {
		t.Errorf("human-poster self-approval: expected true")
	}
}

func TestAgentApprovalDoesNotCount_AgentReviewer(t *testing.T) {
	// Reviewer is an agent (user 100, owned by user 1). Policy enabled.
	installSvc(t, &stubAgentLookup{agents: map[int64]int64{100: 1}}, 0)

	if !AgentApprovalDoesNotCount(context.Background(), 99, 5 /*unrelated human poster*/, 100 /*agent reviewer*/) {
		t.Errorf("agent reviewer: expected true")
	}
}

func TestAgentApprovalDoesNotCount_HumanReviewer(t *testing.T) {
	// Reviewer is a non-agent human, unrelated to the PR-author cluster.
	installSvc(t, &stubAgentLookup{agents: map[int64]int64{100: 1}}, 0)

	if AgentApprovalDoesNotCount(context.Background(), 99, 5 /*human poster*/, 7 /*unrelated human reviewer*/) {
		t.Errorf("unrelated human reviewer: expected false")
	}
}
