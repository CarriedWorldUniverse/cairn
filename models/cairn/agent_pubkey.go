//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

// AgentPubkey binds a public_key (Forgejo's table) to a cairn_agent.
// This is the join table that lets us look up an agent by the fingerprint
// of any of its registered pubkeys. Per-host revocation = delete one
// AgentPubkey row + the corresponding Forgejo public_key.
//
// See docs/cairn/specs/2026-05-11-cairn-instance-rooted-identity.md.
type AgentPubkey struct {
	ID          int64  `xorm:"pk autoincr"`
	AgentID     int64  `xorm:"INDEX NOT NULL"`
	PublicKeyID int64  `xorm:"INDEX NOT NULL UNIQUE"`        // FK to Forgejo's public_key.id
	Fingerprint string `xorm:"VARCHAR(255) UNIQUE NOT NULL"` // cached for O(1) lookup
	CreatedUnix int64  `xorm:"created"`
}

// TableName returns the SQL table name.
func (AgentPubkey) TableName() string { return "cairn_agent_pubkey" }
