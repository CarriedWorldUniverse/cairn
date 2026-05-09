package cairn

import (
	"time"

	"github.com/CarriedWorldUniverse/cairn/models/db"
)

func init() {
	db.RegisterModel(new(AgentBlocklist))
}

// AgentBlocklist records that an agent has been blocked from acting.
// A non-empty row for AgentID means the agent's pushes are rejected
// even if its row in cairn_agent is status="active".
type AgentBlocklist struct {
	ID        int64     `xorm:"pk autoincr"`
	AgentID   int64     `xorm:"NOT NULL INDEX"`
	BlockedAt time.Time `xorm:"NOT NULL"`
	Reason    string    `xorm:"TEXT"`
}

// TableName returns the SQL table name.
func (b AgentBlocklist) TableName() string {
	return "cairn_agent_blocklist"
}
