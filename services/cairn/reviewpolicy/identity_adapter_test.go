// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import (
	"context"
	"errors"
	"testing"

	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
)

func TestIdentityAdapter_HumanUser(t *testing.T) {
	a := identityAdapter{
		userByID: func(_ context.Context, id int64) (*user_model.User, error) {
			return &user_model.User{ID: id, Email: "nexus@darksoft.co.nz"}, nil
		},
		agentByEmail: func(_ context.Context, _, _ string) (int64, bool, error) {
			t.Fatal("agentByEmail must not be called for non-agent email")
			return 0, false, nil
		},
	}
	isAgent, owner := a.IsAgentUser(context.Background(), 7)
	if isAgent || owner != 0 {
		t.Errorf("expected (false, 0); got (%v, %d)", isAgent, owner)
	}
}

func TestIdentityAdapter_AgentUser(t *testing.T) {
	a := identityAdapter{
		userByID: func(_ context.Context, id int64) (*user_model.User, error) {
			return &user_model.User{ID: id, Email: "nexus-plumb@darksoft.co.nz"}, nil
		},
		agentByEmail: func(_ context.Context, slug, domain string) (int64, bool, error) {
			if slug != "plumb" || domain != "darksoft.co.nz" {
				t.Fatalf("unexpected (slug=%q, domain=%q)", slug, domain)
			}
			return 42, true, nil
		},
	}
	isAgent, owner := a.IsAgentUser(context.Background(), 100)
	if !isAgent || owner != 42 {
		t.Errorf("expected (true, 42); got (%v, %d)", isAgent, owner)
	}
}

func TestIdentityAdapter_AgentEmailButNoRecord(t *testing.T) {
	a := identityAdapter{
		userByID: func(_ context.Context, id int64) (*user_model.User, error) {
			return &user_model.User{ID: id, Email: "nexus-ghost@example.com"}, nil
		},
		agentByEmail: func(_ context.Context, _, _ string) (int64, bool, error) {
			return 0, false, nil
		},
	}
	isAgent, owner := a.IsAgentUser(context.Background(), 100)
	if isAgent || owner != 0 {
		t.Errorf("expected (false, 0); got (%v, %d)", isAgent, owner)
	}
}

func TestIdentityAdapter_UserLookupError_FailsClosed(t *testing.T) {
	// On user-lookup error we treat as "not an agent" — failing closed in
	// this direction matches vanilla Forgejo behaviour for unrecognised IDs.
	a := identityAdapter{
		userByID: func(_ context.Context, _ int64) (*user_model.User, error) {
			return nil, errors.New("boom")
		},
		agentByEmail: func(_ context.Context, _, _ string) (int64, bool, error) {
			t.Fatal("agentByEmail must not be called when user lookup errored")
			return 0, false, nil
		},
	}
	isAgent, owner := a.IsAgentUser(context.Background(), 100)
	if isAgent || owner != 0 {
		t.Errorf("expected (false, 0) on user-lookup error; got (%v, %d)", isAgent, owner)
	}
}

func TestIdentityAdapter_UserNotFound(t *testing.T) {
	a := identityAdapter{
		userByID: func(_ context.Context, _ int64) (*user_model.User, error) {
			return nil, nil // adapter normalises ErrUserNotExist to (nil, nil)
		},
		agentByEmail: func(_ context.Context, _, _ string) (int64, bool, error) {
			t.Fatal("agentByEmail must not be called for missing user")
			return 0, false, nil
		},
	}
	isAgent, owner := a.IsAgentUser(context.Background(), 100)
	if isAgent || owner != 0 {
		t.Errorf("expected (false, 0); got (%v, %d)", isAgent, owner)
	}
}

func TestIdentityAdapter_NilLookups(t *testing.T) {
	a := identityAdapter{}
	isAgent, owner := a.IsAgentUser(context.Background(), 100)
	if isAgent || owner != 0 {
		t.Errorf("expected (false, 0) when adapter is unconfigured; got (%v, %d)", isAgent, owner)
	}
}
