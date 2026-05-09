// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import (
	"context"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
)

// stubAgentLookup answers "is user X an agent? if so, who's the owner?"
type stubAgentLookup struct {
	agents map[int64]int64 // userID -> ownerID (entry present iff the user is an agent)
}

func (s *stubAgentLookup) IsAgentUser(_ context.Context, userID int64) (bool, int64) {
	owner, has := s.agents[userID]
	return has, owner
}

func TestFilterApprovers_DropsAgents(t *testing.T) {
	eng := cairntest.NewEngine(t)
	lookup := &stubAgentLookup{agents: map[int64]int64{
		100: 1, // agent owned by user 1
		101: 2, // agent owned by user 2
	}}
	svc := NewService(eng, lookup)

	approvers := []*user_model.User{
		{ID: 100}, // user-1's agent — drop (it's an agent)
		{ID: 101}, // user-2's agent — drop (it's an agent, regardless of owner)
		{ID: 5},   // human reviewer — keep
	}
	out := svc.FilterApprovers(context.Background(), 1 /*ownerID*/, 1 /*PR author cluster*/, approvers)
	if len(out) != 1 {
		t.Fatalf("expected 1 approver, got %d (%v)", len(out), out)
	}
	if out[0].ID != 5 {
		t.Errorf("expected human (id=5), got id=%d", out[0].ID)
	}
}

func TestFilterApprovers_DropsOwnerClusterSelfApproval(t *testing.T) {
	eng := cairntest.NewEngine(t)
	lookup := &stubAgentLookup{agents: map[int64]int64{
		100: 1, // agent owned by user 1
	}}
	svc := NewService(eng, lookup)

	// PR authored by user-1's agent (PR-author-cluster owner = 1).
	approvers := []*user_model.User{
		{ID: 1}, // user 1 themselves — must drop (self-approval of own agent's PR)
		{ID: 5}, // unrelated human — keep
	}
	out := svc.FilterApprovers(context.Background(), 99 /*org owner*/, 1 /*PR author cluster*/, approvers)
	if len(out) != 1 || out[0].ID != 5 {
		t.Errorf("expected only id=5, got %v", out)
	}
}

func TestFilterApprovers_PolicyDisabled(t *testing.T) {
	eng := cairntest.NewEngine(t)
	if _, err := eng.Insert(&cairnmodels.ReviewPolicy{OwnerID: 99, RequireHumanOnly: false}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	lookup := &stubAgentLookup{agents: map[int64]int64{100: 1}}
	svc := NewService(eng, lookup)

	approvers := []*user_model.User{{ID: 100}, {ID: 5}}
	out := svc.FilterApprovers(context.Background(), 99, 1, approvers)
	if len(out) != 2 {
		t.Errorf("policy disabled should pass through unchanged, got %d approvers", len(out))
	}
}

func TestFilterApproverIDs_DropsAgentsAndOwnerCluster(t *testing.T) {
	eng := cairntest.NewEngine(t)
	lookup := &stubAgentLookup{agents: map[int64]int64{
		100: 1, // agent owned by user 1
	}}
	svc := NewService(eng, lookup)

	// PR authored by user-1's agent (PR-author-cluster owner = 1).
	ids := []int64{
		1,   // owner-cluster self-approver — drop
		100, // agent — drop
		5,   // human — keep
	}
	out := svc.FilterApproverIDs(context.Background(), 99, 1, ids)
	if len(out) != 1 || out[0] != 5 {
		t.Errorf("expected [5], got %v", out)
	}
}

func TestFilterApproverIDs_PolicyDisabledPassesThrough(t *testing.T) {
	eng := cairntest.NewEngine(t)
	if _, err := eng.Insert(&cairnmodels.ReviewPolicy{OwnerID: 99, RequireHumanOnly: false}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	lookup := &stubAgentLookup{agents: map[int64]int64{100: 1}}
	svc := NewService(eng, lookup)

	ids := []int64{1, 100, 5}
	out := svc.FilterApproverIDs(context.Background(), 99, 1, ids)
	if len(out) != 3 {
		t.Errorf("expected pass-through (3 IDs), got %v", out)
	}
}

func TestFilterApproverIDs_NilLookupFiltersOnlyOwner(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := NewService(eng, nil)

	ids := []int64{1, 100, 5}
	out := svc.FilterApproverIDs(context.Background(), 99, 1, ids)
	if len(out) != 2 {
		t.Fatalf("expected 2, got %v", out)
	}
	for _, id := range out {
		if id == 1 {
			t.Errorf("owner-cluster self-approver should have been dropped")
		}
	}
}

func TestService_IsAgentUser(t *testing.T) {
	eng := cairntest.NewEngine(t)
	lookup := &stubAgentLookup{agents: map[int64]int64{100: 7}}
	svc := NewService(eng, lookup)

	if isAgent, owner := svc.IsAgentUser(context.Background(), 100); !isAgent || owner != 7 {
		t.Errorf("expected (true, 7); got (%v, %d)", isAgent, owner)
	}
	if isAgent, owner := svc.IsAgentUser(context.Background(), 5); isAgent || owner != 0 {
		t.Errorf("expected (false, 0); got (%v, %d)", isAgent, owner)
	}

	// Service with nil lookup → always (false, 0).
	svcNil := NewService(eng, nil)
	if isAgent, owner := svcNil.IsAgentUser(context.Background(), 100); isAgent || owner != 0 {
		t.Errorf("nil lookup: expected (false, 0); got (%v, %d)", isAgent, owner)
	}
}

func TestFilterApprovers_NilLookupFiltersOnlyOwner(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := NewService(eng, nil)

	approvers := []*user_model.User{
		{ID: 1},   // owner of PR-author cluster — drop
		{ID: 100}, // would-be agent, but no lookup → kept
		{ID: 5},   // human — kept
	}
	out := svc.FilterApprovers(context.Background(), 99, 1, approvers)
	if len(out) != 2 {
		t.Fatalf("expected 2 (no lookup, only literal owner dropped), got %d (%v)", len(out), out)
	}
	for _, u := range out {
		if u.ID == 1 {
			t.Errorf("owner-cluster self-approver should have been dropped")
		}
	}
}
