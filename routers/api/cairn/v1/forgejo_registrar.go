// Cairn-specific code; AGPLv3. See LICENSING.md.

package v1

import (
	"context"
	"fmt"

	asymkey_model "github.com/CarriedWorldUniverse/cairn/models/asymkey"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// forgejoRegistrar implements cairnidentity.AgentUserRegistrar against
// Forgejo's user + asymkey models. The agent user gets login
// "nexus-{slug}" with email "nexus-{slug}@{domain}" and a generated
// fullname "Agent: {slug}". The user is non-admin and active so the
// agent can sign in over HTTP (via personal access token) but cannot
// administer the instance.
type forgejoRegistrar struct{}

// NewForgejoRegistrar returns the production AgentUserRegistrar.
func NewForgejoRegistrar() cairnidentity.AgentUserRegistrar {
	return &forgejoRegistrar{}
}

func agentLogin(slug string) string {
	return "nexus-" + slug
}

func agentEmail(slug, domain string) string {
	return "nexus-" + slug + "@" + domain
}

func (r *forgejoRegistrar) FindOrCreateAgentUser(ctx context.Context, slug, domain string) (int64, error) {
	login := agentLogin(slug)
	if u, err := user_model.GetUserByName(ctx, login); err == nil {
		return u.ID, nil
	} else if !user_model.IsErrUserNotExist(err) {
		return 0, fmt.Errorf("cairn registrar: lookup %q: %w", login, err)
	}
	// Create.
	u := &user_model.User{
		Name:        login,
		LowerName:   login,
		FullName:    "Agent: " + slug,
		Email:       agentEmail(slug, domain),
		Passwd:      "",
		LoginType:   0,
		LoginSource: 0,
		IsAdmin:     false,
		IsActive:    true,
	}
	if err := user_model.CreateUser(ctx, u); err != nil {
		return 0, fmt.Errorf("cairn registrar: create %q: %w", login, err)
	}
	return u.ID, nil
}

func (r *forgejoRegistrar) RegisterPubkey(ctx context.Context, userID int64, pubkeyContent, name string) (int64, error) {
	key, err := asymkey_model.AddPublicKey(ctx, userID, name, pubkeyContent, 0)
	if err != nil {
		return 0, fmt.Errorf("cairn registrar: AddPublicKey: %w", err)
	}
	return key.ID, nil
}

func (r *forgejoRegistrar) GetPubkeyContent(ctx context.Context, publicKeyID int64) (string, error) {
	key, err := asymkey_model.GetPublicKeyByID(ctx, publicKeyID)
	if err != nil {
		return "", fmt.Errorf("cairn registrar: GetPublicKeyByID: %w", err)
	}
	return key.Content, nil
}
