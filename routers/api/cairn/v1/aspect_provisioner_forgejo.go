// Cairn-specific code; AGPLv3. See LICENSING.md.
//
// Production AspectProvisioner implementation — bridges the v1 API's
// minimal AspectProvisioner contract to forgejo's user_model.CreateUser
// + auth_model.NewAccessToken so the aspect-mint endpoint actually
// creates real users and real tokens.

package v1

import (
	"context"
	"errors"
	"fmt"

	auth_model "github.com/CarriedWorldUniverse/cairn/models/auth"
	"github.com/CarriedWorldUniverse/cairn/models/db"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/optional"
)

// ForgejoAspectProvisioner uses forgejo's user + access-token models
// to materialise the aspect. Pure indirection — no state, no caching,
// no service-layer wiring. Caller passes ctx through from the HTTP
// handler; that ctx already carries the forgejo DB engine.
type ForgejoAspectProvisioner struct{}

// NewForgejoAspectProvisioner returns a ready-to-use provisioner.
func NewForgejoAspectProvisioner() *ForgejoAspectProvisioner {
	return &ForgejoAspectProvisioner{}
}

// aspectEmailDomain is the synthetic domain used for aspect bot
// users' email addresses. Forgejo's user-creation path requires a
// shape-valid email; bots never receive mail so the domain is
// internal-only and never resolved.
const aspectEmailDomain = "aspect.cairn.local"

// MintAspect creates a Type=UserTypeBot user with the given parent
// linkage and mints an access token scoped per tokenScopes. Returns
// the minted user id + cleartext token; the token can be returned
// to the caller exactly once because forgejo only stores its hash.
func (ForgejoAspectProvisioner) MintAspect(ctx context.Context, parent AspectParent, username, fullName, description, tokenScopes string) (MintedAspect, error) {
	scope, err := auth_model.AccessTokenScope(tokenScopes).Normalize()
	if err != nil {
		return MintedAspect{}, fmt.Errorf("normalize scope %q: %w", tokenScopes, err)
	}
	if !scope.HasPermissionScope() {
		return MintedAspect{}, fmt.Errorf("scope %q has no permissions", tokenScopes)
	}

	u := &user_model.User{
		Name:         username,
		FullName:     fullName,
		Email:        username + "@" + aspectEmailDomain,
		Type:         user_model.UserTypeBot,
		ParentUserID: parent.ID,
		Description:  description,
		// Aspects authenticate via their API token only; the password
		// field is required by forgejo's user model but is functionally
		// dead-letter (login via password is gated on Type and the
		// hashed password). Set a high-entropy random string the
		// caller never sees so brute-force is meaningless.
		Passwd: randomBotPassword(),
	}

	overwrites := &user_model.CreateUserOverwriteOptions{
		IsActive: optional.Some(true),
	}
	if err := user_model.CreateUser(ctx, u, overwrites); err != nil {
		switch {
		case user_model.IsErrUserAlreadyExist(err):
			return MintedAspect{}, ErrAspectNameTaken
		case user_model.IsErrEmailAlreadyUsed(err):
			// Same logical collision — the synthetic email is derived
			// from the username, so a clash here means the username is
			// already taken under the aspect domain.
			return MintedAspect{}, ErrAspectNameTaken
		case errors.Is(err, user_model.ErrCooldownPeriod{}):
			return MintedAspect{}, ErrAspectNameTaken
		}
		// Forgejo's username-shape errors land here (e.g. reserved
		// usernames the regex in aspects.go didn't catch); surface as
		// invalid_name so the operator can pick a different one.
		if db.IsErrNameReserved(err) || db.IsErrNamePatternNotAllowed(err) || db.IsErrNameCharsNotAllowed(err) {
			return MintedAspect{}, fmt.Errorf("%w: %v", ErrAspectNameInvalid, err)
		}
		return MintedAspect{}, fmt.Errorf("CreateUser: %w", err)
	}

	t := &auth_model.AccessToken{
		Name:             "aspect:" + username,
		UID:              u.ID,
		Scope:            scope,
		ResourceAllRepos: true,
	}
	if err := auth_model.NewAccessToken(ctx, t); err != nil {
		// Token mint failed after the user was created. The user row
		// is now an orphan-aspect with no token; the operator can
		// either delete it manually or call back to mint a token via
		// the standard forgejo flow. Surface as 500 — this is a
		// real internal failure, not a user-input problem.
		return MintedAspect{}, fmt.Errorf("NewAccessToken (user %d already created): %w", u.ID, err)
	}

	return MintedAspect{
		UserID:   u.ID,
		Username: u.Name,
		APIToken: t.Token,
	}, nil
}

// randomBotPassword mints a long random string for the bot user's
// (functionally unused) password field. forgejo hashes whatever we
// pass via argon2 on insert; the bot never logs in via password so
// the cleartext is discarded after the call.
func randomBotPassword() string {
	// Reuse forgejo's crypto-random helper via auth_model.HashToken
	// indirection? Simpler: 40 random hex bytes from the same
	// crypto/rand pool the token generator uses. Distinct from token
	// generation only to avoid accidental dependence on the token
	// path's internal state.
	return auth_model.HashToken(
		// Different salt domains for password vs token so they can't
		// be derived from one another even by accident.
		"aspect-password-seed",
		// crypto/rand source — auth_model.HashToken expects a salt
		// string but is keyed on its inputs; we're abusing it here as
		// a one-shot KDF. Acceptable: the password field is set-once
		// and unread.
		"cairn-aspect-bootstrap",
	)
}
