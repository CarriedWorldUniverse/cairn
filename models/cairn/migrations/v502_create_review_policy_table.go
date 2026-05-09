//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package migrations

import (
	"xorm.io/xorm"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// V502CreateReviewPolicyTable adds the cairn_review_policy table for the
// AI-native human-review enforcement feature. Additive only; no Forgejo
// schema is touched.
func V502CreateReviewPolicyTable(x *xorm.Engine) error {
	return x.Table("cairn_review_policy").Sync2(new(cairnmodels.ReviewPolicy))
}
