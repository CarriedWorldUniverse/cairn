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
// Fingerprint is HMAC-SHA256(instance_hmac_key, public_key), formatted as
// "cairn:" + base64. Slug uniqueness is scoped per UserID. Email convention
// is "nexus-{Slug}@{Domain}". See docs/cairn/specs/2026-05-09-cairn-
// foundation-design.md §5 and §6.
type Agent struct {
	ID          int64       `xorm:"pk autoincr"`
	Fingerprint string      `xorm:"VARCHAR(80) NOT NULL UNIQUE"`
	UserID      int64       `xorm:"NOT NULL INDEX UNIQUE(user_slug)"`
	Slug        string      `xorm:"VARCHAR(64) NOT NULL INDEX(email_lookup) UNIQUE(user_slug)"`
	Domain      string      `xorm:"VARCHAR(255) NOT NULL INDEX(email_lookup)"`
	PublicKey   []byte      `xorm:"BLOB NOT NULL"`
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
