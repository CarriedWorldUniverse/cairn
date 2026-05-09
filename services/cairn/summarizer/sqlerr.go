// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import "strings"

// isUniqueViolation reports whether err is a database-driver unique-
// constraint error. Recognises SQLite (modernc/mattn) and Postgres
// shapes; returns false for unknown drivers (caller will see the raw
// error).
//
// Duplicated from services/cairn/identity/xorm_store.go. Consolidate
// into a shared cairn/internal/cairndb package when one exists.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return true
	case strings.Contains(msg, "constraint failed: UNIQUE"):
		return true
	case strings.Contains(msg, "duplicate key value"):
		return true
	}
	return false
}
