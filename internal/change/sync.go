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
	"github.com/go-git/go-git/v5/plumbing/transport"
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

// testFetchDelay, when non-nil, is invoked by fetchTracking just before the
// actual rem.Fetch call — a test seam for injecting artificial network
// latency into the fetch network phase, mirroring testNetworkDelay in
// push.go. Never set outside tests.
var testFetchDelay func()

// fetchTracking fetches remoteName's heads into tracking refs
// (refs/remotes/<remote>/*) plus all tags, WITHOUT touching refs/heads. This is
// the read-only half of a pull: local lines (which live in the SQLite catalogue
// and in refs/heads) are never clobbered, so they may hold uncommitted work that
// reconcile then merges against the fetched remote tips. prune additionally
// removes tracking refs whose remote-side branch is gone (go-git's Prune),
// so a deleted remote branch stops resolving to a stale local tracking ref —
// see fetchTrackingPruned, used by `pr diff` only; plain PullFromRemote/Fetch
// keep prune off so their well-established non-pruning behavior is unchanged.
func (e *Engine) fetchTracking(remoteName string, prune bool) error {
	rem, err := e.git.Remote(remoteName)
	if errors.Is(err, git.ErrRemoteNotFound) {
		return fmt.Errorf("change.fetchTracking: no remote %q", remoteName)
	}
	if err != nil {
		return fmt.Errorf("change.fetchTracking: %w", err)
	}
	auth, err := e.authForRemote(rem)
	if err != nil {
		return fmt.Errorf("change.fetchTracking: %w", err)
	}
	// Snapshot which refs/cairn/push/* pins already exist locally BEFORE the
	// fetch. This repo may have its OWN in-flight PreparePush pins sitting
	// there right now (a concurrent process's push mid-network-phase, on this
	// very clone) — those are legitimate and must survive; only pins that
	// appear as a RESULT of this fetch are foreign and get pruned below. See
	// pruneImportedPushPins.
	before, serr := e.pushPinRefNames()
	if serr != nil {
		return fmt.Errorf("change.fetchTracking: %w", serr)
	}
	// Placed AFTER the before-snapshot (not before it): the delay models the
	// network round-trip itself, which is exactly the window a concurrent
	// PreparePush on this clone can create its own new, legitimate pin in —
	// the TOCTOU pruneImportedPushPins' advertised-on-remote check defends
	// against (see its doc comment).
	if testFetchDelay != nil {
		testFetchDelay()
	}
	err = rem.Fetch(&git.FetchOptions{
		RefSpecs: []config.RefSpec{
			config.RefSpec("+refs/heads/*:refs/remotes/" + remoteName + "/*"),
			"+refs/cairn/*:refs/cairn/*",
		},
		Tags:  git.AllTags,
		Auth:  auth,
		Prune: prune,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("change.fetchTracking: %w", err)
	}
	// Defense-in-depth against the "pin leak" hazard (issue #98 Phase B
	// review): the "+refs/cairn/*:refs/cairn/*" refspec above matches ANY ref
	// whose name starts with "refs/cairn/" — go-git's glob has no "/"
	// boundary awareness — including a remote's refs/cairn/push/<op-id>/*
	// (another op's in-flight pin, a crash orphan, or a previously-polluted
	// remote). pinOutgoingRefs already refuses to re-pin/re-publish anything
	// under that prefix locally, but if one somehow lands on a remote we
	// still must not import and keep it: a self-perpetuating leak (fetch →
	// local refs/cairn/push/* → a later full-fidelity push republishes it
	// forever) starts the moment it's imported. Delete any pin ref this fetch
	// just pulled in that wasn't already there before it ran.
	if perr := e.pruneImportedPushPins(before, rem, auth); perr != nil {
		return fmt.Errorf("change.fetchTracking: %w", perr)
	}
	return nil
}

// pushPinRefNames returns the set of local ref names currently under
// refs/cairn/push/* (see pushPinRefPrefix in push.go).
func (e *Engine) pushPinRefNames() (map[plumbing.ReferenceName]bool, error) {
	iter, err := e.git.References()
	if err != nil {
		return nil, fmt.Errorf("pushPinRefNames: refs: %w", err)
	}
	out := map[plumbing.ReferenceName]bool{}
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), pushPinRefPrefix) {
			out[ref.Name()] = true
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("pushPinRefNames: %w", err)
	}
	return out, nil
}

// pruneImportedPushPins removes a local ref under refs/cairn/push/* only when
// it is BOTH (a) not in before (i.e. it appeared as a result of this fetch)
// AND (b) actually advertised by rem right now — a TOCTOU fix (security
// re-review): before/after alone cannot distinguish "this fetch imported a
// foreign pin from the remote" from "a concurrent PreparePush on THIS clone
// created its own legitimate pin during the fetch's network round-trip" —
// pins are created under wc.lock while a fetch runs under remote.lock, with
// no shared lock between the two, so that race is real. A remote can only
// ever advertise a ref that pinOutgoingRefs itself put there (it refuses to
// pin anything already under this prefix — see pinOutgoingRefs), so a
// locally-created pin is NEVER advertised by the remote and (b) correctly
// lets it survive, while a genuinely-imported foreign pin — by definition
// something the remote advertised — fails (b) into being pruned.
//
// The rem.List call (an extra round-trip) is made LAZILY: only when there is
// at least one (not in before) candidate. The common case — no candidates —
// costs nothing.
func (e *Engine) pruneImportedPushPins(before map[plumbing.ReferenceName]bool, rem *git.Remote, auth transport.AuthMethod) error {
	after, err := e.pushPinRefNames()
	if err != nil {
		return err
	}
	var candidates []plumbing.ReferenceName
	for name := range after {
		if !before[name] {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	advertised, err := remoteAdvertisedRefNames(rem, auth)
	if err != nil {
		return fmt.Errorf("pruneImportedPushPins: %w", err)
	}
	for _, name := range candidates {
		if !advertised[name] {
			continue // created locally during the fetch's network round-trip, not imported — survives
		}
		if err := e.git.Storer.RemoveReference(name); err != nil {
			return fmt.Errorf("pruneImportedPushPins: remove %s: %w", name, err)
		}
	}
	return nil
}

// remoteAdvertisedRefNames lists rem's currently advertised ref names. An
// empty remote (nothing has ever landed) advertises none — go-git reports
// ErrEmptyRemoteRepository rather than an empty list — which is treated as
// "nothing advertised" (a no-op set) rather than an error.
func remoteAdvertisedRefNames(rem *git.Remote, auth transport.AuthMethod) (map[plumbing.ReferenceName]bool, error) {
	refs, err := rem.List(&git.ListOptions{Auth: auth})
	if err != nil {
		if errors.Is(err, transport.ErrEmptyRemoteRepository) {
			return map[plumbing.ReferenceName]bool{}, nil
		}
		return nil, fmt.Errorf("remoteAdvertisedRefNames: list: %w", err)
	}
	out := make(map[plumbing.ReferenceName]bool, len(refs))
	for _, r := range refs {
		if r.Type() == plumbing.HashReference {
			out[r.Name()] = true
		}
	}
	return out, nil
}

// FetchTracking fetches remoteName's heads into tracking refs without touching
// local lines — the read-only "fetch" verb. It is a thin exported wrapper over
// fetchTracking so the worktree layer can offer `cairn fetch` without exposing
// the reconcile half of a pull. Does NOT prune (unchanged behavior).
func (e *Engine) FetchTracking(remoteName string) error {
	return e.fetchTracking(remoteName, false)
}

// FetchTrackingPruned is FetchTracking with go-git's Prune on: a tracking ref
// whose remote-side branch was deleted is removed rather than left stale, so
// a subsequent revision lookup against it fails clearly instead of silently
// resolving to the last-known (now-orphaned) tip. Used by `pr diff`, where a
// stale tracking ref would otherwise diff against deleted content silently.
func (e *Engine) FetchTrackingPruned(remoteName string) error {
	return e.fetchTracking(remoteName, true)
}

// RemoteHeads is the exported form of remoteHeads: short-name → commit-sha for
// every hash reference under refs/remotes/<remoteName>/, skipping the
// remote's HEAD pointer. It is the READ half of a pull's reconcile — the
// worktree layer (issue #98 Phase B) calls it while holding the per-remote
// remote.lock (the same lock a concurrent Fetch/Pull/Push's network phase
// writes tracking/pushed refs under), so this read can never tear a
// concurrent writer's refs.
func (e *Engine) RemoteHeads(remoteName string) (map[string]string, error) {
	return e.remoteHeads(remoteName)
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
	if err := e.fetchTracking(remoteName, false); err != nil {
		return PullSummary{}, fmt.Errorf("change.PullFromRemote: %w", err)
	}
	rheads, err := e.remoteHeads(remoteName)
	if err != nil {
		return PullSummary{}, fmt.Errorf("change.PullFromRemote: %w", err)
	}
	sum, err := e.ReconcileLines(rheads)
	if err != nil {
		return sum, fmt.Errorf("change.PullFromRemote: %w", err)
	}
	return sum, nil
}

// ReconcileLines is the LOCAL half of PullFromRemote (issue #98 Phase B): it
// reconciles every open local line against the already-fetched remote tips in
// rheads (short branch name → commit sha, as returned by RemoteHeads). It does
// no network I/O and writes only the local catalogue (line tips, change
// heads, conflict rows) — the state the worktree layer's wc.lock guards. See
// PullFromRemote for the combined fetch+reconcile convenience wrapper.
func (e *Engine) ReconcileLines(rheads map[string]string) (PullSummary, error) {
	rows, err := e.db.Query(`SELECT id, name, tip_commit FROM line WHERE status='open'`)
	if err != nil {
		return PullSummary{}, fmt.Errorf("change.ReconcileLines: %w", err)
	}
	type lineRow struct {
		id, name, tip string
	}
	var lines []lineRow
	for rows.Next() {
		var lr lineRow
		if err := rows.Scan(&lr.id, &lr.name, &lr.tip); err != nil {
			_ = rows.Close()
			return PullSummary{}, fmt.Errorf("change.ReconcileLines: %w", err)
		}
		lines = append(lines, lr)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return PullSummary{}, fmt.Errorf("change.ReconcileLines: %w", err)
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
			return PullSummary{}, fmt.Errorf("change.ReconcileLines: %w", err)
		}
		sum.Lines = append(sum.Lines, res)
	}
	return sum, nil
}

// PullFromRemoteBranch is PullFromRemote scoped to ONE named open line — the
// engine primitive behind `cairn push --reconcile`'s single-line reconcile.
// It fetches remoteName into tracking refs (the same fetchTracking as
// PullFromRemote — all remote branches land in refs/remotes/<remote>/*, a
// read-only local mirror), but reconciles (and writes catalogue state for)
// ONLY branch. Every other open line's tip/change is left untouched. A
// missing remote branch or missing/closed local line is a no-op, not an
// error (mirrors PullFromRemote silently skipping lines with no remote
// counterpart).
func (e *Engine) PullFromRemoteBranch(remoteName, branch string) (PullSummary, error) {
	// no prune: reconcile semantics match PullFromRemote (see fetchTracking's prune param, added for pr diff)
	if err := e.fetchTracking(remoteName, false); err != nil {
		return PullSummary{}, fmt.Errorf("change.PullFromRemoteBranch: %w", err)
	}
	rheads, err := e.remoteHeads(remoteName)
	if err != nil {
		return PullSummary{}, fmt.Errorf("change.PullFromRemoteBranch: %w", err)
	}
	sum, err := e.ReconcileBranch(rheads, branch)
	if err != nil {
		return sum, fmt.Errorf("change.PullFromRemoteBranch: %w", err)
	}
	return sum, nil
}

// ReconcileBranch is the LOCAL half of PullFromRemoteBranch (issue #98 Phase
// B): it reconciles ONLY branch against the already-fetched remote tips in
// rheads (as returned by RemoteHeads). It does no network I/O. Every other
// open line's tip/change is left untouched. A missing remote branch or
// missing/closed local line is a no-op, not an error.
func (e *Engine) ReconcileBranch(rheads map[string]string, branch string) (PullSummary, error) {
	r, ok := rheads[branch]
	if !ok {
		return PullSummary{}, nil // remote has no such branch; nothing to reconcile
	}

	var id, tip string
	err := e.db.QueryRow(
		`SELECT id, tip_commit FROM line WHERE name=? AND status='open'`, branch,
	).Scan(&id, &tip)
	if errors.Is(err, sql.ErrNoRows) {
		return PullSummary{}, nil // no open local line by that name; nothing to reconcile
	}
	if err != nil {
		return PullSummary{}, fmt.Errorf("change.ReconcileBranch: %w", err)
	}

	res, err := e.reconcileLine(id, branch, tip, r)
	if err != nil {
		return PullSummary{}, fmt.Errorf("change.ReconcileBranch: %w", err)
	}
	return PullSummary{Lines: []LineResult{res}}, nil
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
	// Pick the line's open WORKING change (sealed=0). A sealed change keeps
	// status='open' but is frozen — its head must not absorb a merge — so it is
	// excluded; the fresh working change a seal opens is the live one a merge
	// conflict belongs on (where `resolve` looks).
	err := e.db.QueryRow(
		`SELECT id, head_commit FROM change WHERE line_id=? AND status='open' AND sealed=0 ORDER BY updated_at DESC LIMIT 1`,
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

	if l == "" {
		// #116: a brand-new local line has no commits at all (tip_commit=="" and
		// no snapshotted open change), so l=="" here — and since l only falls
		// back to lineTip when changeHead=="", the open change (if any) is
		// necessarily headless too. mergeBase("", r) also returns "" (gitobj.go
		// treats an empty input as "no base"), which would otherwise satisfy
		// neither `base == r` nor `base == l && l != ""` below and fall into the
		// diverged default branch — merging against a "" (zero-hash) local tree,
		// which errors "object not found". There is nothing local to preserve,
		// so treat it as a degenerate fast-forward: adopt r wholesale. Pass ""
		// as the change id so a headless change stays headless (#103), later
		// re-snapshotting with parent = the new line tip.
		if err := e.applyHead("", lineID, r, 0); err != nil {
			return LineResult{}, err
		}
		return LineResult{Line: lineName, Status: "fast-forward"}, nil
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
		// change WITH a snapshot, advance its head too; a NEVER-SNAPSHOTTED open
		// change stays headless (#103 second symptom: adopting r as its head made
		// the next folder sync AMEND onto r's parent — amends keep the working
		// commit's parent — presenting the remote commit's own changes as local
		// work). A headless change re-snapshots with parent = the new line tip.
		ffChange := changeID
		if changeHead == "" {
			ffChange = ""
		}
		if err := e.applyHead(ffChange, lineID, r, 0); err != nil {
			return LineResult{}, err
		}
		return LineResult{Line: lineName, Status: "fast-forward"}, nil

	default:
		// Diverged: local and remote both moved past the merge-base. Prefer a
		// REBASE — replay the local commits onto the remote tip R for LINEAR
		// history (no "merge remote-tracking" commit) — but ONLY when the
		// divergence is clean and a real working change exists to land on. A
		// conflicting or ill-defined divergence keeps the 2-parent merge, whose
		// conflicts stay resolvable on the working change exactly as before
		// (so `resolve` keeps working; nothing is lost silently).
		origHasChange := hasChange
		if !hasChange {
			author := e.idName
			if author == "" {
				author = syncAuthor
			}
			ch, cerr := e.CreateChange(lineID, author)
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

		// Clean divergence with a pre-existing working change → REBASE for LINEAR
		// history. mergeTrees is side-effect-free (it returns conflicts as data
		// and only writes a content-addressed tree), so we discard `merged` and
		// replay the local commits onto R instead. rewriteChainOnto reuses the
		// proven per-step 3-way replay, working-change rebase, and single-tx
		// catalogue update. A clean 3-way here means the per-step replay is clean
		// too (the local edits don't touch the remote's regions).
		if len(conflicts) == 0 && base != "" && origHasChange {
			chain, cerr := e.sealedChainAbove(lineID, base)
			if cerr != nil {
				return LineResult{}, cerr
			}
			rconf, cerr := e.rewriteChainOnto(lineID, r, chain, chain)
			if cerr != nil {
				return LineResult{}, cerr
			}
			return LineResult{Line: lineName, Status: "rebased", Conflicts: len(rconf)}, nil
		}

		// Conflicting or ill-defined divergence → a 2-parent merge commit [L, R].
		// When the local side has no commit yet (l==""), parent only on R so the
		// merge is still a real descendant of the remote tip and remains pushable.
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
