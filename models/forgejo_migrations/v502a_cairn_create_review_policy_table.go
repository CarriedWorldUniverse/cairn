// Cairn-specific migration registration. Adds the review-policy table
// (cairn_review_policy). Mirror of v500a's pattern.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.

package forgejo_migrations

import (
	cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"
)

func init() {
	registerMigration(&Migration{
		Description: "Cairn: create review-policy table",
		Upgrade:     cairnmigrations.V502CreateReviewPolicyTable,
	})
}
