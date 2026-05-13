// Cairn AI-first identity layer migration. Adds parent_user_id to the
// forgejo `user` table so bot users (Type=4) can link back to the human
// owner who provisioned them. See Cairn AI-first issue system spec §3.3.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.

package migrations

import (
	"strings"

	"xorm.io/xorm"
)

// V504AddUserParent adds the `parent_user_id` column to the user table
// and indexes it. Idempotent: skipped silently if the column already
// exists (PRAGMA table_info probe), so re-running on already-migrated
// DBs is a no-op.
//
// SQLite-targeted today (matches the bundled deployment); the column
// definition is portable enough that the same statement runs on
// Postgres + MySQL, but the existence probe uses sqlite_master /
// PRAGMA so future cross-DB support needs a switch on the dialect.
func V504AddUserParent(x *xorm.Engine) error {
	exists, err := userColumnExists(x, "parent_user_id")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := x.Exec(`ALTER TABLE "user" ADD COLUMN parent_user_id INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	// CREATE INDEX IF NOT EXISTS is portable; safe to run regardless of
	// whether the ALTER actually fired this pass.
	if _, err := x.Exec(`CREATE INDEX IF NOT EXISTS IDX_user_parent_user_id ON "user" (parent_user_id)`); err != nil {
		return err
	}
	return nil
}

// userColumnExists checks whether a column is already present on the
// user table. Used to make this migration idempotent against fresh
// installs (where xorm's Sync2 created the column from the struct
// tag) and partial reruns.
func userColumnExists(x *xorm.Engine, col string) (bool, error) {
	rows, err := x.QueryString(`PRAGMA table_info("user")`)
	if err != nil {
		return false, err
	}
	for _, r := range rows {
		if strings.EqualFold(r["name"], col) {
			return true, nil
		}
	}
	return false, nil
}
