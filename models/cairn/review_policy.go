//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

// ReviewPolicy is per-org configuration for Cairn's human-review enforcement.
// When RequireHumanOnly is true:
//   - Agent approvals do not count toward "X approving reviews required" gates
//   - PRs from agents owned by user X cannot be approved by X or by any of
//     X's other agents (owner-cluster self-approval block)
//   - New repos in this org get default branch protection auto-applied to
//     main/master requiring 1+ approving reviews
type ReviewPolicy struct {
	OwnerID          int64 `xorm:"pk"`
	RequireHumanOnly bool  `xorm:"NOT NULL DEFAULT true"`
	CreatedUnix      int64 `xorm:"created"`
	UpdatedUnix      int64 `xorm:"updated"`
}

// TableName returns the SQL table name for ReviewPolicy.
func (ReviewPolicy) TableName() string { return "cairn_review_policy" }
