package identity

import (
	"context"
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
// for anonymous requests. IsAdmin mirrors the Forgejo site-admin flag.
type Caller struct {
	UserID   int64
	Username string
	IsAdmin  bool
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
// the first registered is returned. Used by API readback paths where a
// representative pubkey is sufficient.
//
// Prefer PubkeyContentsForAgent when verifying signatures so multi-host
// agents work correctly: a commit signed on host B must still verify
// even though host A's pubkey was registered first.
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

// PubkeyContentsForAgent returns the OpenSSH-format pubkey content for
// every pubkey bound to agentID (one per registered host). Used by the
// push hook to verify a commit signature against any of an agent's
// bound keys — multi-host agents must accept signatures from any host.
//
// Returns an empty slice and ErrAgentNotFound if no bindings exist.
// If any underlying GetPubkeyContent lookup fails, the error is returned
// eagerly — that indicates a broken FK from cairn_agent_pubkey to
// public_key (corruption), not a per-host failure to be papered over.
func (s *AgentService) PubkeyContentsForAgent(ctx context.Context, agentID int64) ([]string, error) {
	rows, err := s.pubkeys.ListByAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrAgentNotFound
	}
	contents := make([]string, 0, len(rows))
	for _, ap := range rows {
		c, err := s.registrar.GetPubkeyContent(ctx, ap.PublicKeyID)
		if err != nil {
			return nil, err
		}
		contents = append(contents, c)
	}
	return contents, nil
}

// UsernameByID returns the username for a user id, via the configured
// UserResolver. Convenience wrapper used by the API layer to render
// owner names on list/detail pages.
func (s *AgentService) UsernameByID(ctx context.Context, userID int64) (string, error) {
	return s.users.UsernameByID(ctx, userID)
}
