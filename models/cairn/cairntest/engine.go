// Package cairntest provides shared test fixtures for Cairn-side tests.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairntest

import (
	"testing"

	cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"
	"xorm.io/xorm"
	"xorm.io/xorm/names"

	_ "github.com/mattn/go-sqlite3"
)

// NewEngine returns an in-memory SQLite engine with the GonicMapper
// configured (matching production at models/db/engine.go) and Cairn's
// V500 + V501 migrations applied.
//
// Engines returned from NewEngine are isolated per test (`:memory:`),
// closed automatically via t.Cleanup. Tests should not share engines.
func NewEngine(t *testing.T) *xorm.Engine {
	t.Helper()
	eng, err := xorm.NewEngine("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	eng.SetMapper(names.GonicMapper{})
	if err := cairnmigrations.V500CreateAgentTables(eng); err != nil {
		eng.Close()
		t.Fatal(err)
	}
	if err := cairnmigrations.V501CreateSummarizerTables(eng); err != nil {
		eng.Close()
		t.Fatalf("V501: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	return eng
}
