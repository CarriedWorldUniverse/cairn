//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package migrations

import (
	"xorm.io/xorm"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// V503RefactorIdentity drops cairn_agent.public_key + .fingerprint (the
// embedded seed-derived pubkey model is replaced by Forgejo's public_key
// table + a Cairn-side join), and adds the new cairn_attachment_request
// + cairn_agent_pubkey tables.
//
// Safe at MVP: no rows in cairn_agent exist on the live deployment as of
// the migration timestamp (verified during Plan 7 deploy). For any future
// instance that already has seed-derived agents, the data-migration logic
// would need to read the embedded pubkey, insert into public_key +
// agent_pubkey, then drop the columns. Out of scope for now.
//
// SQLite 3.35+ supports ALTER TABLE DROP COLUMN directly; Forgejo's
// bundled SQLite is 3.40+, so we use it without a copy-rebuild dance.
func V503RefactorIdentity(x *xorm.Engine) error {
	// Create the new tables first.
	if err := x.Table("cairn_attachment_request").Sync2(new(cairnmodels.AttachmentRequest)); err != nil {
		return err
	}
	if err := x.Table("cairn_agent_pubkey").Sync2(new(cairnmodels.AgentPubkey)); err != nil {
		return err
	}
	// SQLite refuses to drop columns referenced by indexes; drop the
	// fingerprint unique index first. Index names follow xorm's
	// GonicMapper output (UQE_<table>_<col>) on both fresh installs and
	// upgrades.
	if _, err := x.Exec("DROP INDEX IF EXISTS UQE_cairn_agent_fingerprint"); err != nil {
		return err
	}
	// Drop the now-obsolete columns from cairn_agent.
	if _, err := x.Exec("ALTER TABLE cairn_agent DROP COLUMN public_key"); err != nil {
		return err
	}
	if _, err := x.Exec("ALTER TABLE cairn_agent DROP COLUMN fingerprint"); err != nil {
		return err
	}
	return nil
}
