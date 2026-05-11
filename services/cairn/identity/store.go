package identity

import (
	"context"
	"errors"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// ErrAgentNotFound is returned when a lookup finds no matching agent row.
var ErrAgentNotFound = errors.New("cairn identity: agent not found")

// ErrAgentExists is returned when registration would violate the
// (user_id, slug) uniqueness.
var ErrAgentExists = errors.New("cairn identity: agent already exists for (user, slug)")

// ErrPubkeyAlreadyClaimed is returned when an attachment-request pubkey
// (by fingerprint) is already bound to another cairn_agent_pubkey row.
// Same pubkey on multiple agents would defeat the trailer-lookup
// uniqueness assumption; we reject the second claimant explicitly.
var ErrPubkeyAlreadyClaimed = errors.New("cairn identity: public key already claimed by another agent")

// ErrAttachmentRequestNotFound is returned when an Approve/Reject targets
// a nonexistent request id.
var ErrAttachmentRequestNotFound = errors.New("cairn identity: attachment request not found")

// ErrAlreadyDecided is returned when Approve/Reject is called on a
// request that already has a terminal status (approved or rejected).
var ErrAlreadyDecided = errors.New("cairn identity: attachment request already decided")

// AgentStore is the backend-agnostic data access for agents.
//
// Every method opens a short-lived session, executes, and releases.
// Implementations MUST NOT hold sessions across method boundaries.
type AgentStore interface {
	// FindOrCreateByUserSlug returns the existing agent for (userID,
	// slug, domain) or creates a fresh row with Status=pending if none
	// exists. The boolean reports whether the row was created.
	FindOrCreateByUserSlug(ctx context.Context, userID int64, slug, domain string) (*cairn.Agent, bool, error)
	GetByID(ctx context.Context, id int64) (*cairn.Agent, error)
	GetByEmail(ctx context.Context, slug, domain string) (*cairn.Agent, error)
	ListByUser(ctx context.Context, userID int64, status cairn.AgentStatus) ([]*cairn.Agent, error)
	// SetStatus updates the agent's lifecycle status. Setting to
	// AgentStatusActive also stamps ActivatedAt to now.
	SetStatus(ctx context.Context, agentID int64, status cairn.AgentStatus) error
}

// AgentPubkeyStore is the backend-agnostic data access for the
// cairn_agent_pubkey join rows binding agents to Forgejo public keys.
type AgentPubkeyStore interface {
	Insert(ctx context.Context, row *cairn.AgentPubkey) error
	GetByFingerprint(ctx context.Context, fingerprint string) (*cairn.AgentPubkey, error)
	ListByAgent(ctx context.Context, agentID int64) ([]*cairn.AgentPubkey, error)
}

// AttachmentRequestStore is the backend-agnostic data access for
// pending/historical attachment requests.
type AttachmentRequestStore interface {
	Insert(ctx context.Context, req *cairn.AttachmentRequest) error
	GetByID(ctx context.Context, id int64) (*cairn.AttachmentRequest, error)
	ListPendingByOwner(ctx context.Context, ownerUsername string) ([]*cairn.AttachmentRequest, error)
	// ListByOwner returns attachment requests for the named owner.
	// Empty status returns all statuses; otherwise filters to the
	// supplied status.
	ListByOwner(ctx context.Context, ownerUsername string, status cairn.AttachmentRequestStatus) ([]*cairn.AttachmentRequest, error)
	// ListAll returns every attachment request, optionally filtered by
	// status. Empty status returns all statuses. Used for the admin
	// listing endpoint.
	ListAll(ctx context.Context, status cairn.AttachmentRequestStatus) ([]*cairn.AttachmentRequest, error)
	UpdateDecision(ctx context.Context, id int64, status cairn.AttachmentRequestStatus, decidedByUserID int64) error
}

// AgentBlocklistStore is the backend-agnostic data access for the
// agent blocklist. Same connection-per-operation discipline.
//
// Block is idempotent — repeat calls for the same agent are no-ops
// rather than producing duplicate rows.
//
// Unblock is intentionally out of scope for MVP. To rescind a block,
// either delete the row directly via admin DB access, or rotate the
// agent identity. A first-class Unblock method may be added post-MVP
// if the team workflow needs it.
type AgentBlocklistStore interface {
	Block(ctx context.Context, agentID int64, reason string) error
	IsBlocked(ctx context.Context, agentID int64) (bool, error)
	List(ctx context.Context) ([]*cairn.AgentBlocklist, error)
}

// AgentUserRegistrar abstracts the Forgejo-side bits of agent
// registration: provisioning a user account for the agent and storing
// its SSH public key under that account.
//
// The service uses this so its test suite can fake out the Forgejo
// dependencies (which are not available in the cairntest in-memory
// engine) while production wires the real Forgejo user + asymkey
// models.
type AgentUserRegistrar interface {
	// FindOrCreateAgentUser ensures a Forgejo user exists for the agent
	// with login "nexus-{slug}" and email "nexus-{slug}@{domain}". On
	// first call it creates the user; subsequent calls return the same
	// id. Returns the user's id.
	FindOrCreateAgentUser(ctx context.Context, slug, domain string) (int64, error)
	// RegisterPubkey inserts pubkeyContent (OpenSSH-format text) into
	// Forgejo's public_key table under userID with the given key name.
	// Returns the public_key.id. If a row with the same content already
	// exists for that user, the existing id is returned.
	RegisterPubkey(ctx context.Context, userID int64, pubkeyContent, name string) (int64, error)
	// GetPubkeyContent returns the OpenSSH-format content of the
	// public_key row identified by publicKeyID.
	GetPubkeyContent(ctx context.Context, publicKeyID int64) (string, error)
}
