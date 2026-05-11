// Cairn-specific migration registration. Refactors identity: drops the
// embedded seed-derived pubkey/fingerprint columns from cairn_agent and
// adds cairn_attachment_request + cairn_agent_pubkey tables. Mirror of
// v500a/v501a/v502a's pattern (Plan 7 lesson: shipped migrations need the
// registration shim or they don't run in production).
//
// Cairn-specific code; AGPLv3. See LICENSING.md.

package forgejo_migrations

import (
	cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"
)

func init() {
	registerMigration(&Migration{
		Description: "Cairn: refactor identity — drop seed-derived embedded keys, add attachment_request + agent_pubkey",
		Upgrade:     cairnmigrations.V503RefactorIdentity,
	})
}
