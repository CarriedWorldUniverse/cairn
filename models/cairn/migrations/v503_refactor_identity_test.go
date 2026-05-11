//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package migrations_test

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

func TestV503DropsAgentEmbeddedPubkey(t *testing.T) {
	eng := cairntest.NewEngine(t)
	// After V503, cairn_agent should NOT have public_key or fingerprint cols.
	tables, err := eng.DBMetas()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, table := range tables {
		if table.Name != "cairn_agent" {
			continue
		}
		found = true
		for _, col := range table.Columns() {
			if col.Name == "public_key" || col.Name == "fingerprint" {
				t.Errorf("cairn_agent still has dropped column %q", col.Name)
			}
		}
	}
	if !found {
		t.Fatal("cairn_agent table not present after V503")
	}
}

func TestV503CreatesNewTables(t *testing.T) {
	eng := cairntest.NewEngine(t)
	for _, table := range []string{"cairn_attachment_request", "cairn_agent_pubkey"} {
		exists, err := eng.IsTableExist(table)
		if err != nil {
			t.Fatalf("IsTableExist %q: %v", table, err)
		}
		if !exists {
			t.Errorf("table %q not created", table)
		}
	}
}
