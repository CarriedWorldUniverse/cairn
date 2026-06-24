package change

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// openRawDB opens the same SQLite DB the engine uses — same driver and DSN
// (with the busy_timeout pragma). This lets tests build a legacy schema
// without going through Open/schemaSQL.
func openRawDB(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "cairn.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("openRawDB: %v", err)
	}
	return db
}

// TestMigrateAddsSealedToLegacyDB verifies that Open on a pre-existing repo
// whose change table was created WITHOUT the sealed column (task-2 addition)
// still succeeds and that GetChange returns Sealed=false for rows inserted
// before the migration ran.
func TestMigrateAddsSealedToLegacyDB(t *testing.T) {
	dir := t.TempDir()

	// ── Phase 1: create a legacy DB that mimics a repo created before task 2 ──
	// We open the raw sqlite file and build ONLY the tables that existed before
	// sealed was introduced, then insert a change row.
	legacyDB := openRawDB(t, dir)

	// WAL + FK pragmas (mirrors what schemaSQL sets on a fresh open).
	if _, err := legacyDB.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		t.Fatalf("WAL pragma: %v", err)
	}
	if _, err := legacyDB.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("FK pragma: %v", err)
	}

	// Legacy line table (unchanged, still needed for FK).
	if _, err := legacyDB.Exec(`CREATE TABLE IF NOT EXISTS line (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL UNIQUE,
		parent_line TEXT REFERENCES line(id),
		tip_commit  TEXT NOT NULL DEFAULT '',
		base_commit TEXT NOT NULL DEFAULT '',
		status      TEXT NOT NULL DEFAULT 'open',
		created_at  TEXT NOT NULL,
		updated_at  TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy line: %v", err)
	}

	// Legacy change table — note NO sealed column.
	if _, err := legacyDB.Exec(`CREATE TABLE IF NOT EXISTS change (
		id           TEXT PRIMARY KEY,
		line_id      TEXT NOT NULL REFERENCES line(id) ON DELETE CASCADE,
		author       TEXT NOT NULL,
		head_commit  TEXT NOT NULL DEFAULT '',
		status       TEXT NOT NULL DEFAULT 'open',
		has_conflict INTEGER NOT NULL DEFAULT 0,
		created_at   TEXT NOT NULL,
		updated_at   TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create legacy change: %v", err)
	}

	// Insert a root line so the FK constraint is satisfied.
	if _, err := legacyDB.Exec(`INSERT INTO line(id, name, parent_line, tip_commit, base_commit, status, created_at, updated_at)
		VALUES('line-root','main',NULL,'','','open','2024-01-01T00:00:00Z','2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert legacy root line: %v", err)
	}

	// Insert a legacy change row (no sealed column — it doesn't exist yet).
	const legacyChangeID = "zlegacychangeid00000000000000000000000000000000000000000000000000"
	if _, err := legacyDB.Exec(`INSERT INTO change(id, line_id, author, head_commit, status, has_conflict, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		legacyChangeID, "line-root", "legacy-author", "", "open", 0,
		"2024-01-01T00:00:00Z", "2024-01-01T00:00:00Z"); err != nil {
		t.Fatalf("insert legacy change: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close legacyDB: %v", err)
	}

	// ── Phase 2: Open the engine on the same dir (runs schemaSQL + migrate) ──
	e, err := Open(dir)
	if err != nil {
		t.Fatalf("Open on legacy dir: %v", err)
	}
	defer e.Close()

	// GetChange must succeed — the sealed column now exists (migrate added it).
	ch, err := e.GetChange(legacyChangeID)
	if err != nil {
		t.Fatalf("GetChange on legacy row: %v", err)
	}
	if ch.Sealed {
		t.Fatalf("legacy row Sealed = true, want false (DEFAULT 0 from migration)")
	}
	if ch.Author != "legacy-author" {
		t.Fatalf("Author = %q, want legacy-author", ch.Author)
	}
}

// TestMigrateIdempotentFreshRepo verifies that opening a fresh repo twice
// (second open triggers the ALTER again) does not error — the duplicate-
// column error is silently ignored.
func TestMigrateIdempotentFreshRepo(t *testing.T) {
	dir := t.TempDir()

	e1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	if err := e1.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}

	e2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open 2 (should ignore duplicate-column from ALTER): %v", err)
	}
	defer e2.Close()

	// Sanity-check the engine is usable after double-open.
	root, err := e2.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName after second Open: %v", err)
	}
	if root.Status != "open" {
		t.Fatalf("root status = %q, want open", root.Status)
	}
}
