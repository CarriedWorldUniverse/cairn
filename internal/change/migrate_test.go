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
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	for pass := 1; pass <= 2; pass++ {
		if err := migrate(db); err != nil {
			t.Fatalf("migrate pass %d: %v", pass, err)
		}
		for _, c := range [][2]string{{"change", "sealed"}, {"line", "tracks_remote"}} {
			has, err := hasColumn(db, c[0], c[1])
			if err != nil {
				t.Fatal(err)
			}
			if !has {
				t.Fatalf("pass %d: %s.%s missing after migrate", pass, c[0], c[1])
			}
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
