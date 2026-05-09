package identity

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// ErrUserNotFound is returned when a referenced username does not
// resolve to a user record.
var ErrUserNotFound = errors.New("cairn identity: user not found")

// ErrForbidden is returned when an authenticated caller attempts an
// action that requires being the agent's owner (approve, block).
var ErrForbidden = errors.New("cairn identity: forbidden")

// UserResolver looks up Forgejo user records by username or id.
// The API layer implements this against models/user; tests provide
// a fake.
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
	ownerID, err := s.users.UserIDByUsername(ctx, req.ProposedOwner)
	if err != nil {
		return nil, err
	}

	autoApprove := caller != nil && caller.UserID == ownerID

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
	if caller == nil {
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
	if caller == nil {
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

// UserResolverUsername returns the username for a user id, or empty
// string on lookup failure (caller decides whether that's an error).
// Convenience wrapper used by the API layer.
func (s *AgentService) UserResolverUsername(ctx context.Context, userID int64) (string, error) {
	return s.users.UsernameByID(ctx, userID)
}
