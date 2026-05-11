package cairn

import (
	"time"

	"github.com/CarriedWorldUniverse/cairn/models/db"
)

func init() {
	db.RegisterModel(new(Agent))
}

// AgentStatus is the lifecycle state of an agent record.
type AgentStatus string

const (
	// AgentStatusPending — proposed, awaiting owner approval.
	AgentStatusPending AgentStatus = "pending"
	// AgentStatusActive — approved, may sign commits and act under the owner.
	AgentStatusActive AgentStatus = "active"
)

// Agent is the registered identity of a Cairn agent under a human owner.
//
// As of V503 (Plan 8), agent pubkeys live in Forgejo's public_key table
// joined via cairn_agent_pubkey. The embedded Fingerprint/PublicKey
// columns and Go fields are gone. To look up an agent by fingerprint,
// query cairn_agent_pubkey first then load the agent by AgentID.
// Slug uniqueness is scoped per UserID. Email convention is
// "nexus-{Slug}@{Domain}". See docs/cairn/specs/2026-05-11-cairn-
// instance-rooted-identity.md.
type Agent struct {
	ID          int64       `xorm:"pk autoincr"`
	UserID      int64       `xorm:"NOT NULL INDEX UNIQUE(user_slug)"`
	Slug        string      `xorm:"VARCHAR(64) NOT NULL INDEX(email_lookup) UNIQUE(user_slug)"`
	Domain      string      `xorm:"VARCHAR(255) NOT NULL INDEX(email_lookup)"`
	Status      AgentStatus `xorm:"VARCHAR(16) NOT NULL DEFAULT 'pending'"`
	CreatedAt   time.Time   `xorm:"NOT NULL"`
	ActivatedAt *time.Time
}

// TableName returns the SQL table name.
func (a Agent) TableName() string {
	return "cairn_agent"
}

// IsActive reports whether the agent may currently act.
func (a Agent) IsActive() bool {
	return a.Status == AgentStatusActive
}
