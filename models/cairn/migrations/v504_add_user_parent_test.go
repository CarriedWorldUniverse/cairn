//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package migrations_test

import (
	"testing"

	cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"

	_ "github.com/mattn/go-sqlite3"
	"xorm.io/xorm"
	"xorm.io/xorm/names"
)

// newEngineWithUserTable builds a bare in-memory engine with just
// enough of the forgejo `user` table for V504 to ALTER. Mirrors the
// shape used by V504 (no need to mirror every column — ALTER only
// touches the existing schema, the migration is portable across any
// pre-existing user-table layout).
func newEngineWithUserTable(t *testing.T) *xorm.Engine {
	t.Helper()
	eng, err := xorm.NewEngine("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	eng.SetMapper(names.GonicMapper{})
	if _, err := eng.Exec(`CREATE TABLE "user" (id INTEGER PRIMARY KEY, name TEXT, type INTEGER NOT NULL DEFAULT 0)`); err != nil {
		eng.Close()
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}

func TestV504AddsParentUserIDColumn(t *testing.T) {
	eng := newEngineWithUserTable(t)
	if err := cairnmigrations.V504AddUserParent(eng); err != nil {
		t.Fatalf("V504: %v", err)
	}
	rows, err := eng.QueryString(`PRAGMA table_info("user")`)
	if err != nil {
		t.Fatalf("PRAGMA: %v", err)
	}
	var found bool
	for _, r := range rows {
		if r["name"] == "parent_user_id" {
			found = true
			if r["dflt_value"] != "0" {
				t.Errorf("parent_user_id default: got %q, want 0", r["dflt_value"])
			}
			if r["notnull"] != "1" {
				t.Errorf("parent_user_id NOT NULL: got %q, want 1", r["notnull"])
			}
		}
	}
	if !found {
		t.Fatal("parent_user_id column was not created")
	}
}

func TestV504IsIdempotent(t *testing.T) {
	eng := newEngineWithUserTable(t)
	if err := cairnmigrations.V504AddUserParent(eng); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := cairnmigrations.V504AddUserParent(eng); err != nil {
		t.Fatalf("second run (should be no-op): %v", err)
	}
}

func TestV504IndexCreated(t *testing.T) {
	eng := newEngineWithUserTable(t)
	if err := cairnmigrations.V504AddUserParent(eng); err != nil {
		t.Fatalf("V504: %v", err)
	}
	rows, err := eng.QueryString(`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='user'`)
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	var found bool
	for _, r := range rows {
		if r["name"] == "IDX_user_parent_user_id" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("IDX_user_parent_user_id index missing; got rows: %v", rows)
	}
}
