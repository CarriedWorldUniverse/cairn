package change

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

// syncAuthor is the author recorded on merge commits the engine synthesises when
// reconciling a divergent line against its remote counterpart.
const syncAuthor = "sync"

// LineResult is the per-line outcome of a pull: the line name, the action taken
// (up-to-date | fast-forward | merged), and the number of conflicts recorded if
// the reconcile produced a 3-way merge.
type LineResult struct {
	Line      string
	Status    string
	Conflicts int
}

// PullSummary is the outcome of PullFromRemote across every reconciled line.
type PullSummary struct {
	Lines []LineResult
}

// fetchTracking fetches remoteName's heads into tracking refs
// (refs/remotes/<remote>/*) plus all tags, WITHOUT touching refs/heads. This is
// the read-only half of a pull: local lines (which live in the SQLite catalogue
// and in refs/heads) are never clobbered, so they may hold uncommitted work that
// reconcile then merges against the fetched remote tips.
func (e *Engine) fetchTracking(remoteName string) error {
	rem, err := e.git.Remote(remoteName)
	if errors.Is(err, git.ErrRemoteNotFound) {
		return fmt.Errorf("change.fetchTracking: no remote %q", remoteName)
	}
	if err != nil {
		return fmt.Errorf("change.fetchTracking: %w", err)
	}
	err = rem.Fetch(&git.FetchOptions{
		RefSpecs: []config.RefSpec{
			config.RefSpec("+refs/heads/*:refs/remotes/" + remoteName + "/*"),
		},
		Tags: git.AllTags,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("change.fetchTracking: %w", err)
	}
	return nil
}

// FetchTracking fetches remoteName's heads into tracking refs without touching
// local lines — the read-only "fetch" verb. It is a thin exported wrapper over
// fetchTracking so the worktree layer can offer `cairn fetch` without exposing
// the reconcile half of a pull.
func (e *Engine) FetchTracking(remoteName string) error {
	return e.fetchTracking(remoteName)
}

// remoteHeads returns short-name → commit-sha for every hash reference under
// refs/remotes/<remoteName>/, skipping the remote's HEAD pointer.
func (e *Engine) remoteHeads(remoteName string) (map[string]string, error) {
	prefix := "refs/remotes/" + remoteName + "/"
	iter, err := e.git.Storer.IterReferences()
	if err != nil {
		return nil, fmt.Errorf("change.remoteHeads: %w", err)
	}
	defer iter.Close()
	out := map[string]string{}
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		n := ref.Name().String()
		if !strings.HasPrefix(n, prefix) {
			return nil
		}
		short := n[len(prefix):]
		if short == "HEAD" {
			return nil
		}
		out[short] = ref.Hash().String()
		return nil
	}); err != nil {
		return nil, fmt.Errorf("change.remoteHeads: %w", err)
	}
	return out, nil
}

// PullFromRemote fetches remoteName into tracking refs and reconciles every open
// local line whose name matches a fetched remote branch. Each line is brought up
// to date independently: up-to-date / local-ahead (no-op), fast-forward (adopt
// the remote tip), or a 3-way merge (a 2-parent merge commit, with any conflicts
// recorded as data on the line's active change). The per-line catalogue writes
// are transactional.
func (e *Engine) PullFromRemote(remoteName string) (PullSummary, error) {
	if err := e.fetchTracking(remoteName); err != nil {
		return PullSummary{}, fmt.Errorf("change.PullFromRemote: %w", err)
	}
	rheads, err := e.remoteHeads(remoteName)
	if err != nil {
		return PullSummary{}, fmt.Errorf("change.PullFromRemote: %w", err)
	}

	rows, err := e.db.Query(`SELECT id, name, tip_commit FROM line WHERE status='open'`)
	if err != nil {
		return PullSummary{}, fmt.Errorf("change.PullFromRemote: %w", err)
	}
	type lineRow struct {
		id, name, tip string
	}
	var lines []lineRow
	for rows.Next() {
		var lr lineRow
		if err := rows.Scan(&lr.id, &lr.name, &lr.tip); err != nil {
			_ = rows.Close()
			return PullSummary{}, fmt.Errorf("change.PullFromRemote: %w", err)
		}
		lines = append(lines, lr)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return PullSummary{}, fmt.Errorf("change.PullFromRemote: %w", err)
	}
	_ = rows.Close()

	var sum PullSummary
	// Each line reconcile is its own transaction (see reconcileLine's applyHead /
	// applyMerge). On an error mid-loop, already-reconciled lines stay committed
	// and a retry is safe — it re-fetches and re-reconciles, finding the done
	// lines up-to-date. Full cross-line atomicity (all-or-nothing across every
	// line) is a Phase-2 concern, not provided here.
	for _, lr := range lines {
		r, ok := rheads[lr.name]
		if !ok {
			continue
		}
		res, err := e.reconcileLine(lr.id, lr.name, lr.tip, r)
		if err != nil {
			return PullSummary{}, fmt.Errorf("change.PullFromRemote: %w", err)
		}
		sum.Lines = append(sum.Lines, res)
	}
	return sum, nil
}

// reconcileLine reconciles one open line against its remote tip R. L is the
// line's active open-change head (or the line tip if the change has no commit
// yet). The catalogue writes (conflict rows, change head, line tip) commit or
// roll back together, matching the Commit path.
func (e *Engine) reconcileLine(lineID, lineName, lineTip, r string) (LineResult, error) {
	// Find the line's active open change WITHOUT creating one. A change is only
	// needed on the diverged-merge path (to carry the merge head + conflicts);
	// creating one here would leave a dangling, never-committed "sync" change on
	// the up-to-date / ahead / fast-forward paths.
	var changeID, changeHead string
	hasChange := true
	err := e.db.QueryRow(
		`SELECT id, head_commit FROM change WHERE line_id=? AND status='open' ORDER BY updated_at DESC LIMIT 1`,
		lineID).Scan(&changeID, &changeHead)
	if errors.Is(err, sql.ErrNoRows) {
		hasChange = false
		changeID = ""
		changeHead = ""
	} else if err != nil {
		return LineResult{}, fmt.Errorf("reconcile: find change: %w", err)
	}

	// L is the local side: the change head when a change exists (falling back to
	// the line tip if that change has no commit yet), otherwise the line tip.
	l := changeHead
	if l == "" {
		l = lineTip
	}

	if l == r {
		return LineResult{Line: lineName, Status: "up-to-date"}, nil
	}

	base, err := e.mergeBase(l, r)
	if err != nil {
		return LineResult{}, err
	}

	switch {
	case base == r:
		// Local already contains the remote tip: local is ahead (or equal). No-op.
		return LineResult{Line: lineName, Status: "up-to-date"}, nil

	case base == l && l != "":
		// Fast-forward: adopt the remote tip wholesale. When the line has an open
		// change, advance its head too; otherwise advance only the line tip — no
		// change is created.
		if err := e.applyHead(changeID, lineID, r, 0); err != nil {
			return LineResult{}, err
		}
		return LineResult{Line: lineName, Status: "fast-forward"}, nil

	default:
		// Diverged: a real 3-way merge needs a change to attach the merge head and
		// any conflicts. Create one now only if the line had none.
		if !hasChange {
			ch, cerr := e.CreateChange(lineID, syncAuthor)
			if cerr != nil {
				return LineResult{}, cerr
			}
			changeID = ch.ID
		}
		// Diverged (or unrelated histories, base==""): 3-way merge. ours=remote,
		// theirs=local — so the merge favours nothing automatically and records a
		// conflict on a genuine same-region divergence.
		var baseTree string
		if base != "" {
			if baseTree, err = e.commitTree(base); err != nil {
				return LineResult{}, err
			}
		}
		rTree, err := e.commitTree(r)
		if err != nil {
			return LineResult{}, err
		}
		var lTree string
		if l != "" {
			if lTree, err = e.commitTree(l); err != nil {
				return LineResult{}, err
			}
		}
		// cairn convention: ours = the side being adopted (here the remote tip),
		// theirs = the local change — consistent with merge-forward, where ours is
		// the parent line being adopted. So a recorded conflict's parent_blob is
		// the adopted/remote side and its change_blob is the local side, by design.
		merged, conflicts, err := e.mergeTrees(changeID, baseTree, rTree, lTree)
		if err != nil {
			return LineResult{}, err
		}
		// A 2-parent merge commit: [L, R]. When the local side has no commit yet
		// (l==""), parent only on R so the merge is still a real descendant of the
		// remote tip and remains pushable.
		parents := []string{r}
		if l != "" {
			parents = []string{l, r}
		}
		head, err := e.writeCommit(merged, changeID, "merge remote-tracking", parents)
		if err != nil {
			return LineResult{}, err
		}
		hasConflict := 0
		if len(conflicts) > 0 {
			hasConflict = 1
		}
		if err := e.applyMerge(changeID, lineID, head, hasConflict, conflicts); err != nil {
			return LineResult{}, err
		}
		return LineResult{Line: lineName, Status: "merged", Conflicts: len(conflicts)}, nil
	}
}

// applyHead advances the owning line tip to head — and, when changeID is non-
// empty, the change head too — in one transaction (fast-forward / no-conflict
// path). A "" changeID means the line has no open change, so only the line tip
// moves and no change is touched or created.
func (e *Engine) applyHead(changeID, lineID, head string, hasConflict int) error {
	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("reconcile: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if changeID != "" {
		if _, err := tx.Exec(
			`UPDATE change SET head_commit=?, has_conflict=?, updated_at=? WHERE id=?`,
			head, hasConflict, ts, changeID); err != nil {
			return fmt.Errorf("reconcile: advance change head: %w", err)
		}
	}
	if _, err := tx.Exec(`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`, head, ts, lineID); err != nil {
		return fmt.Errorf("reconcile: advance line tip: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reconcile: commit tx: %w", err)
	}
	return nil
}

// applyMerge inserts the conflict rows, advances the change head + has_conflict,
// and advances the owning line tip — all in one transaction, exactly as Commit
// persists merge conflicts as data.
func (e *Engine) applyMerge(changeID, lineID, head string, hasConflict int, conflicts []Conflict) error {
	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("reconcile: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, c := range conflicts {
		if err := insertConflict(tx, c, ts); err != nil {
			return fmt.Errorf("reconcile: record conflict: %w", err)
		}
	}
	if _, err := tx.Exec(
		`UPDATE change SET head_commit=?, has_conflict=?, updated_at=? WHERE id=?`,
		head, hasConflict, ts, changeID); err != nil {
		return fmt.Errorf("reconcile: advance change head: %w", err)
	}
	if _, err := tx.Exec(`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`, head, ts, lineID); err != nil {
		return fmt.Errorf("reconcile: advance line tip: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("reconcile: commit tx: %w", err)
	}
	return nil
}
