package change

import (
	"fmt"
	"strings"
	"time"
)

// Privacy modes. omit removes a withheld path from the pushed projection
// entirely (no path, no name, no bytes); shape-only keeps the path but replaces
// its bytes with a placeholder. omit is the default (see worktree.MarkPrivate).
const (
	PrivacyOmit      = "omit"
	PrivacyShapeOnly = "shape-only"
)

// privatePlaceholder is the blob content a shape-only withheld file projects to.
// It carries none of the real bytes.
var privatePlaceholder = []byte("<<private>>\n")

// PrivateEntry is one privacy flag: a repo path withheld from every push, with
// the projection mode to apply. A flag covers the path itself AND everything
// beneath it (path/...), so a single flag withholds a whole subtree — no
// file-vs-folder distinction is needed, which also means redaction never depends
// on a (fragile) filesystem stat to decide what to withhold.
type PrivateEntry struct {
	Path, Mode, CreatedAt string
}

// MarkPrivate records (or replaces) a privacy flag for path with the given mode
// (PrivacyOmit or PrivacyShapeOnly). Re-marking a path updates its mode.
func (e *Engine) MarkPrivate(path, mode string) error {
	if mode != PrivacyOmit && mode != PrivacyShapeOnly {
		return fmt.Errorf("change.MarkPrivate: mode must be %q or %q, got %q", PrivacyOmit, PrivacyShapeOnly, mode)
	}
	cp := cleanPrivacyPath(path)
	if cp == "" {
		return fmt.Errorf("change.MarkPrivate: empty path")
	}
	at := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(
		`INSERT INTO privacy(path, mode, created_at) VALUES(?,?,?)
		 ON CONFLICT(path) DO UPDATE SET mode=excluded.mode`,
		cp, mode, at); err != nil {
		return fmt.Errorf("change.MarkPrivate: %w", err)
	}
	return nil
}

// UnmarkPrivate removes the privacy flag for path. Idempotent: removing a missing
// flag is not an error.
func (e *Engine) UnmarkPrivate(path string) error {
	if _, err := e.db.Exec(`DELETE FROM privacy WHERE path=?`, cleanPrivacyPath(path)); err != nil {
		return fmt.Errorf("change.UnmarkPrivate: %w", err)
	}
	return nil
}

// ListPrivate returns every privacy flag, ordered by path.
func (e *Engine) ListPrivate() ([]PrivateEntry, error) {
	rows, err := e.db.Query(`SELECT path, mode, created_at FROM privacy ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("change.ListPrivate: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PrivateEntry
	for rows.Next() {
		var p PrivateEntry
		if err := rows.Scan(&p.Path, &p.Mode, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("change.ListPrivate: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change.ListPrivate: %w", err)
	}
	return out, nil
}

// HasPrivate reports whether any privacy flag is set (the push fast-path: with
// no flags, redaction is skipped and the push is byte-identical to today).
func (e *Engine) HasPrivate() (bool, error) {
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM privacy`).Scan(&n); err != nil {
		return false, fmt.Errorf("change.HasPrivate: %w", err)
	}
	return n > 0, nil
}

// PrivacyMatch reports whether the repo path repoPath is withheld, and with what
// mode. The single source of truth for redaction.
func (e *Engine) PrivacyMatch(repoPath string) (mode string, ok bool) {
	flags, err := e.ListPrivate()
	if err != nil {
		return "", false
	}
	return matchPrivacy(flags, repoPath)
}

// matchPrivacy is the pure resolution rule, separated from the DB for testing. A
// flag on path P matches repoPath when repoPath == P or repoPath is under P/
// (subtree). When several flags match, the most specific (longest) wins.
func matchPrivacy(flags []PrivateEntry, repoPath string) (mode string, ok bool) {
	repoPath = cleanPrivacyPath(repoPath)
	bestLen := -1
	for _, f := range flags {
		fp := cleanPrivacyPath(f.Path)
		if repoPath == fp || strings.HasPrefix(repoPath, fp+"/") {
			if len(fp) > bestLen {
				bestLen = len(fp)
				mode, ok = f.Mode, true
			}
		}
	}
	return mode, ok
}

// cleanPrivacyPath normalizes a repo path for comparison: forward slashes, no
// leading "./", no surrounding slashes.
func cleanPrivacyPath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	p = strings.Trim(p, "/")
	return p
}
