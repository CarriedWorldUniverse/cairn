//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package migrations_test

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

func TestV501CreateSummarizerTables(t *testing.T) {
	eng := cairntest.NewEngine(t)

	for _, table := range []string{"cairn_summarizer_config", "cairn_summarizer_repo_consent", "cairn_pr_summary"} {
		exists, err := eng.IsTableExist(table)
		if err != nil {
			t.Fatalf("IsTableExist(%q): %v", table, err)
		}
		if !exists {
			t.Errorf("table %q not created", table)
		}
	}
}
