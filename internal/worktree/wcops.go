package worktree

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/gitpatch"
)

// Working-copy mutation verbs (git parity): restore, reset, revert, partial
// commit / amend, rm, mv, clean, apply / am, and the stash additions.
//
// They share one shape (opCtx / beginOp): take the working-copy lock, snapshot
// the folder into the open working change so the change IS what is on disk,
// compute the result as a TREE in the engine (one transaction, one op-log
// entry — so `cairn undo` reverts it), then bring the folder to the new tip
// through the ordinary materialize. Nothing here edits files directly except
// Clean, which removes paths cairn never tracked and so has nothing to record.

// opCtx is one branch's state at the start of a working-copy verb, after the
// folder has been snapshotted.
type opCtx struct {
	branch string
	entry  Entry
	line   change.Line
	dir    string
	// synced is the snapshot scan's per-path fingerprints, handed to the
	// materialize that follows so an untouched path costs one Lstat.
	synced map[string]wcCacheEntry
	// work is the open working change's tree; top is the sealed tip commit
	// ("" when the line has none) and base its tree.
	work map[string]change.TreeEntry
	top  string
	base map[string]change.TreeEntry
}

// beginOp snapshots branch's folder and loads the state a verb computes on.
// The caller must hold the working-copy lock.
func (r *Repo) beginOp(branch, verb string) (*opCtx, error) {
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return nil, fmt.Errorf("worktree.%s: branch %q is not expressed", verb, branch)
	}
	if active, err := r.eng.BisectActive(); err != nil {
		return nil, fmt.Errorf("worktree.%s: %w", verb, err)
	} else if active {
		return nil, fmt.Errorf("worktree.%s: a bisect is in progress — 'cairn bisect reset' first", verb)
	}
	synced, _, err := r.syncBranch(branch, entry)
	if err != nil {
		return nil, err
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return nil, fmt.Errorf("worktree.%s: %w", verb, err)
	}
	// The engine verbs act on the line's open change; wc.json must agree on
	// which one that is, or the folder and the catalogue would part ways.
	open, err := r.eng.OpenChangeForLine(line.ID)
	if err != nil {
		return nil, fmt.Errorf("worktree.%s: %w", verb, err)
	}
	if open.ID != entry.ChangeID {
		return nil, fmt.Errorf("worktree.%s: branch %q: the working copy and the catalogue disagree on the open change — run 'cairn status' and retry", verb, branch)
	}
	c := &opCtx{branch: branch, entry: entry, line: line, dir: filepath.Join(r.root, entry.Path), synced: synced}
	c.work = map[string]change.TreeEntry{}
	if open.HeadCommit != "" {
		if c.work, err = r.eng.FilesMeta(open.HeadCommit); err != nil {
			return nil, fmt.Errorf("worktree.%s: %w", verb, err)
		}
	}
	if c.top, err = r.eng.SealedTip(line.ID); err != nil {
		return nil, fmt.Errorf("worktree.%s: %w", verb, err)
	}
	c.base = map[string]change.TreeEntry{}
	if c.top != "" {
		if c.base, err = r.eng.FilesMeta(c.top); err != nil {
			return nil, fmt.Errorf("worktree.%s: %w", verb, err)
		}
	}
	return c, nil
}

// refreshFolder brings the branch folder to the line's current tip after a
// verb moved it, reusing the scan the verb started with.
func (r *Repo) refreshFolder(c *opCtx) error {
	line, err := r.eng.LineByName(c.branch)
	if err != nil {
		return fmt.Errorf("worktree.refreshFolder: %w", err)
	}
	if line.TipCommit == "" {
		return nil
	}
	fps, err := MaterializeSeededFrom(r.eng, r.cacheDir(), line.TipCommit, c.dir, c.synced, c.synced)
	if err != nil {
		return fmt.Errorf("worktree.refreshFolder: %w", err)
	}
	return r.seedWCCache(c.branch, fps)
}

// openConflictPaths lists the open conflicts on the branch's working change.
func (r *Repo) openConflictPaths(c *opCtx) ([]string, error) {
	open, err := r.eng.Conflicts(c.entry.ChangeID)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(open))
	for i, cf := range open {
		paths[i] = cf.Path
	}
	return paths, nil
}

// refuseOpenConflicts is Commit's unresolved-conflict refusal for the other
// sealing verbs.
func (r *Repo) refuseOpenConflicts(c *opCtx, verb string) error {
	paths, err := r.openConflictPaths(c)
	if err != nil {
		return fmt.Errorf("worktree.%s: %w", verb, err)
	}
	if len(paths) > 0 {
		return fmt.Errorf(
			"worktree.%s: %d unresolved conflict(s) in: %s — edit out the <<<<<<< markers, run 'cairn resolve %s <path>' for each, then commit",
			verb, len(paths), strings.Join(paths, ", "), c.branch)
	}
	return nil
}

// ── pathspecs ────────────────────────────────────────────────────────────────

// MatchPathspec reports whether the tree path p is selected by spec: the whole
// tree ("" or "."), the path itself, a directory above it, or a glob. A glob
// with no "/" matches the file name at any depth, as git's pathspecs do.
func MatchPathspec(p, spec string) bool {
	spec = strings.TrimSuffix(spec, "/")
	if spec == "" || spec == "." {
		return true
	}
	if p == spec || strings.HasPrefix(p, spec+"/") {
		return true
	}
	if strings.ContainsAny(spec, "*?[") {
		if ok, _ := path.Match(spec, p); ok {
			return true
		}
		if !strings.Contains(spec, "/") {
			if ok, _ := path.Match(spec, path.Base(p)); ok {
				return true
			}
		}
	}
	return false
}

func matchAny(p string, specs []string) bool {
	for _, s := range specs {
		if MatchPathspec(p, s) {
			return true
		}
	}
	return false
}

// unmatchedSpec returns the first spec that selects no path in any of sets.
func unmatchedSpec(specs []string, sets ...map[string]change.TreeEntry) (string, bool) {
	for _, s := range specs {
		hit := false
		for _, set := range sets {
			for p := range set {
				if MatchPathspec(p, s) {
					hit = true
					break
				}
			}
			if hit {
				break
			}
		}
		if !hit {
			return s, true
		}
	}
	return "", false
}

func sortedKeys(m map[string]change.TreeEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func cloneEntries(m map[string]change.TreeEntry) map[string]change.TreeEntry {
	out := make(map[string]change.TreeEntry, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ── revisions ────────────────────────────────────────────────────────────────

// ResolveRev resolves a commit argument for the working-copy verbs through the
// shared revision resolver, with HEAD/@ meaning branch's sealed tip.
func (r *Repo) ResolveRev(branch, rev string) (string, error) {
	sha, err := r.eng.ResolveCommitAt(branch, rev)
	if err != nil {
		return "", fmt.Errorf("unknown revision %q: %w", rev, err)
	}
	return sha, nil
}

// ResolveRange expands a cherry-pick/revert argument into commits, oldest
// first: a single revision, or `A..B` — the commits after A up to and
// including B along B's first-parent history.
func (r *Repo) ResolveRange(branch, spec string) ([]string, error) {
	a, b, isRange := strings.Cut(spec, "..")
	if !isRange {
		sha, err := r.ResolveRev(branch, spec)
		if err != nil {
			return nil, err
		}
		return []string{sha}, nil
	}
	if strings.HasPrefix(b, ".") {
		return nil, fmt.Errorf("%q: three-dot ranges are not supported here; use A..B", spec)
	}
	if a == "" || b == "" {
		return nil, fmt.Errorf("%q: a range needs both ends (A..B)", spec)
	}
	from, err := r.ResolveRev(branch, a)
	if err != nil {
		return nil, err
	}
	to, err := r.ResolveRev(branch, b)
	if err != nil {
		return nil, err
	}
	if anc, aerr := r.eng.IsAncestor(from, to); aerr != nil {
		return nil, aerr
	} else if !anc {
		return nil, fmt.Errorf("%q: %s is not an ancestor of %s", spec, a, b)
	}
	var out []string
	const walkCap = 10000
	for cur := to; cur != from; {
		if cur == "" || len(out) >= walkCap {
			return nil, fmt.Errorf("%q: %s is not a first-parent ancestor of %s", spec, a, b)
		}
		out = append(out, cur)
		// DeltaBase, not the literal first parent: a seal can emit two commits
		// for one change (stamped + merge-forward), and picking the top one
		// already covers the change.
		if cur, err = r.eng.DeltaBase(cur); err != nil {
			return nil, err
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ── pending message ──────────────────────────────────────────────────────────

// A revert / cherry-pick that stops on a conflict, or runs with --no-commit,
// leaves its message for the `cairn commit` that finishes it (git's MERGE_MSG).
// It lives beside wc.json, one file per branch, and is cleared by the commit
// that uses it, by `reset --hard`, and by undo.

func (r *Repo) pendingDir() string { return filepath.Join(r.root, ".cairn", "pending-msg") }

func (r *Repo) pendingPath(branch string) string {
	return filepath.Join(r.pendingDir(), FolderName(branch))
}

// PendingMessage returns the message a stopped revert/cherry-pick left for the
// next commit on branch, or "".
func (r *Repo) PendingMessage(branch string) string {
	b, err := os.ReadFile(r.pendingPath(branch))
	if err != nil {
		return ""
	}
	return string(b)
}

// SetPendingMessage records msg for the next commit on branch.
func (r *Repo) SetPendingMessage(branch, msg string) error {
	if err := os.MkdirAll(r.pendingDir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(r.pendingPath(branch), []byte(msg), 0o644)
}

// ClearPendingMessage forgets branch's pending message.
func (r *Repo) ClearPendingMessage(branch string) { _ = os.Remove(r.pendingPath(branch)) }

// ── restore / reset ──────────────────────────────────────────────────────────

// Restore discards the working change's edits to the paths selected by specs,
// restoring them from source (a commit; "" = the line's sealed tip). git
// semantics: a deleted file comes back, and a path the source does not have is
// removed. It returns the paths it changed.
func (r *Repo) Restore(branch, source string, specs []string) ([]string, error) {
	unlock, err := r.lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	c, err := r.beginOp(branch, "Restore")
	if err != nil {
		return nil, err
	}
	src := c.base
	if source == "" && c.top == "" && len(specs) > 0 {
		// Nothing sealed to restore from: "the sealed state" is empty, and
		// restoring to it would delete every un-sealed file. git refuses too.
		return nil, fmt.Errorf("pathspec %q did not match any file(s) known to cairn (%q has no sealed commit to restore from; pass --source <commit>)", specs[0], branch)
	}
	if source != "" {
		sha, rerr := r.ResolveRev(branch, source)
		if rerr != nil {
			return nil, rerr
		}
		if src, err = r.eng.FilesMeta(sha); err != nil {
			return nil, fmt.Errorf("worktree.Restore: %w", err)
		}
	}
	if s, bad := unmatchedSpec(specs, c.work, src); bad {
		return nil, fmt.Errorf("pathspec %q did not match any file(s) known to cairn", s)
	}
	conflicted, err := r.openConflictPaths(c)
	if err != nil {
		return nil, fmt.Errorf("worktree.Restore: %w", err)
	}
	for _, p := range conflicted {
		if matchAny(p, specs) {
			return nil, fmt.Errorf("path %q has an unresolved conflict — settle it with 'cairn resolve %s %s', or discard the whole working change with 'cairn reset --hard'", p, branch, p)
		}
	}
	next := cloneEntries(c.work)
	var changed []string
	seen := map[string]struct{}{}
	visit := func(p string) {
		if _, dup := seen[p]; dup || !matchAny(p, specs) {
			return
		}
		seen[p] = struct{}{}
		cur, inWork := c.work[p]
		want, inSrc := src[p]
		// A gitlink is another repository's commit: never cairn's to rewrite.
		if (inWork && cur.Mode == change.ModeGitlink) || (inSrc && want.Mode == change.ModeGitlink) {
			return
		}
		switch {
		case inSrc && (!inWork || cur != want):
			next[p] = want
			changed = append(changed, p)
		case !inSrc && inWork:
			delete(next, p)
			changed = append(changed, p)
		}
	}
	for p := range c.work {
		visit(p)
	}
	for p := range src {
		visit(p)
	}
	sort.Strings(changed)
	if len(changed) == 0 {
		return nil, nil
	}
	tree, err := r.eng.WriteTree(next)
	if err != nil {
		return nil, fmt.Errorf("worktree.Restore: %w", err)
	}
	if _, err := r.eng.RewriteWorking(c.line.ID, tree, change.OpRestore, false); err != nil {
		return nil, fmt.Errorf("worktree.Restore: %w", err)
	}
	return changed, r.refreshFolder(c)
}

// ResetInfo reports a Reset: what moved, and whether the remote still holds
// commits the line no longer has (so the next push must be forced).
type ResetInfo struct {
	change.ResetResult
	// ForceRemote is the remote whose branch now holds commits this line
	// dropped ("" when none) — the next push to it needs --force.
	ForceRemote string
}

// Reset moves branch's sealed tip to target (git `reset <rev>`); target ""
// means the current sealed tip, so Reset(branch, "", true) is `reset --hard`:
// discard the whole working change. See change.ResetLine for the safety rule.
func (r *Repo) Reset(branch, target string, hard bool) (ResetInfo, error) {
	unlock, err := r.lockState()
	if err != nil {
		return ResetInfo{}, err
	}
	defer unlock()
	c, err := r.beginOp(branch, "Reset")
	if err != nil {
		return ResetInfo{}, err
	}
	sha := c.top
	if target != "" {
		if sha, err = r.ResolveRev(branch, target); err != nil {
			return ResetInfo{}, err
		}
	}
	res, err := r.eng.ResetLine(c.line.ID, sha, hard)
	if err != nil {
		return ResetInfo{}, err
	}
	if hard {
		r.ClearPendingMessage(branch)
		if err := r.refreshFolder(c); err != nil {
			return ResetInfo{}, err
		}
	}
	info := ResetInfo{ResetResult: res}
	if res.Dropped > 0 {
		info.ForceRemote = r.remoteHoldsDropped(branch)
	}
	return info, nil
}

// remoteHoldsDropped names the remote whose copy of branch has commits the
// line itself no longer has (behind or diverged), or "".
func (r *Repo) remoteHoldsDropped(branch string) string {
	remote, states, err := r.remoteStates()
	if err != nil || remote == "" {
		return ""
	}
	if st, ok := states[branch]; ok && (st.Kind == "behind" || st.Kind == "diverged") {
		return remote
	}
	return ""
}

// RemoteStateOf returns branch's relationship to its remote branch, as of the
// last fetch or push. ok is false when the repo has no remote.
func (r *Repo) RemoteStateOf(branch string) (remote string, st change.RemoteState, ok bool, err error) {
	remote, states, err := r.remoteStates()
	if err != nil {
		return "", change.RemoteState{}, false, err
	}
	st, ok = states[branch]
	return remote, st, ok && remote != "", nil
}

// ── revert / cherry-pick ─────────────────────────────────────────────────────

// PickResult is the outcome of a Revert or a cherry-pick.
type PickResult struct {
	// Commit is the new sealed commit ("" when the delta was left in the
	// working change: --no-commit, or it did not apply cleanly).
	Commit string
	// Conflicts are open conflicts the verb left on the working change.
	Conflicts []change.Conflict
	// Message is the message the commit carries, or that a later
	// `cairn commit` will pick up when Commit is "".
	Message string
}

// Revert applies the inverse of commit to branch as a new sealed commit
// (`Revert "<subject>"`). mainline selects the parent a MERGE commit is
// reverted against (git's -m, 1-based; 0 = not given). With noCommit, or when
// the inverse does not apply cleanly, it lands in the working change instead —
// conflicts recorded as data — and the message is kept for the commit that
// finishes it.
func (r *Repo) Revert(branch, commit string, mainline int, noCommit bool) (PickResult, error) {
	unlock, err := r.lockState()
	if err != nil {
		return PickResult{}, err
	}
	defer unlock()
	c, err := r.beginOp(branch, "Revert")
	if err != nil {
		return PickResult{}, err
	}
	if err := r.refuseOpenConflicts(c, "Revert"); err != nil {
		return PickResult{}, err
	}
	sha, err := r.ResolveRev(branch, commit)
	if err != nil {
		return PickResult{}, err
	}
	if working, werr := r.eng.IsWorkingHead(sha); werr == nil && working {
		return PickResult{}, fmt.Errorf("revert: %s is the open working change, not a sealed commit — 'cairn reset --hard' discards it", shortID(sha))
	}
	info, err := r.eng.CommitInfoOf(sha)
	if err != nil {
		return PickResult{}, fmt.Errorf("worktree.Revert: %w", err)
	}
	own, isMerge, err := r.eng.OwnCommit(sha)
	if err != nil {
		return PickResult{}, fmt.Errorf("worktree.Revert: %w", err)
	}
	// A merge is decided by the commit's own parent count. OwnCommit maps a
	// cairn adoption merge to the stamped commit beneath it (so a plain revert
	// takes back the operator's change), but an explicit -m names a parent of
	// THE merge commit itself, as in git.
	if mainline != 0 && !isMerge {
		if ps, perr := r.eng.Parents(sha); perr == nil && len(ps) > 1 {
			own, isMerge = sha, true
		}
	}
	// git writes the subject between plain quotes, never escaped.
	msg := fmt.Sprintf("Revert \"%s\"\n\nThis reverts commit %s.", info.Subject, sha)
	var parent string
	switch {
	case isMerge:
		parents, perr := r.eng.Parents(own)
		if perr != nil {
			return PickResult{}, fmt.Errorf("worktree.Revert: %w", perr)
		}
		if mainline == 0 {
			return PickResult{}, fmt.Errorf("revert: commit %s is a merge but no -m option was given", shortID(sha))
		}
		if mainline < 1 || mainline > len(parents) {
			return PickResult{}, fmt.Errorf("revert: commit %s does not have parent %d", shortID(sha), mainline)
		}
		parent = parents[mainline-1]
		msg = fmt.Sprintf("Revert \"%s\"\n\nThis reverts commit %s, reversing\nchanges made to %s.", info.Subject, sha, parent)
	case mainline != 0:
		return PickResult{}, fmt.Errorf("revert: mainline was specified but commit %s is not a merge", shortID(sha))
	default:
		if parent, err = r.eng.DeltaBase(own); err != nil {
			return PickResult{}, fmt.Errorf("worktree.Revert: %w", err)
		}
	}
	// Inverting: the commit's own tree is the base, its parent's the target.
	baseTree, err := r.eng.TreeOf(own)
	if err != nil {
		return PickResult{}, fmt.Errorf("worktree.Revert: %w", err)
	}
	theirsTree, err := r.eng.TreeOf(parent)
	if err != nil {
		return PickResult{}, fmt.Errorf("worktree.Revert: %w", err)
	}
	res := PickResult{Message: msg}
	if !noCommit {
		ar, aerr := r.eng.AppendSealed(c.line.ID, change.OpRevert, change.AppendSpec{Message: msg, Base: baseTree, Theirs: theirsTree})
		switch {
		case aerr == nil:
			res.Commit, res.Conflicts = ar.Commit, ar.Conflicts
			return res, r.refreshFolder(c)
		case errors.Is(aerr, change.ErrNothingToCommit):
			return PickResult{}, fmt.Errorf("revert: nothing to revert — %q no longer carries what %s changed", branch, shortID(sha))
		case !errors.Is(aerr, change.ErrWouldConflict):
			return PickResult{}, aerr
		}
		// Does not apply cleanly on the sealed tip: fall through and land it
		// in the working change, where the conflict can be resolved.
	}
	cf, _, err := r.eng.MergeIntoWorking(c.line.ID, baseTree, theirsTree, change.OpRevert)
	if err != nil {
		return PickResult{}, fmt.Errorf("worktree.Revert: %w", err)
	}
	res.Conflicts = cf
	if err := r.SetPendingMessage(branch, msg); err != nil {
		return PickResult{}, fmt.Errorf("worktree.Revert: %w", err)
	}
	return res, r.refreshFolder(c)
}

// PickOpts are cherry-pick's git flags.
type PickOpts struct {
	NoCommit bool // -n: leave the delta in the working change
	AppendX  bool // -x: append "(cherry picked from commit <sha>)"
}

// CherryPickOpts is CherryPick with git's -n / -x.
func (r *Repo) CherryPickOpts(branch, commit string, opts PickOpts) (PickResult, error) {
	unlock, err := r.lockState()
	if err != nil {
		return PickResult{}, err
	}
	defer unlock()
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
	}
	commit, err = r.eng.ResolveCommit(commit)
	if err != nil {
		return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
	}
	transform := func(msg string) string {
		if opts.AppendX {
			return strings.TrimRight(msg, "\n") + "\n\n(cherry picked from commit " + commit + ")"
		}
		return msg
	}
	entry, expressed := r.st.Expressed[branch]
	if opts.NoCommit {
		if !expressed {
			return PickResult{}, fmt.Errorf("worktree.CherryPick: branch %q is not expressed", branch)
		}
		info, ierr := r.eng.CommitInfoOf(commit)
		if ierr != nil {
			return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", ierr)
		}
		parent, perr := r.eng.DeltaBase(commit)
		if perr != nil {
			return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", perr)
		}
		baseTree, terr := r.eng.TreeOf(parent)
		if terr != nil {
			return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", terr)
		}
		theirsTree, terr := r.eng.TreeOf(commit)
		if terr != nil {
			return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", terr)
		}
		cf, _, merr := r.eng.MergeIntoWorking(line.ID, baseTree, theirsTree, change.OpPickNoCommit)
		if merr != nil {
			return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", merr)
		}
		msg := transform(info.Message)
		if err := r.SetPendingMessage(branch, msg); err != nil {
			return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
		}
		if err := r.rematerialize(branch, entry); err != nil {
			return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
		}
		return PickResult{Conflicts: cf, Message: msg}, nil
	}
	newID, conflicts, err := r.eng.CherryPickWith(commit, line.ID, transform)
	if err != nil {
		return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
	}
	// Pick-level conflicts are recorded on the NEW sealed change the pick mints,
	// but resolve/status operate on the working change W (entry.ChangeID). Mirror
	// Commit: reassign the pick conflicts from newID onto W so they're reachable.
	// (W-rebase conflicts are already on W, so this consolidates ALL conflicts.)
	if len(conflicts) > 0 && expressed {
		if err := r.eng.ReassignConflicts(newID, entry.ChangeID); err != nil {
			return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
		}
		// The pick is sealed with the conflict recorded; the resolution is a
		// follow-up commit. Leave it a message, so `cairn commit` /
		// `cherry-pick --continue` need none.
		_ = r.SetPendingMessage(branch, "Resolve conflicts in cherry-pick of "+shortID(commit))
	}
	if expressed {
		if err := r.rematerialize(branch, entry); err != nil {
			return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
		}
	}
	line, err = r.eng.LineByName(branch)
	if err != nil {
		return PickResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
	}
	return PickResult{Commit: line.TipCommit, Conflicts: conflicts}, nil
}

// AbortPick rewinds branch to where it stood before a revert or `cherry-pick
// -n` that is still the latest thing that happened to it — the most recent
// operation, or followed only by working snapshots (an attempted resolution).
// It refuses when anything else has happened since. Only this branch moves;
// the open conflicts the pick left are dropped with it.
func (r *Repo) AbortPick(branch, verb string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return fmt.Errorf("worktree.AbortPick: branch %q is not expressed", branch)
	}
	ops, err := r.eng.OperationLog()
	if err != nil {
		return err
	}
	i := len(ops) - 1
	for i >= 0 && ops[i].OpType == "snapshot" {
		i--
	}
	if i < 0 || (ops[i].OpType != change.OpRevert && ops[i].OpType != change.OpPickNoCommit) ||
		ops[i].ViewBefore[branch] == ops[i].ViewAfter[branch] {
		return fmt.Errorf("%s --abort: the last operation on %q is not a cherry-pick or revert — 'cairn oplog' lists what happened and 'cairn undo' rewinds one step", verb, branch)
	}
	if err := r.eng.RewindLine(branch, ops[i].ViewBefore[branch], entry.ChangeID); err != nil {
		return err
	}
	r.ClearPendingMessage(branch)
	return r.reconcileRewound(branch, entry)
}

// ── commit additions ─────────────────────────────────────────────────────────

// CommitOpts are the git commit flags that reach the seal.
type CommitOpts struct {
	// Author overrides the commit's author (git --author / --date); nil keeps
	// the configured identity. The committer is always the configured identity.
	Author *change.Signature
	// AllowEmpty seals even when the working change holds no edits (git
	// commit --allow-empty). Without it an empty working change is refused
	// with ErrNothingToCommit, as git does.
	AllowEmpty bool
}

// CommitWith is Commit with an explicit author/date, refusing a seal that
// would record nothing unless opts.AllowEmpty.
func (r *Repo) CommitWith(branch, message string, opts CommitOpts) (change.CommitResult, error) {
	if !opts.AllowEmpty {
		if err := r.refuseNothingToCommit(branch); err != nil {
			return change.CommitResult{}, err
		}
	}
	if opts.Author != nil {
		r.eng.SetAuthorOverride(opts.Author)
		defer r.eng.SetAuthorOverride(nil)
	}
	res, err := r.Commit(branch, message)
	if err == nil {
		r.ClearPendingMessage(branch)
	}
	return res, err
}

// CommitPaths seals ONLY the paths selected by specs — new, modified and
// deleted files alike, directories recursively — as a new commit on branch.
// Every other edit stays, untouched, in the open working change. This is
// cairn's answer to the staging area: instead of staging what goes in, name it.
//
// Unlike a full Commit it does not adopt the parent line (it appends one
// commit and re-parents the working change, like cherry-pick); the next full
// commit reconciles as usual.
func (r *Repo) CommitPaths(branch, message string, specs []string, opts CommitOpts) (string, error) {
	unlock, err := r.lockState()
	if err != nil {
		return "", err
	}
	defer unlock()
	c, err := r.beginOp(branch, "Commit")
	if err != nil {
		return "", err
	}
	if err := r.refuseOpenConflicts(c, "Commit"); err != nil {
		return "", err
	}
	if s, bad := unmatchedSpec(specs, c.work, c.base); bad {
		return "", fmt.Errorf("pathspec %q did not match any file(s) known to cairn", s)
	}
	partial := cloneEntries(c.base)
	for p, e := range c.work {
		if matchAny(p, specs) {
			partial[p] = e
		}
	}
	for p := range c.base {
		if _, still := c.work[p]; !still && matchAny(p, specs) {
			delete(partial, p)
		}
	}
	tree, err := r.eng.WriteTree(partial)
	if err != nil {
		return "", fmt.Errorf("worktree.Commit: %w", err)
	}
	res, err := r.eng.AppendSealed(c.line.ID, change.OpCommitPaths, change.AppendSpec{Message: message, Tree: tree, Author: opts.Author})
	if err != nil {
		if errors.Is(err, change.ErrNothingToCommit) {
			return "", fmt.Errorf("nothing to commit: no changes under %s", strings.Join(specs, ", "))
		}
		return "", err
	}
	// The working tree is unchanged (it already held these edits), so the
	// folder needs no rewrite.
	return res.Commit, nil
}

// Amend folds the working change into branch's top sealed commit (git `commit
// --amend`). message nil keeps the commit's message. forceRemote names the
// remote that already holds the commit being replaced ("" when none): the next
// push to it needs --force.
func (r *Repo) Amend(branch string, message *string, opts CommitOpts, resetAuthor bool) (sealed, forceRemote string, err error) {
	unlock, err := r.lockState()
	if err != nil {
		return "", "", err
	}
	defer unlock()
	c, err := r.beginOp(branch, "Amend")
	if err != nil {
		return "", "", err
	}
	if err := r.refuseOpenConflicts(c, "Amend"); err != nil {
		return "", "", err
	}
	// Was the commit being replaced already on the remote?
	pushed := ""
	if remote, states, serr := r.remoteStates(); serr == nil && remote != "" {
		if st, ok := states[branch]; ok && (st.Kind == "pushed" || st.Kind == "behind") {
			pushed = remote
		}
	}
	sealed, err = r.eng.AmendTop(c.line.ID, message, opts.Author, resetAuthor)
	if err != nil {
		return "", "", err
	}
	r.ClearPendingMessage(branch)
	return sealed, pushed, nil
}

// TopMessage returns the message of branch's top sealed commit ("" if none) —
// what `commit --amend` starts the editor with.
func (r *Repo) TopMessage(branch string) (string, error) {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return "", err
	}
	top, err := r.eng.SealedTip(line.ID)
	if err != nil || top == "" {
		return "", err
	}
	info, err := r.eng.CommitInfoOf(top)
	if err != nil {
		return "", err
	}
	return info.Message, nil
}

// ── rm / mv ──────────────────────────────────────────────────────────────────

// Rm removes the paths selected by specs from branch's working change and
// folder (git rm). A directory needs recursive; a path whose content differs
// from the sealed tip needs force, so un-sealed work is never dropped by
// accident. With dryRun nothing changes. It returns the removed paths.
func (r *Repo) Rm(branch string, specs []string, recursive, force, ignoreUnmatch, dryRun bool) ([]string, error) {
	unlock, err := r.lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	c, err := r.beginOp(branch, "Rm")
	if err != nil {
		return nil, err
	}
	next := cloneEntries(c.work)
	var removed []string
	for _, spec := range specs {
		clean := strings.TrimSuffix(spec, "/")
		var hits []string
		for p, e := range c.work {
			if e.Mode != change.ModeGitlink && MatchPathspec(p, spec) {
				hits = append(hits, p)
			}
		}
		if len(hits) == 0 {
			if ignoreUnmatch {
				continue
			}
			return nil, fmt.Errorf("pathspec %q did not match any files", spec)
		}
		if _, isFile := c.work[clean]; !isFile && !recursive && !strings.ContainsAny(spec, "*?[") {
			return nil, fmt.Errorf("not removing %q recursively without -r", spec)
		}
		for _, p := range hits {
			if _, gone := next[p]; !gone {
				continue
			}
			if base, inBase := c.base[p]; !force && (!inBase || base != c.work[p]) {
				return nil, fmt.Errorf("%q has un-sealed changes — commit them first, or pass -f to remove it anyway", p)
			}
			delete(next, p)
			removed = append(removed, p)
		}
	}
	sort.Strings(removed)
	if dryRun || len(removed) == 0 {
		return removed, nil
	}
	tree, err := r.eng.WriteTree(next)
	if err != nil {
		return nil, fmt.Errorf("worktree.Rm: %w", err)
	}
	if _, err := r.eng.RewriteWorking(c.line.ID, tree, change.OpRm, false); err != nil {
		return nil, fmt.Errorf("worktree.Rm: %w", err)
	}
	return removed, r.refreshFolder(c)
}

// Move is one path renamed by Mv.
type Move struct{ From, To string }

// Mv renames files or directories inside branch's working change and folder
// (git mv). With several sources, or when dst is an existing directory, each
// source moves INTO dst. An existing destination file needs force. Only paths
// in the working change move: an ignored file inside a moved directory stays
// where it is.
func (r *Repo) Mv(branch string, srcs []string, dst string, force, dryRun bool) ([]Move, error) {
	unlock, err := r.lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	c, err := r.beginOp(branch, "Mv")
	if err != nil {
		return nil, err
	}
	dst = strings.TrimSuffix(dst, "/")
	isDir := func(p string) bool {
		if p == "" || p == "." {
			return true
		}
		for k := range c.work {
			if strings.HasPrefix(k, p+"/") {
				return true
			}
		}
		fi, serr := os.Stat(filepath.Join(c.dir, filepath.FromSlash(p)))
		return serr == nil && fi.IsDir()
	}
	dstIsDir := isDir(dst)
	if len(srcs) > 1 && !dstIsDir {
		return nil, fmt.Errorf("destination %q is not a directory", dst)
	}
	next := cloneEntries(c.work)
	var moves []Move
	for _, src := range srcs {
		src = strings.TrimSuffix(src, "/")
		target := dst
		if dstIsDir {
			target = path.Join(dst, path.Base(src))
		}
		if src == target {
			return nil, fmt.Errorf("can not move %q onto itself", src)
		}
		if strings.HasPrefix(target, src+"/") {
			return nil, fmt.Errorf("can not move directory %q into itself (%q)", src, target)
		}
		var hits []string
		if e, ok := c.work[src]; ok {
			if e.Mode == change.ModeGitlink {
				return nil, fmt.Errorf("%q is a submodule; cairn cannot move it", src)
			}
			hits = []string{src}
		} else {
			for p, e := range c.work {
				if strings.HasPrefix(p, src+"/") && e.Mode != change.ModeGitlink {
					hits = append(hits, p)
				}
			}
		}
		if len(hits) == 0 {
			return nil, fmt.Errorf("bad source, source=%s, destination=%s", src, target)
		}
		sort.Strings(hits)
		for _, p := range hits {
			to := target + strings.TrimPrefix(p, src)
			if _, exists := next[to]; exists && !force {
				return nil, fmt.Errorf("destination exists, source=%s, destination=%s (pass -f to overwrite)", p, to)
			}
			for k := range next {
				if strings.HasPrefix(k, to+"/") {
					return nil, fmt.Errorf("destination %q is a directory", to)
				}
			}
			next[to] = c.work[p]
			delete(next, p)
			moves = append(moves, Move{From: p, To: to})
		}
	}
	if dryRun || len(moves) == 0 {
		return moves, nil
	}
	tree, err := r.eng.WriteTree(next)
	if err != nil {
		return nil, fmt.Errorf("worktree.Mv: %w", err)
	}
	if _, err := r.eng.RewriteWorking(c.line.ID, tree, change.OpMv, false); err != nil {
		return nil, fmt.Errorf("worktree.Mv: %w", err)
	}
	return moves, r.refreshFolder(c)
}

// ── clean ────────────────────────────────────────────────────────────────────

// IgnoredPaths lists what is in branch's folder but outside cairn's model:
// files matched by .gitignore/.cairnignore (and not tracked). Wholly-ignored
// directories are reported once, with a trailing "/", when dirs is set, and
// skipped otherwise. A directory holding a nested repository (.git / .cairn)
// is never listed.
func (r *Repo) IgnoredPaths(branch string, dirs bool) ([]string, error) {
	unlock, err := r.lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	c, err := r.beginOp(branch, "IgnoredPaths")
	if err != nil {
		return nil, err
	}
	return ignoredIn(c, dirs)
}

func ignoredIn(c *opCtx, dirs bool) ([]string, error) {
	// What the snapshot walk SEES is, by definition, everything that is not
	// ignored; the ignored set is its complement on disk.
	seen := map[string]struct{}{}
	seenDirs := map[string]struct{}{}
	skip := &skipTracker{}
	// Tracked means tracked in the WORKING change (snapshots included), not the
	// sealed tip: a file added since the last commit that a later .gitignore
	// edit now matches is still work, and clean must never delete it.
	tracked := trackedSetMeta(c.work)
	if err := walkWorktree(c.dir, tracked, gitlinkSet(c.work), skip, func(slashRel, _ string, _ fs.DirEntry) error {
		seen[slashRel] = struct{}{}
		for d := path.Dir(slashRel); d != "." && d != "/"; d = path.Dir(d) {
			if _, ok := seenDirs[d]; ok {
				break
			}
			seenDirs[d] = struct{}{}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("worktree.IgnoredPaths: %w", err)
	}
	// Paths the walk could not read are not "ignored" — leave them alone.
	unreadable := map[string]struct{}{}
	for _, p := range skip.Paths {
		unreadable[strings.TrimSuffix(p, "/")] = struct{}{}
	}
	gitlinks := gitlinkSet(c.work)
	var out []string
	err := filepath.WalkDir(c.dir, func(full string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(c.dir, full)
		if rerr != nil || rel == "." {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		if _, bad := unreadable[slashRel]; bad {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".cairn" {
				return filepath.SkipDir
			}
			if _, isLink := gitlinks[slashRel]; isLink {
				return filepath.SkipDir
			}
			if _, has := seenDirs[slashRel]; has {
				return nil
			}
			// Nothing cairn tracks lives under here.
			if dirs && !holdsRepo(full) {
				out = append(out, slashRel+"/")
			}
			return filepath.SkipDir
		}
		if _, ok := seen[slashRel]; !ok {
			out = append(out, slashRel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("worktree.IgnoredPaths: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// holdsRepo reports whether dir contains a nested repository anywhere inside.
func holdsRepo(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".cairn") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// Clean removes ignored files from branch's folder (git clean -X / -x): build
// output, caches, editor droppings. dirs also removes wholly-ignored
// directories (-d). specs, when given, restrict it. With dryRun it only
// reports. It returns the paths (directories with a trailing "/") it removed
// or would remove.
//
// It never touches anything in the working change — in cairn a new, un-ignored
// file is already tracked work, not "untracked" — and so, alone among the
// verbs here, it has nothing to record: the files it deletes were never in
// cairn's store, and are not recoverable with undo.
func (r *Repo) Clean(branch string, specs []string, dirs, dryRun bool) ([]string, error) {
	unlock, err := r.lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	c, err := r.beginOp(branch, "Clean")
	if err != nil {
		return nil, err
	}
	all, err := ignoredIn(c, dirs)
	if err != nil {
		return nil, err
	}
	var targets []string
	for _, p := range all {
		if len(specs) == 0 || matchAny(strings.TrimSuffix(p, "/"), specs) {
			targets = append(targets, p)
		}
	}
	if dryRun {
		return targets, nil
	}
	for _, p := range targets {
		full, jerr := containedJoin(c.dir, strings.TrimSuffix(p, "/"))
		if jerr != nil {
			return nil, jerr
		}
		if strings.HasSuffix(p, "/") {
			err = os.RemoveAll(full)
		} else {
			err = os.Remove(full)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("worktree.Clean: %w", err)
		}
	}
	return targets, nil
}

// ── apply / am ───────────────────────────────────────────────────────────────

func gitMode(m string, fallback change.EntryMode) (change.EntryMode, error) {
	switch m {
	case "":
		return fallback, nil
	case "100644", "100664":
		return change.ModeRegular, nil
	case "100755":
		return change.ModeExecutable, nil
	case "120000":
		return change.ModeSymlink, nil
	}
	return 0, fmt.Errorf("unsupported file mode %s", m)
}

// applyFiles applies parsed file patches to a tree (path->entry), all or
// nothing, and returns the new tree plus the paths it touched. Like `git
// apply` it is strict: a created file must not exist, a patched or deleted one
// must, and every hunk's context must match.
func (r *Repo) applyFiles(entries map[string]change.TreeEntry, files []gitpatch.File) (map[string]change.TreeEntry, []string, error) {
	next := cloneEntries(entries)
	var touched []string
	for _, f := range files {
		if f.Binary {
			return nil, nil, fmt.Errorf("%s: binary patches are not supported", f.Path())
		}
		var old []byte
		mode := change.ModeRegular
		if f.IsNew {
			if _, exists := next[f.NewPath]; exists {
				return nil, nil, fmt.Errorf("%s: already exists in the working change", f.NewPath)
			}
		} else {
			e, ok := next[f.OldPath]
			if !ok {
				return nil, nil, fmt.Errorf("%s: No such file or directory", f.OldPath)
			}
			if e.Mode == change.ModeGitlink {
				return nil, nil, fmt.Errorf("%s: is a submodule; cairn cannot patch it", f.OldPath)
			}
			data, err := r.eng.ReadBlob(e.SHA)
			if err != nil {
				return nil, nil, err
			}
			old, mode = data, e.Mode
		}
		patched, err := gitpatch.Apply(old, f)
		if err != nil {
			return nil, nil, err
		}
		if f.IsDelete {
			if len(patched) != 0 {
				return nil, nil, fmt.Errorf("%s: patch does not apply (the file has content the deletion does not account for)", f.OldPath)
			}
			delete(next, f.OldPath)
			touched = append(touched, f.OldPath)
			continue
		}
		if mode, err = gitMode(f.NewMode, mode); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", f.Path(), err)
		}
		sha, err := r.eng.WriteBlob(patched)
		if err != nil {
			return nil, nil, err
		}
		if f.OldPath != "" && f.OldPath != f.NewPath {
			if _, exists := next[f.NewPath]; exists {
				return nil, nil, fmt.Errorf("%s: already exists in the working change", f.NewPath)
			}
			delete(next, f.OldPath)
			touched = append(touched, f.OldPath)
		}
		next[f.NewPath] = change.TreeEntry{SHA: sha, Mode: mode}
		touched = append(touched, f.NewPath)
	}
	return next, touched, nil
}

// ApplyPatch applies parsed file patches to branch's working change and folder
// (git apply), all or nothing. With check it only verifies they would apply.
// It returns the touched paths.
func (r *Repo) ApplyPatch(branch string, files []gitpatch.File, check bool) ([]string, error) {
	unlock, err := r.lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	c, err := r.beginOp(branch, "Apply")
	if err != nil {
		return nil, err
	}
	next, touched, err := r.applyFiles(c.work, files)
	if err != nil {
		return nil, err
	}
	if check || len(touched) == 0 {
		return touched, nil
	}
	tree, err := r.eng.WriteTree(next)
	if err != nil {
		return nil, fmt.Errorf("worktree.Apply: %w", err)
	}
	if _, err := r.eng.RewriteWorking(c.line.ID, tree, change.OpApply, false); err != nil {
		return nil, fmt.Errorf("worktree.Apply: %w", err)
	}
	return touched, r.refreshFolder(c)
}

// AmResult reports an Am: the commits sealed, and — when it stopped — why.
type AmResult struct {
	Commits   []string
	Conflicts []change.Conflict // un-sealed work overlapping an applied patch
}

// Am applies `git format-patch` mails to branch as sealed commits, one per
// mail, preserving each author, date and message. Each patch must apply
// exactly onto the sealed tip left by the one before; the first that does not
// stops the run with the earlier ones sealed (as `git am` does).
func (r *Repo) Am(branch string, mails []gitpatch.Mail, strip int) (AmResult, error) {
	unlock, err := r.lockState()
	if err != nil {
		return AmResult{}, err
	}
	defer unlock()
	c, err := r.beginOp(branch, "Am")
	if err != nil {
		return AmResult{}, err
	}
	if err := r.refuseOpenConflicts(c, "Am"); err != nil {
		return AmResult{}, err
	}
	var res AmResult
	cur := c.base
	stop := func(n int, m gitpatch.Mail, cause error) (AmResult, error) {
		if len(res.Commits) > 0 {
			if rerr := r.refreshFolder(c); rerr != nil {
				return res, rerr
			}
		}
		return res, fmt.Errorf("am: patch %d/%d (%s) failed: %w — %d earlier patch(es) were applied", n+1, len(mails), m.Subject, cause, len(res.Commits))
	}
	for n, m := range mails {
		files, perr := gitpatch.Parse(m.Patch, strip)
		if perr != nil {
			return stop(n, m, perr)
		}
		if len(files) == 0 {
			return stop(n, m, errors.New("the mail carries no patch"))
		}
		next, _, aerr := r.applyFiles(cur, files)
		if aerr != nil {
			return stop(n, m, aerr)
		}
		tree, werr := r.eng.WriteTree(next)
		if werr != nil {
			return stop(n, m, werr)
		}
		ar, serr := r.eng.AppendSealed(c.line.ID, change.OpAm, change.AppendSpec{
			Message: m.Message(),
			Tree:    tree,
			Author:  &change.Signature{Name: m.AuthorName, Email: m.AuthorEmail, When: m.Date},
		})
		if serr != nil {
			return stop(n, m, serr)
		}
		res.Commits = append(res.Commits, ar.Commit)
		cur = next
		if len(ar.Conflicts) > 0 {
			res.Conflicts = ar.Conflicts
			break
		}
	}
	return res, r.refreshFolder(c)
}

// ── stash additions ──────────────────────────────────────────────────────────

// StashPaths shelves only the edits under specs (`stash push -- <paths>`),
// leaving the rest of the working change in place.
func (r *Repo) StashPaths(branch, message string, specs []string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	c, err := r.beginOp(branch, "Stash")
	if err != nil {
		return err
	}
	if s, bad := unmatchedSpec(specs, c.work, c.base); bad {
		return fmt.Errorf("pathspec %q did not match any file(s) known to cairn", s)
	}
	shelve := cloneEntries(c.base)
	keep := cloneEntries(c.work)
	for p, e := range c.work {
		if matchAny(p, specs) {
			shelve[p] = e
			if b, ok := c.base[p]; ok {
				keep[p] = b
			} else {
				delete(keep, p)
			}
		}
	}
	for p, b := range c.base {
		if _, still := c.work[p]; !still && matchAny(p, specs) {
			delete(shelve, p)
			keep[p] = b
		}
	}
	shelveTree, err := r.eng.WriteTree(shelve)
	if err != nil {
		return fmt.Errorf("worktree.Stash: %w", err)
	}
	keepTree, err := r.eng.WriteTree(keep)
	if err != nil {
		return fmt.Errorf("worktree.Stash: %w", err)
	}
	if _, err := r.eng.StashPushTrees(c.line.ID, message, shelveTree, keepTree); err != nil {
		return fmt.Errorf("worktree.Stash: %w", err)
	}
	return r.refreshFolder(c)
}

// StashRestore applies a stash onto branch's working change with a three-way
// merge — over un-sealed work, and onto a line that has moved since the stash
// was taken. drop removes the entry afterwards (pop), unless the apply left
// conflicts, which are returned.
func (r *Repo) StashRestore(branch string, stashID int64, drop bool) ([]change.Conflict, error) {
	unlock, err := r.lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	c, err := r.beginOp(branch, "StashPop")
	if err != nil {
		return nil, err
	}
	cf, err := r.eng.StashApplyMerge(c.line.ID, stashID, drop)
	if err != nil {
		return nil, fmt.Errorf("worktree.StashPop: %w", err)
	}
	return cf, r.refreshFolder(c)
}

// StashGet loads one stash entry (0 = the most recent).
func (r *Repo) StashGet(id int64) (change.StashEntry, error) { return r.eng.StashGet(id) }

// StashClear discards every stash entry, returning how many there were.
func (r *Repo) StashClear() (int, error) {
	unlock, err := r.lockState()
	if err != nil {
		return 0, err
	}
	defer unlock()
	return r.eng.StashClear()
}

// StashDiff returns what a stash entry shelved, as a diff against the commit
// it was taken on.
func (r *Repo) StashDiff(id int64) (change.StashEntry, []change.FileDiff, error) {
	s, err := r.eng.StashGet(id)
	if err != nil {
		return change.StashEntry{}, nil, err
	}
	diffs, err := r.eng.DiffCommits(s.BaseSha, s.CommitSha)
	if err != nil {
		return change.StashEntry{}, nil, err
	}
	return s, diffs, nil
}

// ResolveStashID maps a stash argument to a catalogue id: cairn's own id from
// `stash list`, or git's stash@{N} (N entries below the top of the stack).
func (r *Repo) ResolveStashID(arg string) (int64, error) {
	if strings.HasPrefix(arg, "stash@{") && strings.HasSuffix(arg, "}") {
		n, err := strconv.Atoi(arg[len("stash@{") : len(arg)-1])
		if err != nil || n < 0 {
			return 0, fmt.Errorf("invalid stash reference %q", arg)
		}
		list, err := r.eng.StashList()
		if err != nil {
			return 0, err
		}
		if n >= len(list) {
			return 0, fmt.Errorf("%s is not a valid stash reference (the stack holds %d)", arg, len(list))
		}
		return list[n].ID, nil
	}
	id, err := strconv.ParseInt(arg, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid stash id %q (use an id from 'cairn stash list', or stash@{N})", arg)
	}
	return id, nil
}

// FolderOf returns the absolute path of branch's expressed folder.
func (r *Repo) FolderOf(branch string) (string, bool) {
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return "", false
	}
	return filepath.Join(r.root, entry.Path), true
}

func shortID(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// refuseNothingToCommit snapshots branch and returns ErrNothingToCommit when
// its working change is identical to the line's sealed tip. A merge in
// progress (a conflicted pull) or a stopped rebase still has a seal to make,
// so those are never "empty".
func (r *Repo) refuseNothingToCommit(branch string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	entry, ok := r.st.Expressed[branch]
	if !ok {
		unlock()
		return nil // Commit reports the unexpressed branch
	}
	_, _, serr := r.syncBranch(branch, entry)
	unlock()
	if serr != nil {
		return serr
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.Commit: %w", err)
	}
	empty, err := r.eng.WorkingChangeEmpty(line.ID)
	if err != nil {
		return fmt.Errorf("worktree.Commit: %w", err)
	}
	if empty {
		return ErrNothingToCommit
	}
	return nil
}

// ErrNothingToCommit is returned by CommitWith when the working change is
// clean (git: "nothing to commit, working tree clean").
var ErrNothingToCommit = errors.New("nothing to commit, working tree clean")
