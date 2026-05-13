// Cairn-specific code; AGPLv3. See LICENSING.md.
//
// Aspect provisioning — Phase 1 of the AI-first identity layer.
// Authenticated humans can mint a derived bot user (an "aspect") that
// will act on their behalf via tokens scoped beneath the parent's
// authority. The returned API token is shown exactly once.

package v1

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"

	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// Aspect-name shape: lowercase ASCII + digits + hyphens, 1-32 chars,
// must start with a letter. Tight enough to keep generated usernames
// like "<parent>-<aspect>" predictable; loose enough to cover every
// aspect identity in the current network (keel, shadow, plumb, anvil,
// wren, maren, forge, harrow).
var aspectNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// MintAspectRequestJSON is the wire format for POST /users/me/aspects.
type MintAspectRequestJSON struct {
	// Name is the aspect's short identity, e.g. "keel". The minted
	// forgejo user is named "<parent>-<name>" (lowercase) so multiple
	// humans can mint aspects with overlapping short names without
	// username collisions.
	Name string `json:"name"`

	// Description is an optional free-form blurb stored on the
	// minted user. Bounded server-side to 255 chars (forgejo's
	// User.Description truncation matches).
	Description string `json:"description"`
}

// MintAspectResponseJSON is the wire format for a successful mint.
// The APIToken field is the one-time-visible cleartext token; it
// cannot be retrieved later, the caller MUST capture it now or
// re-mint a fresh aspect to obtain a new one.
type MintAspectResponseJSON struct {
	UserID         int64  `json:"user_id"`
	Username       string `json:"username"`
	FullName       string `json:"full_name"`
	ParentUserID   int64  `json:"parent_user_id"`
	ParentUsername string `json:"parent_username"`
	APIToken       string `json:"api_token"`
	TokenScopes    string `json:"token_scopes"`
}

// AspectProvisioner mints aspect users and access tokens. Indirection
// kept narrow so unit tests can inject a recorder without spinning up
// forgejo's full schema; production wiring (cmd/cairn/main.go-ish)
// supplies an impl backed by models/user.CreateUser + models/auth.
// NewAccessToken.
type AspectProvisioner interface {
	// MintAspect creates a Type=UserTypeBot user with the given
	// parent linkage and mints an access token. Returns the newly
	// created user + the cleartext API token. tokenScopes is the
	// canonical comma-separated scope string the token should
	// carry — implementations are expected to honour it verbatim,
	// not expand or restrict it.
	MintAspect(ctx context.Context, parent AspectParent, name, fullName, description, tokenScopes string) (MintedAspect, error)
}

// AspectParent identifies the human user under whom an aspect is
// being minted. Decoupled from forgejo's *user_model.User so the
// provisioner contract doesn't drag the whole user package into
// every test.
type AspectParent struct {
	ID       int64
	Username string
}

// MintedAspect is what the provisioner returns on success.
type MintedAspect struct {
	UserID   int64
	Username string
	APIToken string
}

// ErrAspectNameTaken signals a username collision during mint.
// Surfaces as 409 Conflict.
var ErrAspectNameTaken = errors.New("aspect: name already in use")

// ErrAspectNameInvalid signals a name that failed shape validation
// outside aspectNameRE — e.g. a reserved username caught by the
// downstream provisioner. Surfaces as 400 Bad Request.
var ErrAspectNameInvalid = errors.New("aspect: name invalid")

// defaultAspectTokenScopes is the scope string used when minting.
// Covers the surfaces an aspect needs to participate in the issue
// tracker workflow that Phase 2/3 will lean on: file + comment on
// issues, push commits, read repository content, and the misc
// read-only metadata the dispatch loop needs.
const defaultAspectTokenScopes = "write:issue,write:repository,read:user,read:misc"

// PostMintAspect handles POST /api/cairn/v1/users/me/aspects.
func (h *Handler) PostMintAspect(w http.ResponseWriter, r *http.Request) {
	caller := callerFromCtx(r.Context())
	if caller == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}
	if h.aspectProvisioner == nil {
		// No provisioner wired — production main forgot to inject one.
		// 503 (not 500) because the feature is genuinely unavailable,
		// not because the request was malformed or the server crashed.
		log.Print("cairn api v1: PostMintAspect: no provisioner wired")
		writeError(w, http.StatusServiceUnavailable, "aspect_provisioning_unavailable", "")
		return
	}

	if r.Body != nil {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	}
	var in MintAspectRequestJSON
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not decode JSON body")
		return
	}
	if !aspectNameRE.MatchString(in.Name) {
		writeError(w, http.StatusBadRequest, "invalid_name", "name must be 1-32 chars, lowercase letters/digits/hyphens, leading letter")
		return
	}
	if len(in.Description) > 255 {
		writeError(w, http.StatusBadRequest, "invalid_description", "description must be 255 chars or fewer")
		return
	}

	username := caller.Username + "-" + in.Name
	minted, err := h.aspectProvisioner.MintAspect(r.Context(), AspectParent{
		ID:       caller.UserID,
		Username: caller.Username,
	}, username, in.Name, in.Description, defaultAspectTokenScopes)

	switch {
	case err == nil:
		// fall through to success response
	case errors.Is(err, ErrAspectNameTaken):
		writeError(w, http.StatusConflict, "name_taken", "an aspect with this name already exists for this parent")
		return
	case errors.Is(err, ErrAspectNameInvalid):
		writeError(w, http.StatusBadRequest, "invalid_name", err.Error())
		return
	default:
		log.Printf("cairn api v1: PostMintAspect: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(MintAspectResponseJSON{
		UserID:         minted.UserID,
		Username:       minted.Username,
		FullName:       in.Name,
		ParentUserID:   caller.UserID,
		ParentUsername: caller.Username,
		APIToken:       minted.APIToken,
		TokenScopes:    defaultAspectTokenScopes,
	})
}

// (Caller import retained for godoc cross-reference clarity in this file.)
var _ = (*cairnidentity.Caller)(nil)
