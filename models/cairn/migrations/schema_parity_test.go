package migrations

import (
	"sort"
	"testing"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"xorm.io/xorm"
	"xorm.io/xorm/names"

	_ "github.com/mattn/go-sqlite3"
)

// schemaSnapshot captures column + index info for a single table.
type schemaSnapshot struct {
	columns []string // formatted "<name> <type> <nullability>"
	indexes []string // formatted "<name> <unique?> <columns>"
}

// snapshotTable queries sqlite_master + pragma to produce a stable
// snapshot of a table's schema.
func snapshotTable(t *testing.T, eng *xorm.Engine, table string) schemaSnapshot {
	t.Helper()
	var snap schemaSnapshot

	// Columns from PRAGMA table_info.
	rows, err := eng.DB().Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    interface{}
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		nullStr := "NULL"
		if notnull != 0 {
			nullStr = "NOT NULL"
		}
		snap.columns = append(snap.columns, name+" "+ctype+" "+nullStr)
	}
	sort.Strings(snap.columns)

	// Indexes from PRAGMA index_list.
	idxRows, err := eng.DB().Query("PRAGMA index_list(" + table + ")")
	if err != nil {
		t.Fatalf("PRAGMA index_list(%s): %v", table, err)
	}
	type idxInfo struct {
		name   string
		unique int
	}
	var indexes []idxInfo
	for idxRows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := idxRows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		indexes = append(indexes, idxInfo{name: name, unique: unique})
	}
	idxRows.Close()

	for _, idx := range indexes {
		// Get the columns this index covers.
		colRows, err := eng.DB().Query("PRAGMA index_info(" + idx.name + ")")
		if err != nil {
			t.Fatalf("PRAGMA index_info(%s): %v", idx.name, err)
		}
		var cols []string
		for colRows.Next() {
			var seqno, cid int
			var name string
			if err := colRows.Scan(&seqno, &cid, &name); err != nil {
				t.Fatalf("scan index col: %v", err)
			}
			cols = append(cols, name)
		}
		colRows.Close()
		uniqStr := "INDEX"
		if idx.unique != 0 {
			uniqStr = "UNIQUE"
		}
		snap.indexes = append(snap.indexes, idx.name+" "+uniqStr+" ("+joinCols(cols)+")")
	}
	sort.Strings(snap.indexes)

	return snap
}

func joinCols(cols []string) string {
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ","
		}
		out += c
	}
	return out
}

// newEngine returns an in-memory SQLite engine configured with
// GonicMapper (matching production at models/db/engine.go).
func newEngine(t *testing.T) *xorm.Engine {
	t.Helper()
	eng, err := xorm.NewEngine("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	eng.SetMapper(names.GonicMapper{})
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func TestSchemaParity_AgentTable(t *testing.T) {
	// Path A: migration.
	engA := newEngine(t)
	if err := V500CreateAgentTables(engA); err != nil {
		t.Fatalf("migration: %v", err)
	}
	snapA := snapshotTable(t, engA, "cairn_agent")

	// Path B: SyncAllTables on runtime models.
	engB := newEngine(t)
	if err := engB.Sync2(new(cairn.Agent)); err != nil {
		t.Fatalf("Sync2: %v", err)
	}
	snapB := snapshotTable(t, engB, "cairn_agent")

	if !columnsEqual(snapA.columns, snapB.columns) {
		t.Errorf("cairn_agent columns differ\nmigration: %v\nsync2: %v", snapA.columns, snapB.columns)
	}
	if !columnsEqual(snapA.indexes, snapB.indexes) {
		t.Errorf("cairn_agent indexes differ\nmigration: %v\nsync2: %v", snapA.indexes, snapB.indexes)
	}
}

func TestSchemaParity_AgentBlocklistTable(t *testing.T) {
	engA := newEngine(t)
	if err := V500CreateAgentTables(engA); err != nil {
		t.Fatalf("migration: %v", err)
	}
	snapA := snapshotTable(t, engA, "cairn_agent_blocklist")

	engB := newEngine(t)
	if err := engB.Sync2(new(cairn.AgentBlocklist)); err != nil {
		t.Fatalf("Sync2: %v", err)
	}
	snapB := snapshotTable(t, engB, "cairn_agent_blocklist")

	if !columnsEqual(snapA.columns, snapB.columns) {
		t.Errorf("cairn_agent_blocklist columns differ\nmigration: %v\nsync2: %v", snapA.columns, snapB.columns)
	}
	if !columnsEqual(snapA.indexes, snapB.indexes) {
		t.Errorf("cairn_agent_blocklist indexes differ\nmigration: %v\nsync2: %v", snapA.indexes, snapB.indexes)
	}
}

func columnsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
