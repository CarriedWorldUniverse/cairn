//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package migrations

import (
	"xorm.io/xorm"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// V501CreateSummarizerTables adds the simplifier tables. Additive only;
// no Forgejo schema is touched.
func V501CreateSummarizerTables(x *xorm.Engine) error {
	if err := x.Table("cairn_summarizer_config").Sync2(new(cairnmodels.SummarizerConfig)); err != nil {
		return err
	}
	if err := x.Table("cairn_summarizer_repo_consent").Sync2(new(cairnmodels.SummarizerRepoConsent)); err != nil {
		return err
	}
	if err := x.Table("cairn_pr_summary").Sync2(new(cairnmodels.PRSummary)); err != nil {
		return err
	}
	return nil
}
