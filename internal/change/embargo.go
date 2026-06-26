package change

import (
	"fmt"
	"time"
)

// Embargo is the COMMIT-level half of the privacy model, distinct from the
// file/folder `private` flags. A `private` path is a secret that is NEVER pushed
// (withheld, full stop). An EMBARGOED commit is content you DO intend to
// distribute — just gated and not-yet-public: it (and everything after it) is
// held out of the PUBLIC projection until Disclose, while authorized recipients
// get the real bytes now through a cairn server's gated private store. This is
// the NEX-25 patch-gap: ship a fix to the people who need it before it is visible
// in the public repo, so it can't be patch-diffed into an n-day.
//
// Embargo flags travel in refs/cairn/meta so a cairn server can enforce them.

// EmbargoRefPrefix is the ref namespace a cairn client pushes the REAL (uncapped)
// embargoed tips + full meta to. The server relocates these into a per-repo gated
// private store; the public bare keeps only the capped projection.
const EmbargoRefPrefix = "refs/cairn/embargo/"

// MarkEmbargo flags a commit as embargoed (idempotent). The caller resolves the
// revision to a full sha first.
func (e *Engine) MarkEmbargo(commitSHA string) error {
	at := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(
		`INSERT INTO embargo(commit_sha, created_at) VALUES(?,?)
		 ON CONFLICT(commit_sha) DO NOTHING`, commitSHA, at); err != nil {
		return fmt.Errorf("change.MarkEmbargo: %w", err)
	}
	return nil
}

// UnmarkEmbargo lifts an embargo on a commit (Disclose). Idempotent.
func (e *Engine) UnmarkEmbargo(commitSHA string) error {
	if _, err := e.db.Exec(`DELETE FROM embargo WHERE commit_sha=?`, commitSHA); err != nil {
		return fmt.Errorf("change.UnmarkEmbargo: %w", err)
	}
	return nil
}

// ListEmbargo returns all embargoed commit shas (sorted).
func (e *Engine) ListEmbargo() ([]string, error) {
	rows, err := e.db.Query(`SELECT commit_sha FROM embargo ORDER BY commit_sha`)
	if err != nil {
		return nil, fmt.Errorf("change.ListEmbargo: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("change.ListEmbargo: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// IsEmbargoed reports whether a specific commit sha is embargoed.
func (e *Engine) IsEmbargoed(commitSHA string) (bool, error) {
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM embargo WHERE commit_sha=?`, commitSHA).Scan(&n); err != nil {
		return false, fmt.Errorf("change.IsEmbargoed: %w", err)
	}
	return n > 0, nil
}

// HasEmbargo reports whether any commit is embargoed (the push fast-path).
func (e *Engine) HasEmbargo() (bool, error) {
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM embargo`).Scan(&n); err != nil {
		return false, fmt.Errorf("change.HasEmbargo: %w", err)
	}
	return n > 0, nil
}

// embargoSet returns the embargoed commits as a set for O(1) lookups.
func (e *Engine) embargoSet() (map[string]bool, error) {
	list, err := e.ListEmbargo()
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(list))
	for _, s := range list {
		set[s] = true
	}
	return set, nil
}

// PublicTip returns the highest commit on tip's first-parent chain that is safe
// to publish: everything at or after the EARLIEST embargoed commit on the chain
// is held back. With no embargo on the chain it returns tip unchanged; if the
// chain's root itself is embargoed it returns "" (nothing is public).
func (e *Engine) PublicTip(tip string) (string, error) {
	set, err := e.embargoSet()
	if err != nil {
		return "", err
	}
	if len(set) == 0 || tip == "" {
		return tip, nil
	}
	// Walk first-parent tip→root, recording the chain.
	var chain []string
	for cur := tip; cur != ""; {
		chain = append(chain, cur)
		p, err := e.firstParent(cur)
		if err != nil {
			return "", fmt.Errorf("change.PublicTip: %w", err)
		}
		cur = p
		if len(chain) > describeWalkCap {
			return "", fmt.Errorf("change.PublicTip: chain exceeded %d commits", describeWalkCap)
		}
	}
	// The earliest (closest to root) embargoed commit caps the public view at its
	// parent. chain[i+1] is the first-parent of chain[i].
	for i := len(chain) - 1; i >= 0; i-- {
		if set[chain[i]] {
			if i+1 < len(chain) {
				return chain[i+1], nil
			}
			return "", nil // the root commit is embargoed → nothing public
		}
	}
	return tip, nil
}
