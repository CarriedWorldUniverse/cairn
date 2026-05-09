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

// AgentStore is the backend-agnostic data access for agents.
//
// Every method opens a short-lived session, executes, and releases.
// Implementations MUST NOT hold sessions across method boundaries.
type AgentStore interface {
	Register(ctx context.Context, a *cairn.Agent) error
	GetByFingerprint(ctx context.Context, fingerprint string) (*cairn.Agent, error)
	GetByEmail(ctx context.Context, slug, domain string) (*cairn.Agent, error)
	ListByUser(ctx context.Context, userID int64, status cairn.AgentStatus) ([]*cairn.Agent, error)
	Approve(ctx context.Context, fingerprint string) error
}

// AgentBlocklistStore is the backend-agnostic data access for the
// agent blocklist. Same connection-per-operation discipline.
type AgentBlocklistStore interface {
	Block(ctx context.Context, agentID int64, reason string) error
	IsBlocked(ctx context.Context, agentID int64) (bool, error)
	List(ctx context.Context) ([]*cairn.AgentBlocklist, error)
}
