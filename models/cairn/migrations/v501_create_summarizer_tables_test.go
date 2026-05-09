//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package migrations_test

import (
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
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

func TestV501_PRSummaryUniqueOnContentHashPerPR(t *testing.T) {
	eng := cairntest.NewEngine(t)
	row1 := &cairnmodels.PRSummary{RepoID: 1, PRNumber: 1, ContentHash: "h1", SummaryMD: "x", ModelID: "m"}
	if _, err := eng.Insert(row1); err != nil {
		t.Fatalf("insert row1: %v", err)
	}
	// Same composite key — must fail.
	row2 := &cairnmodels.PRSummary{RepoID: 1, PRNumber: 1, ContentHash: "h1", SummaryMD: "y", ModelID: "m"}
	if _, err := eng.Insert(row2); err == nil {
		t.Errorf("duplicate (repo_id, pr_number, content_hash) was accepted; expected unique violation")
	}
	// Different content_hash — must succeed.
	row3 := &cairnmodels.PRSummary{RepoID: 1, PRNumber: 1, ContentHash: "h2", SummaryMD: "z", ModelID: "m"}
	if _, err := eng.Insert(row3); err != nil {
		t.Errorf("different content_hash should succeed: %v", err)
	}
	// Different repo or PR with same hash — should also succeed (the unique is composite).
	row4 := &cairnmodels.PRSummary{RepoID: 2, PRNumber: 1, ContentHash: "h1", SummaryMD: "x", ModelID: "m"}
	if _, err := eng.Insert(row4); err != nil {
		t.Errorf("different repo with same hash should succeed: %v", err)
	}
}
