// Cairn-specific migration registration. Adds the simplifier tables
// (cairn_summarizer_config, cairn_summarizer_repo_consent, cairn_pr_summary).
// Mirror of v500a's pattern.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.

package forgejo_migrations

import (
	cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"
)

func init() {
	registerMigration(&Migration{
		Description: "Cairn: create simplifier tables",
		Upgrade:     cairnmigrations.V501CreateSummarizerTables,
	})
}
