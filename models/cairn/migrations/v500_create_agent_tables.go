// Cairn migrations begin at v500 to leave room above Forgejo's existing
// series. They register with Forgejo's xorm engine alongside upstream's;
// one migration table, one ordering.

package migrations

import (
	"time"

	"xorm.io/xorm"
)

// V500CreateAgentTables creates cairn_agent and cairn_agent_blocklist.
func V500CreateAgentTables(x *xorm.Engine) error {
	type Agent struct {
		ID          int64     `xorm:"pk autoincr"`
		Fingerprint string    `xorm:"VARCHAR(80) NOT NULL UNIQUE"`
		UserID      int64     `xorm:"NOT NULL INDEX"`
		Slug        string    `xorm:"VARCHAR(64) NOT NULL"`
		Domain      string    `xorm:"VARCHAR(255) NOT NULL"`
		PublicKey   []byte    `xorm:"BLOB NOT NULL"`
		Status      string    `xorm:"VARCHAR(16) NOT NULL DEFAULT 'pending'"`
		CreatedAt   time.Time `xorm:"NOT NULL"`
		ActivatedAt *time.Time
	}

	type AgentBlocklist struct {
		ID        int64     `xorm:"pk autoincr"`
		AgentID   int64     `xorm:"NOT NULL INDEX"`
		BlockedAt time.Time `xorm:"NOT NULL"`
		Reason    string    `xorm:"TEXT"`
	}

	if err := x.Sync2(new(Agent), new(AgentBlocklist)); err != nil {
		return err
	}

	// Composite index for email lookup at push time. Naming aligns with
	// xorm's GonicMapper output (IDX_<table>_<index_name>) so a fresh
	// install via SyncAllTables and an upgrade via this migration end up
	// with the same index name.
	if _, err := x.Exec(
		`CREATE INDEX IF NOT EXISTS IDX_cairn_agent_email_lookup ON cairn_agent (slug, domain)`,
	); err != nil {
		return err
	}
	// (user_id, slug) uniqueness — scopes slug per owner.
	if _, err := x.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS UQE_cairn_agent_user_slug ON cairn_agent (user_id, slug)`,
	); err != nil {
		return err
	}
	return nil
}
