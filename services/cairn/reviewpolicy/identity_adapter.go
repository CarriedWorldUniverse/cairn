// Cairn-specific code; AGPLv3. See LICENSING.md.
//
// Production AgentLookup adapter. Bridges the review-policy filter to the
// existing services/cairn/identity service.
//
// Agent identity in Cairn is keyed off the Forgejo user's email address:
// agents have email `nexus-<slug>@<domain>` per Plan 1. So mapping a
// Forgejo user_id to "is this an agent? who's the owner?" goes:
//   user_id → user.Email → ParseAgentEmail → identity.GetByEmail
//   → Agent.UserID (the owner's Forgejo user_id).

package reviewpolicy

import (
	"context"
	"errors"

	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// userByID is the user-lookup function the adapter calls. Production passes
// user_model.GetUserByID; tests inject a fake.
type userByID func(ctx context.Context, id int64) (*user_model.User, error)

// agentByEmail looks up an agent record by (slug, domain). Production passes
// identity.GlobalAgentService().GetByEmail; tests inject a fake.
type agentByEmail func(ctx context.Context, slug, domain string) (ownerUserID int64, isAgent bool, err error)

type identityAdapter struct {
	userByID     userByID
	agentByEmail agentByEmail
}

// IsAgentUser implements AgentLookup. Returns (false, 0) on any lookup error
// — failing closed in this direction means an unrecognised user is treated
// as a human, which is the same behaviour vanilla Forgejo would give. The
// inverse failure mode (treating a real human as an agent) would be far
// more disruptive.
func (a identityAdapter) IsAgentUser(ctx context.Context, userID int64) (bool, int64) {
	if a.userByID == nil || a.agentByEmail == nil {
		return false, 0
	}
	u, err := a.userByID(ctx, userID)
	if err != nil || u == nil {
		return false, 0
	}
	slug, domain, ok := identity.ParseAgentEmail(u.Email)
	if !ok {
		return false, 0
	}
	ownerID, isAgent, err := a.agentByEmail(ctx, slug, domain)
	if err != nil || !isAgent {
		return false, 0
	}
	return true, ownerID
}

// newProductionAdapter wires the adapter to live Forgejo + Cairn services.
func newProductionAdapter() identityAdapter {
	return identityAdapter{
		userByID: func(ctx context.Context, id int64) (*user_model.User, error) {
			u, err := user_model.GetUserByID(ctx, id)
			if err != nil {
				if user_model.IsErrUserNotExist(err) {
					return nil, nil
				}
				return nil, err
			}
			return u, nil
		},
		agentByEmail: func(ctx context.Context, slug, domain string) (int64, bool, error) {
			svc := identity.GlobalAgentService()
			if svc == nil {
				return 0, false, nil
			}
			agent, err := svc.GetByEmail(ctx, slug, domain)
			if err != nil {
				if errors.Is(err, identity.ErrAgentNotFound) {
					return 0, false, nil
				}
				return 0, false, err
			}
			if agent == nil {
				return 0, false, nil
			}
			return agent.UserID, true, nil
		},
	}
}
