package change

import (
	"fmt"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

// opCommit is the op-log type for a seal: finalizing a working change into a
// described commit. Unlike opSnapshot it never coalesces.
const opCommit = "commit"

// Seal finalizes the open working change `changeID`: it stamps `message` onto the
// working commit's tree (a described commit replacing the "(working)"
// placeholder), adopts the parent line via merge-forward (conflicts-as-data),
// marks the change sealed, advances the line tip to the sealed commit, and opens a
// FRESH open change on the same line whose future working commit will sit on top
// of the sealed commit. Returns the new working change-id and any conflicts.
//
// The caller is expected to have already snapshotted the folder into the working
// commit (Repo.Commit runs SyncWorking first). A change never snapshotted seals an
// empty tree.
//
// All catalogue writes — conflict rows, sealing the old change, advancing the line
// tip, inserting the fresh change, and the op-log entry — commit or roll back
// together in ONE transaction, exactly like Commit. The go-git tree/commit writes
// stay outside the tx (content-addressed and idempotent).
func (e *Engine) Seal(changeID, message string) (newID string, conflicts []Conflict, err error) {
	ch, err := e.GetChange(changeID)
	if err != nil {
		return "", nil, err
	}
	line, err := e.lineByID(ch.LineID)
	if err != nil {
		return "", nil, err
	}
	before, err := e.viewMap()
	if err != nil {
		return "", nil, fmt.Errorf("change.Seal: %w", err)
	}

	// Determine the tree to seal and the parent commit. The working commit's tree
	// and parent are carried over verbatim — only the message changes — so the
	// sealed commit is the working snapshot with a real description.
	var treeSha, parent string
	if ch.HeadCommit != "" {
		wc, cerr := e.git.CommitObject(plumbing.NewHash(ch.HeadCommit))
		if cerr != nil {
			return "", nil, fmt.Errorf("change.Seal: read working commit: %w", cerr)
		}
		treeSha = wc.TreeHash.String()
		if parent, err = e.firstParent(ch.HeadCommit); err != nil {
			return "", nil, fmt.Errorf("change.Seal: %w", err)
		}
	} else {
		// Never snapshotted: seal an empty tree rooted on the line's current tip.
		tree, terr := e.writeTreeRefs(nil)
		if terr != nil {
			return "", nil, fmt.Errorf("change.Seal: empty tree: %w", terr)
		}
		treeSha = tree.String()
		parent = line.TipCommit
	}

	var sealParents []string
	if parent != "" {
		sealParents = []string{parent}
	}

	// Stamp the message onto the working tree.
	sealed, err := e.writeCommit(treeSha, ch.ID, message, sealParents)
	if err != nil {
		return "", nil, err
	}

	// Adopt the parent line, recording conflicts as data. If the merge produced a
	// different tree, re-commit on it so the sealed head reflects the adopted
	// state. (Mirrors Commit: mergeForward always returns a non-empty tree — the
	// snapshot's own for a root/empty-parent line — so compare against treeSha.)
	merged, conflicts, err := e.mergeForward(ch.ID, sealed)
	if err != nil {
		return "", nil, err
	}
	if merged != "" && merged != treeSha {
		sealed, err = e.writeCommit(merged, ch.ID, message, []string{sealed})
		if err != nil {
			return "", nil, err
		}
	}
	hasConflict := 0
	if len(conflicts) > 0 {
		hasConflict = 1
	}

	newID = newChangeID()

	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return "", nil, fmt.Errorf("change.Seal: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, c := range conflicts {
		if err := insertConflict(tx, c, ts); err != nil {
			return "", nil, fmt.Errorf("change.Seal: record conflict: %w", err)
		}
	}
	// Seal the old change: freeze its head at the described commit.
	if _, err := tx.Exec(
		`UPDATE change SET head_commit=?, has_conflict=?, sealed=1, updated_at=? WHERE id=?`,
		sealed, hasConflict, ts, ch.ID); err != nil {
		return "", nil, fmt.Errorf("change.Seal: seal change: %w", err)
	}
	// Advance the owning line's tip to the sealed commit.
	if _, err := tx.Exec(
		`UPDATE line SET tip_commit=?, updated_at=? WHERE id=?`,
		sealed, ts, ch.LineID); err != nil {
		return "", nil, fmt.Errorf("change.Seal: advance line tip: %w", err)
	}
	// Open the fresh change inline (in-tx INSERT, not CreateChange, so it is
	// atomic with the seal). Same column set CreateChange uses: open status, no
	// head, unsealed, no conflict.
	if _, err := tx.Exec(
		`INSERT INTO change(id, line_id, author, head_commit, status, has_conflict, sealed, created_at, updated_at)
		 VALUES(?,?,?,'','open',0,0,?,?)`,
		newID, ch.LineID, ch.Author, ts, ts); err != nil {
		return "", nil, fmt.Errorf("change.Seal: open fresh change: %w", err)
	}

	// Record a non-coalesced commit op in-tx (so view_after sees the new line tip),
	// mirroring recordSnapshotOp's insert but with op_type=commit and empty detail.
	after, err := viewMapTx(tx)
	if err != nil {
		return "", nil, fmt.Errorf("change.Seal: %w", err)
	}
	if err := recordOpTx(tx, e.now().UTC(), opCommit, ch.Author, before, after, ts); err != nil {
		return "", nil, fmt.Errorf("change.Seal: record op: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", nil, fmt.Errorf("change.Seal: commit tx: %w", err)
	}
	return newID, conflicts, nil
}
