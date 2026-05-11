package identity

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"regexp"
	"time"

	"golang.org/x/crypto/ssh"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// ErrUserNotFound is returned when a referenced username does not
// resolve to a user record.
var ErrUserNotFound = errors.New("cairn identity: user not found")

// ErrForbidden is returned when an authenticated caller attempts an
// action that requires being the agent's owner (approve, block).
var ErrForbidden = errors.New("cairn identity: forbidden")

// ErrInvalidInput is returned when a request fails grammar-level
// validation (slug or domain shape). Wrapped errors include the
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

// validateSlugDomain enforces slug grammar and domain length. Returns
// nil on success, or an error wrapping ErrInvalidInput.
func validateSlugDomain(slug, domain string) error {
	if slug == "" || len(slug) > maxSlugLen || !slugPattern.MatchString(slug) {
		return fmt.Errorf("%w: slug must match [a-z0-9][a-z0-9-]* and be 1-%d chars", ErrInvalidInput, maxSlugLen)
	}
	if domain == "" || len(domain) > maxDomainLen {
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

// RegisterRequest is the input to AgentService.Register. PublicKey is
// the agent's ed25519 public key (32 bytes); the service marshals it
// to OpenSSH-format text before handing to the registrar.
type RegisterRequest struct {
	ProposedOwner string            // username
	Slug          string            // bare slug, e.g. "plumb"
	Domain        string            // e.g. "darksoft.co.nz"
	PublicKey     ed25519.PublicKey // 32 bytes
}

// AgentService orchestrates the registration / approval / blocking
// flow on top of AgentStore + AgentPubkeyStore + AttachmentRequestStore
// + AgentBlocklistStore + UserResolver + AgentUserRegistrar.
//
// The service owns the instance HMAC key (used to compute fingerprints)
// and the auto-approve gate (caller's user_id == proposed_owner.user_id).
type AgentService struct {
	hmacKey   []byte
	store     AgentStore
	pubkeys   AgentPubkeyStore
	requests  AttachmentRequestStore
	blocklist AgentBlocklistStore
	users     UserResolver
	registrar AgentUserRegistrar
}

// NewAgentService constructs an AgentService.
func NewAgentService(
	hmacKey []byte,
	store AgentStore,
	pubkeys AgentPubkeyStore,
	requests AttachmentRequestStore,
	blocklist AgentBlocklistStore,
	users UserResolver,
	registrar AgentUserRegistrar,
) *AgentService {
	return &AgentService{
		hmacKey:   hmacKey,
		store:     store,
		pubkeys:   pubkeys,
		requests:  requests,
		blocklist: blocklist,
		users:     users,
		registrar: registrar,
	}
}

// marshalEd25519 turns a raw ed25519 public key into OpenSSH-format
// authorized_keys text (e.g. "ssh-ed25519 AAAAC3...").
func marshalEd25519(pub ed25519.PublicKey) (string, error) {
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	return string(ssh.MarshalAuthorizedKey(sshPub)), nil
}

// Fingerprint returns the Cairn fingerprint for the given raw ed25519
// public key under the service's instance HMAC key. Exposed so handlers
// that need to surface a fingerprint to the wire format (without doing
// a join lookup) can recompute it locally.
func (s *AgentService) FingerprintEd25519(pub ed25519.PublicKey) string {
	return Fingerprint(s.hmacKey, pub)
}

// Register is the unified registration flow. If caller is the proposed
// owner (caller.UserID == proposed owner's id), the agent is created
// active. Otherwise (different user, or anonymous) the agent is pending.
//
// Returns ErrUserNotFound if proposed_owner doesn't exist; ErrAgentExists
// for duplicate (user_id, slug); ErrPubkeyAlreadyClaimed if the pubkey
// fingerprint is already bound to another agent.
func (s *AgentService) Register(ctx context.Context, req RegisterRequest, caller *Caller) (*cairn.Agent, error) {
	if err := validateSlugDomain(req.Slug, req.Domain); err != nil {
		return nil, err
	}
	ownerID, err := s.users.UserIDByUsername(ctx, req.ProposedOwner)
	if err != nil {
		return nil, err
	}

	autoApprove := caller != nil && caller.UserID > 0 && caller.UserID == ownerID

	pubContent, err := marshalEd25519(req.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal pubkey: %v", ErrInvalidInput, err)
	}
	fp := Fingerprint(s.hmacKey, req.PublicKey)

	return s.registerCore(ctx, ownerID, req.Slug, req.Domain, pubContent, fp, autoApprove)
}

// registerCore performs the full agent-registration sequence: ensure
// the Forgejo agent user exists, register the pubkey on that user,
// find-or-create the cairn_agent row, bind via cairn_agent_pubkey,
// and (if autoApprove) flip status to active.
//
// Used by Register and by ApproveAttachmentRequest.
func (s *AgentService) registerCore(ctx context.Context, ownerID int64, slug, domain, pubkeyContent, fingerprint string, autoApprove bool) (*cairn.Agent, error) {
	if s.registrar == nil {
		return nil, errors.New("cairn identity: no AgentUserRegistrar configured")
	}

	// Provision (or find) the agent's Forgejo user account.
	agentUserID, err := s.registrar.FindOrCreateAgentUser(ctx, slug, domain)
	if err != nil {
		return nil, err
	}

	// Register the pubkey on the agent user. Name uses the fingerprint
	// so each registered pubkey has a stable, unique label.
	pubKeyID, err := s.registrar.RegisterPubkey(ctx, agentUserID, pubkeyContent, "cairn:"+fingerprint)
	if err != nil {
		return nil, err
	}

	// Find or create the cairn_agent row (owned by ownerID, scoped by
	// slug). Subsequent pubkey registrations for the same slug under
	// the same owner attach to the existing agent (multi-host case).
	agent, _, err := s.store.FindOrCreateByUserSlug(ctx, ownerID, slug, domain)
	if err != nil {
		return nil, err
	}

	// Bind the pubkey to the agent.
	if err := s.pubkeys.Insert(ctx, &cairn.AgentPubkey{
		AgentID:     agent.ID,
		PublicKeyID: pubKeyID,
		Fingerprint: fingerprint,
	}); err != nil {
		return nil, err
	}

	if autoApprove && agent.Status != cairn.AgentStatusActive {
		if err := s.store.SetStatus(ctx, agent.ID, cairn.AgentStatusActive); err != nil {
			return nil, err
		}
		now := time.Now()
		agent.Status = cairn.AgentStatusActive
		agent.ActivatedAt = &now
	}
	return agent, nil
}

// Approve transitions an agent from pending to active. Caller must be
// the agent's owner. The agent is identified by the Cairn fingerprint
// of any of its registered pubkeys.
func (s *AgentService) Approve(ctx context.Context, fingerprint string, caller *Caller) error {
	if caller == nil || caller.UserID <= 0 {
		return ErrForbidden
	}
	a, err := s.LookupAgentByFingerprint(ctx, fingerprint)
	if err != nil {
		return err
	}
	if a.UserID != caller.UserID {
		return ErrForbidden
	}
	return s.store.SetStatus(ctx, a.ID, cairn.AgentStatusActive)
}

// Block adds the agent to the blocklist. Caller must be the agent's
// owner.
func (s *AgentService) Block(ctx context.Context, fingerprint, reason string, caller *Caller) error {
	if caller == nil || caller.UserID <= 0 {
		return ErrForbidden
	}
	a, err := s.LookupAgentByFingerprint(ctx, fingerprint)
	if err != nil {
		return err
	}
	if a.UserID != caller.UserID {
		return ErrForbidden
	}
	return s.blocklist.Block(ctx, a.ID, reason)
}

// LookupAgentByFingerprint resolves a Cairn fingerprint to the owning
// agent by joining through cairn_agent_pubkey. Returns ErrAgentNotFound
// when no pubkey matches.
//
// Implementation runs two queries instead of a SQL join. The two-step
// path keeps the AgentPubkeyStore interface backend-agnostic (no need
// to expose join helpers) and is fine at our scale.
func (s *AgentService) LookupAgentByFingerprint(ctx context.Context, fingerprint string) (*cairn.Agent, error) {
	ap, err := s.pubkeys.GetByFingerprint(ctx, fingerprint)
	if err != nil {
		return nil, err
	}
	return s.store.GetByID(ctx, ap.AgentID)
}

// GetByFingerprint is a thin wrapper around LookupAgentByFingerprint
// preserved as a stable name for API handlers and the push hook.
func (s *AgentService) GetByFingerprint(ctx context.Context, fingerprint string) (*cairn.Agent, error) {
	return s.LookupAgentByFingerprint(ctx, fingerprint)
}

// GetByEmail looks up an agent by (slug, domain) — the components of
// its nexus-{slug}@{domain} email. Returns ErrAgentNotFound if no
// matching record exists. Used by the push-verification hook.
func (s *AgentService) GetByEmail(ctx context.Context, slug, domain string) (*cairn.Agent, error) {
	return s.store.GetByEmail(ctx, slug, domain)
}

// IsBlocked reports whether the agent identified by fingerprint is
// in the blocklist.
func (s *AgentService) IsBlocked(ctx context.Context, fingerprint string) (bool, error) {
	a, err := s.LookupAgentByFingerprint(ctx, fingerprint)
	if err != nil {
		return false, err
	}
	return s.blocklist.IsBlocked(ctx, a.ID)
}

// IsAgentBlocked is a fingerprint-free variant for callers that already
// have an *Agent.
func (s *AgentService) IsAgentBlocked(ctx context.Context, agentID int64) (bool, error) {
	return s.blocklist.IsBlocked(ctx, agentID)
}

// ListByUser returns agents owned by the given user. Empty status
// means all statuses.
func (s *AgentService) ListByUser(ctx context.Context, userID int64, status cairn.AgentStatus) ([]*cairn.Agent, error) {
	return s.store.ListByUser(ctx, userID, status)
}

// ListAgentPubkeys returns the cairn_agent_pubkey bindings for the
// given agent. Surfaced for handlers that need to render an agent's
// fingerprints + per-host pubkey ids.
func (s *AgentService) ListAgentPubkeys(ctx context.Context, agentID int64) ([]*cairn.AgentPubkey, error) {
	return s.pubkeys.ListByAgent(ctx, agentID)
}

// PubkeyContentForFingerprint returns the OpenSSH-format pubkey content
// bound to the given Cairn fingerprint. Used by the push hook to verify
// commit signatures without storing the key bytes on the agent row.
func (s *AgentService) PubkeyContentForFingerprint(ctx context.Context, fingerprint string) (string, error) {
	ap, err := s.pubkeys.GetByFingerprint(ctx, fingerprint)
	if err != nil {
		return "", err
	}
	return s.registrar.GetPubkeyContent(ctx, ap.PublicKeyID)
}

// PubkeyContentForAgent returns the OpenSSH-format pubkey content for
// any pubkey bound to agentID. If multiple are bound (multi-host case),
// the first registered is returned. Used by the push hook when the
// commit author identifies the agent by email rather than fingerprint.
func (s *AgentService) PubkeyContentForAgent(ctx context.Context, agentID int64) (string, error) {
	rows, err := s.pubkeys.ListByAgent(ctx, agentID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", ErrAgentNotFound
	}
	return s.registrar.GetPubkeyContent(ctx, rows[0].PublicKeyID)
}

// UsernameByID returns the username for a user id, via the configured
// UserResolver. Convenience wrapper used by the API layer to render
// owner names on list/detail pages.
func (s *AgentService) UsernameByID(ctx context.Context, userID int64) (string, error) {
	return s.users.UsernameByID(ctx, userID)
}
