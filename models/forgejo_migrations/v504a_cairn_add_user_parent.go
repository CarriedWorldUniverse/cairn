// Cairn-specific migration registration. Adds parent_user_id to the
// user table so bot users can link back to their human owner (AI-first
// identity layer, Phase 1). Mirrors the v500a..v503a shims.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.

package forgejo_migrations //nolint:revive

import (
	cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"
)

func init() {
	registerMigration(&Migration{
		Description: "Cairn: add user.parent_user_id for AI-first aspect-owner linkage",
		Upgrade:     cairnmigrations.V504AddUserParent,
	})
}
