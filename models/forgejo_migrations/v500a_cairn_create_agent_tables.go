// Cairn-specific migration registration. The actual migration logic lives in
// models/cairn/migrations so the Cairn additive layer stays out of Forgejo's
// own migration packages; this file is the thin shim that hooks Cairn into
// Forgejo's existing migrator. Cairn migrations begin at v500 to leave room
// above Forgejo's existing v1xa-series.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.

package forgejo_migrations

import (
	// Side-effect import: registers Cairn models with db.SyncAllTables so
	// fresh installs create the tables and live upgrades have the structs
	// available alongside the migration below.
	_ "github.com/CarriedWorldUniverse/cairn/models/cairn"
	cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"
)

func init() {
	registerMigration(&Migration{
		Description: "Cairn: create agent tables",
		Upgrade:     cairnmigrations.V500CreateAgentTables,
	})
}
