package change

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// migrate must be idempotent by inspection, not by parsing an error message
// (#166): it adds a column that is missing and leaves one that exists alone.
func TestMigrateAddsMissingColumnsAndIsIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// The shape a repo created BEFORE these columns existed would have.
	for _, ddl := range []string{
		`CREATE TABLE change (id TEXT PRIMARY KEY, line_id TEXT NOT NULL)`,
		`CREATE TABLE line (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		// operation as it stood before seq (#173): rows whose only order is rowid.
		`CREATE TABLE operation (id TEXT PRIMARY KEY, op_type TEXT NOT NULL, actor TEXT NOT NULL,
		   parent_op TEXT NOT NULL DEFAULT '', view_before TEXT NOT NULL, view_after TEXT NOT NULL,
		   detail TEXT NOT NULL DEFAULT '{}', at TEXT NOT NULL)`,
		`INSERT INTO operation(id, op_type, actor, view_before, view_after, at) VALUES
		   ('2026-09-02T03:46:38.9Z-x',    'commit', 'first',  '{}', '{}', ''),
		   ('2026-09-02T03:46:38.925Z-x',  'commit', 'second', '{}', '{}', ''),
		   ('2026-09-02T03:46:38.9251Z-x', 'commit', 'third',  '{}', '{}', '')`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	for pass := 1; pass <= 2; pass++ {
		if err := migrate(db); err != nil {
			t.Fatalf("migrate pass %d: %v", pass, err)
		}
		for _, c := range [][2]string{{"change", "sealed"}, {"line", "tracks_remote"}, {"operation", "seq"}} {
			has, err := hasColumn(db, c[0], c[1])
			if err != nil {
				t.Fatal(err)
			}
			if !has {
				t.Fatalf("pass %d: %s.%s missing after migrate", pass, c[0], c[1])
			}
		}
		// The seq backfill must reproduce insertion order for pre-#173 rows —
		// whose ids, being trimmed RFC3339Nano, sort in exactly the WRONG order.
		rows, err := db.Query(`SELECT actor FROM operation ORDER BY seq`)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err != nil {
				t.Fatal(err)
			}
			got = append(got, a)
		}
		_ = rows.Close()
		if want := []string{"first", "second", "third"}; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("pass %d: ORDER BY seq = %v, want %v — the backfill did not preserve insertion order", pass, got, want)
		}
		var idx int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_operation_seq'`).Scan(&idx); err != nil || idx != 1 {
			t.Fatalf("pass %d: unique seq index missing (count=%d, err=%v)", pass, idx, err)
		}
	}
}

// StashPush on a change with nothing un-sealed must return the sentinel the
// CLI keys its no-op behaviour on — via errors.Is, through worktree's wrapping.
func TestStashPushReturnsSentinelWhenNothingToStash(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()
	main, err := e.LineByName("main")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := e.CreateChange(main.ID, "a")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.StashPush(ch.ID, "nothing here")
	if !errors.Is(err, ErrNothingToStash) {
		t.Fatalf("StashPush on a clean change: got %v, want ErrNothingToStash", err)
	}
}
