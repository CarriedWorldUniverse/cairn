package identity

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"regexp"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// ErrUserNotFound is returned when a referenced username does not
// resolve to a user record.
var ErrUserNotFound = errors.New("cairn identity: user not found")

// ErrForbidden is returned when an authenticated caller attempts an
// action that requires being the agent's owner (approve, block).
var ErrForbidden = errors.New("cairn identity: forbidden")

// ErrInvalidInput is returned when a Register request fails grammar-
// level validation (slug or domain shape). Wrapped errors include the
// specific rule that was violated; the message is safe to surface to
// clients (it states the rule, not user-supplied data).
var ErrInvalidInput = errors.New("cairn identity: invalid input")

const (
	maxSlugLen   = 64
	maxDomainLen = 255
)

// slugPattern: lowercase alphanumeric + hyphen, must start with
// alphanumeric. Matches the agentEmailPattern grammar from Plan 1.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// validateRegisterRequest enforces slug grammar and domain length.
// Returns nil on success, or an error wrapping ErrInvalidInput.
func validateRegisterRequest(req RegisterRequest) error {
	if req.Slug == "" || len(req.Slug) > maxSlugLen || !slugPattern.MatchString(req.Slug) {
		return fmt.Errorf("%w: slug must match [a-z0-9][a-z0-9-]* and be 1-%d chars", ErrInvalidInput, maxSlugLen)
	}
	if req.Domain == "" || len(req.Domain) > maxDomainLen {
		return fmt.Errorf("%w: domain must be 1-%d chars", ErrInvalidInput, maxDomainLen)
	}
	return nil
}

// UserResolver looks up Forgejo user records by username or id.
// The API layer implements this against models/user; tests provide
// a fake.
//
// Implementations MUST return ErrUserNotFound (the sentinel defined
// in this package) when the requested user does not exist.
// Other errors are surfaced as-is.
type UserResolver interface {
	UserIDByUsername(ctx context.Context, name string) (int64, error)
	UsernameByID(ctx context.Context, id int64) (string, error)
}

// Caller represents the authenticated user making a request, or nil
// for anonymous requests.
type Caller struct {
	UserID   int64
	Username string
}

// RegisterRequest is the input to AgentService.Register.
type RegisterRequest struct {
	ProposedOwner string            // username
	Slug          string            // bare slug, e.g. "plumb"
	Domain        string            // e.g. "darksoft.co.nz"
	PublicKey     ed25519.PublicKey // 32 bytes
}

// AgentService orchestrates the registration / approval / blocking
// flow on top of AgentStore + AgentBlocklistStore + UserResolver.
//
// The service owns the instance HMAC key (used to compute fingerprints)
// and the auto-approve gate (caller's user_id == proposed_owner.user_id).
type AgentService struct {
	hmacKey   []byte
	store     AgentStore
	blocklist AgentBlocklistStore
	users     UserResolver
}

// NewAgentService constructs an AgentService.
func NewAgentService(hmacKey []byte, store AgentStore, blocklist AgentBlocklistStore, users UserResolver) *AgentService {
	return &AgentService{
		hmacKey:   hmacKey,
		store:     store,
		blocklist: blocklist,
		users:     users,
	}
}

// Register is the unified registration flow. If caller is the proposed
// owner (caller.UserID == proposed owner's id), the agent is created
// active. Otherwise (different user, or anonymous) the agent is pending.
//
// Returns ErrUserNotFound if proposed_owner doesn't exist; ErrAgentExists
// for duplicate (user_id, slug) or duplicate fingerprint.
func (s *AgentService) Register(ctx context.Context, req RegisterRequest, caller *Caller) (*cairn.Agent, error) {
	if err := validateRegisterRequest(req); err != nil {
		return nil, err
	}
	ownerID, err := s.users.UserIDByUsername(ctx, req.ProposedOwner)
	if err != nil {
		return nil, err
	}

	autoApprove := caller != nil && caller.UserID > 0 && caller.UserID == ownerID

	now := time.Now()
	agent := &cairn.Agent{
		Fingerprint: Fingerprint(s.hmacKey, req.PublicKey),
		UserID:      ownerID,
		Slug:        req.Slug,
		Domain:      req.Domain,
		PublicKey:   []byte(req.PublicKey),
		CreatedAt:   now,
	}
	if autoApprove {
		agent.Status = cairn.AgentStatusActive
		agent.ActivatedAt = &now
	} else {
		agent.Status = cairn.AgentStatusPending
	}

	if err := s.store.Register(ctx, agent); err != nil {
		return nil, err
	}
	return agent, nil
}

// Approve transitions an agent from pending to active. Caller must be
// the agent's owner.
func (s *AgentService) Approve(ctx context.Context, fingerprint string, caller *Caller) error {
	if caller == nil || caller.UserID <= 0 {
		return ErrForbidden
	}
	a, err := s.store.GetByFingerprint(ctx, fingerprint)
	if err != nil {
		return err
	}
	if a.UserID != caller.UserID {
		return ErrForbidden
	}
	return s.store.Approve(ctx, fingerprint)
}

// Block adds the agent to the blocklist. Caller must be the agent's
// owner.
func (s *AgentService) Block(ctx context.Context, fingerprint, reason string, caller *Caller) error {
	if caller == nil || caller.UserID <= 0 {
		return ErrForbidden
	}
	a, err := s.store.GetByFingerprint(ctx, fingerprint)
	if err != nil {
		return err
	}
	if a.UserID != caller.UserID {
		return ErrForbidden
	}
	return s.blocklist.Block(ctx, a.ID, reason)
}

// GetByFingerprint is a thin wrapper around the store; included on
// the service so handlers don't have to reach across two layers.
func (s *AgentService) GetByFingerprint(ctx context.Context, fingerprint string) (*cairn.Agent, error) {
	return s.store.GetByFingerprint(ctx, fingerprint)
}

// GetByEmail looks up an agent by (slug, domain) — the components of
// its nexus-{slug}@{domain} email. Returns ErrAgentNotFound if no
// matching record exists. Convenience wrapper exposed on the service
// so callers (notably the push-verification hook) don't have to reach
// into the store directly.
func (s *AgentService) GetByEmail(ctx context.Context, slug, domain string) (*cairn.Agent, error) {
	return s.store.GetByEmail(ctx, slug, domain)
}

// IsBlocked reports whether the agent identified by fingerprint is
// in the blocklist.
func (s *AgentService) IsBlocked(ctx context.Context, fingerprint string) (bool, error) {
	a, err := s.store.GetByFingerprint(ctx, fingerprint)
	if err != nil {
		return false, err
	}
	return s.blocklist.IsBlocked(ctx, a.ID)
}

// ListByUser returns agents owned by the given user. Empty status
// means all statuses.
func (s *AgentService) ListByUser(ctx context.Context, userID int64, status cairn.AgentStatus) ([]*cairn.Agent, error) {
	return s.store.ListByUser(ctx, userID, status)
}

// UsernameByID returns the username for a user id, via the configured
// UserResolver. Convenience wrapper used by the API layer to render
// owner names on list/detail pages without holding its own resolver
// reference.
func (s *AgentService) UsernameByID(ctx context.Context, userID int64) (string, error) {
	return s.users.UsernameByID(ctx, userID)
}
