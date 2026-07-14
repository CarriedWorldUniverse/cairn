package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/change/diff3"
	"github.com/CarriedWorldUniverse/cairn/internal/release"
	"github.com/CarriedWorldUniverse/cairn/internal/userconfig"
	"github.com/CarriedWorldUniverse/cairn/internal/version"
)

// FolderName maps a branch (line) name to its on-disk working-folder name. A
// branch name may be path-like (e.g. "base/5-0"), but it must NOT become a nested
// folder: "/" is replaced with "-" so every branch expresses as a single flat
// folder ("base-5-0") under the repo root. This avoids nested-directory swaps —
// the source of the Windows rename race — and keeps expressed folders siblings.
// The branch NAME is unchanged everywhere else (tree/log/push use "base/5-0").
func FolderName(branch string) string {
	return strings.ReplaceAll(branch, "/", "-")
}

// ErrPushConflict is returned by Repo.Push when a diverged remote was pulled
// and the 3-way merge produced conflicts: the push is stopped (not retried) so
// the operator can resolve the conflict markers left on disk, then push again.
var ErrPushConflict = errors.New("remote diverged and merging produced conflicts; resolve, then push")

// Repo is the working-copy orchestrator that bridges expressed branch folders on
// disk and the cairn change engine. Each expressed branch is a folder under root
// holding the materialized files of an open change on the corresponding line.
type Repo struct {
	root   string
	author string
	eng    *change.Engine
	st     *State
	stPath string

	// lastSyncNote records the outcome of the best-effort commit-time auto-sync
	// from the most recent Commit, so the CLI can surface it. Empty means autosync
	// was off (or no commit has run); see LastSyncNote.
	lastSyncNote string

	// branchHint is the branch whose expressed folder the user is standing in
	// (set by the CLI when a command is run from inside a branch folder). When
	// set, it is the default branch for commands that omit one. Empty means at the
	// repo root (or not inside any branch folder) → fall back to the root line.
	branchHint string

	// lockFile/lockDepth back the cross-process working-copy lock (issue #81).
	// Every wc.json-mutating method takes it via lockState() first; it is
	// reentrant within this process (Commit → autoSync → Pull) so nested calls
	// share the one held OS lock. nil/0 = not held.
	lockFile  *os.File
	lockDepth int
}

// StatusInfo reports the state of an expressed branch: its lineage (root-first
// line names), how far ahead of its base it is, any open conflict paths, and the
// set of currently expressed branch names.
type StatusInfo struct {
	Branch    string
	Lineage   []string
	Ahead     int
	Conflicts []string
	Expressed []string
	Added     []string
	Modified  []string
	Deleted   []string
}

// Open opens (creating if needed) the working copy rooted at root with the given
// default author. The change engine lives under root/.cairn and the working-copy
// state under root/.cairn/wc.json. On first run the structural root line
// (whatever it is named) is expressed automatically.
func Open(root, author string) (*Repo, error) {
	cairnDir := filepath.Join(root, ".cairn")
	if err := os.MkdirAll(cairnDir, 0o755); err != nil {
		return nil, fmt.Errorf("worktree.Open: %w", err)
	}
	eng, err := change.Open(cairnDir)
	if err != nil {
		return nil, fmt.Errorf("worktree.Open: %w", err)
	}
	stPath := filepath.Join(cairnDir, "wc.json")
	st, err := LoadState(stPath)
	if err != nil {
		_ = eng.Close()
		return nil, fmt.Errorf("worktree.Open: %w", err)
	}
	author = resolveIdentity(eng, author)
	r := &Repo{root: root, author: author, eng: eng, st: st, stPath: stPath}
	// First-run guard, by STRUCTURE not name: express the actual root line
	// (parent_line IS NULL), whatever it is called. After a Clone of a remote
	// whose default branch is e.g. "master", the root is named "master" and
	// expressing the literal "main" would fail.
	root2, err := r.eng.RootLine()
	if err != nil {
		_ = eng.Close()
		return nil, err
	}
	if _, ok := st.Expressed[root2.Name]; !ok {
		if err := r.Express(root2.Name, ""); err != nil {
			_ = eng.Close()
			return nil, err
		}
	}
	return r, nil
}

// resolveIdentity resolves the author name + email for commits this working copy
// writes and configures the engine accordingly. name comes from config user.name
// (else the passed author); email from config user.email (else $CAIRN_EMAIL, else
// $GIT_AUTHOR_EMAIL, else ""). It returns the resolved name so the caller can use
// it as the Repo's author (recorded on change rows via CreateChange).
func resolveIdentity(eng *change.Engine, author string) string {
	// cairn OWNS its identity — it is not silently inherited from git config.
	// Precedence: explicit (--author / $CAIRN_* env) → repo cairn config → global
	// cairn config. When nothing is set the CLI runs first-use setup (see the
	// cmd layer's ensureIdentity), pre-filling suggestions from git config.
	name := firstNonEmpty(author, repoConfig(eng, "user.name"), userconfig.Get("user.name"))
	email := firstNonEmpty(
		os.Getenv("CAIRN_EMAIL"), os.Getenv("GIT_AUTHOR_EMAIL"),
		repoConfig(eng, "user.email"), userconfig.Get("user.email"))
	eng.SetIdentity(name, email)
	return name
}

func repoConfig(eng *change.Engine, key string) string {
	if v, ok, _ := eng.GetConfig(key); ok {
		return v
	}
	return ""
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// Identity returns the resolved author name and email (either may be "" when
// nothing is configured yet).
func (r *Repo) Identity() (name, email string) { return r.eng.Identity() }

// SetIdentity overrides the author identity for this session (used by first-use
// setup after it collects and stores the identity globally).
func (r *Repo) SetIdentity(name, email string) { r.eng.SetIdentity(name, email) }

// cacheDir returns the path to the content-addressed blob cache shared by all
// materializations in this working copy.
func (r *Repo) cacheDir() string { return filepath.Join(r.root, ".cairn", "cache") }

// Close releases the underlying change engine.
func (r *Repo) Close() error {
	if err := r.eng.Close(); err != nil {
		return fmt.Errorf("worktree.Close: %w", err)
	}
	return nil
}

// lockState takes the cross-process working-copy lock and reloads wc.json so
// this operation sees other processes' committed state — the fix for issue
// #81, where concurrent `express` did an unlocked reload-modify-save of
// wc.json and clobbered each other's expressed-lines entries (a later commit
// then reported "branch not expressed" and a naive push shipped an empty
// branch). Every wc.json-mutating method calls this first and defers the
// returned release.
//
// It is REENTRANT within this process: nested calls (Commit → autoSync →
// Pull; Open → Express; methods that call Unexpress) share the single held OS
// lock and, crucially, do NOT reload — only the outermost acquisition reloads,
// so an inner call can't drop the outer op's in-progress state. Cross-process
// it is mutually exclusive, so concurrent cairn invocations on one shared
// working copy serialize instead of racing.
//
// INVARIANT (issue #98 Phase A): every exported Repo/releaseAdapter method
// that mutates shared state — go-git refs, the SQLite catalogue, wc.json, or
// an expressed folder's on-disk content — takes this lock as its first
// statement, before reading r.st (so it sees the freshly-reloaded state, not
// a pre-lock snapshot). A pure read with no side effect does not need it.
// When adding a new verb: if it writes any of the above, copy the
// `unlock, err := r.lockState(); if err != nil { return ... }; defer unlock()`
// idiom used throughout this file — do not invent a new locking pattern.
func (r *Repo) lockState() (func(), error) {
	if r.lockDepth > 0 {
		r.lockDepth++
		return r.releaseState, nil
	}
	f, err := openWCLock(filepath.Dir(r.stPath))
	if err != nil {
		return nil, fmt.Errorf("worktree: lock working copy: %w", err)
	}
	r.lockFile = f
	r.lockDepth = 1
	fresh, err := LoadState(r.stPath)
	if err != nil {
		r.releaseState()
		return nil, err
	}
	r.st = fresh
	return r.releaseState, nil
}

// releaseState drops one level of the reentrant lock, releasing the OS lock
// when the outermost holder returns.
func (r *Repo) releaseState() {
	if r.lockDepth == 0 {
		return
	}
	r.lockDepth--
	if r.lockDepth == 0 && r.lockFile != nil {
		closeWCLock(r.lockFile)
		r.lockFile = nil
	}
}

// Express materializes a branch as a folder under root, creating its line if it
// does not exist. For the root line, branch must equal change.RootLineName and
// parent is ignored. For any other branch, an absent line is forked off parent
// (defaulting to the structural root). Re-expressing an already-expressed
// branch is a no-op.
func (r *Repo) Express(branch, parent string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	if _, ok := r.st.Expressed[branch]; ok {
		return nil
	}

	// Resolve the line structurally: an existing line (under ANY name, including
	// the root) is used as-is; an absent line is forked off parent (defaulting
	// to the structural root). This keeps root detection name-independent —
	// after an import the root may be named "master"/"trunk" rather than "main".
	var line change.Line
	l, err := r.eng.LineByName(branch)
	switch {
	case err == nil:
		line = l
	case errors.Is(err, change.ErrNotFound):
		if parent == "" {
			// Default to the actual structural root (parent_line IS NULL),
			// whatever its name — after an import it may be "master"/"trunk".
			rootLine, rerr := r.eng.RootLine()
			if rerr != nil {
				return fmt.Errorf("worktree.Express: %w", rerr)
			}
			parent = rootLine.Name
		}
		parentLine, perr := r.eng.LineByName(parent)
		if perr != nil {
			return fmt.Errorf("worktree.Express: parent %q: %w", parent, perr)
		}
		created, cerr := r.eng.CreateLine(branch, parentLine.ID)
		if cerr != nil {
			return fmt.Errorf("worktree.Express: %w", cerr)
		}
		line = created
	default:
		return fmt.Errorf("worktree.Express: %w", err)
	}

	// The on-disk folder is the FLAT form of the branch name (slashes → dashes),
	// so a path-like branch never nests. Refuse if that flat name is already used
	// by a different branch (e.g. "feat/x" → "feat-x" colliding with a literal
	// "feat-x") rather than clobbering it.
	folder := FolderName(branch)
	for b, e := range r.st.Expressed {
		if b != branch && e.Path == folder {
			return fmt.Errorf("worktree.Express: folder %q is already used by branch %q; rename one before expressing %q", folder, b, branch)
		}
	}

	// Do the filesystem work FIRST so a failed materialize/mkdir cannot leave a
	// dangling change with no wc.json entry. CreateChange has no observable side
	// effect until Express records it below, so deferring it past the FS op is
	// safe and avoids the leak.
	dir := filepath.Join(r.root, folder)
	if line.TipCommit != "" {
		if err := Materialize(r.eng, r.cacheDir(), line.TipCommit, dir); err != nil {
			return fmt.Errorf("worktree.Express: %w", err)
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("worktree.Express: %w", err)
	}

	// Reuse an existing open (unsealed) change if one already exists for this
	// line (e.g. a change imported from refs/cairn/meta that carries open
	// conflict rows). Creating a new change would orphan those rows.
	ch, err := r.eng.OpenChangeForLine(line.ID)
	if errors.Is(err, change.ErrNotFound) {
		ch, err = r.eng.CreateChange(line.ID, r.author)
	}
	if err != nil {
		return fmt.Errorf("worktree.Express: %w", err)
	}

	r.st.Expressed[branch] = Entry{Path: folder, ChangeID: ch.ID}
	if err := SaveState(r.stPath, r.st); err != nil {
		return fmt.Errorf("worktree.Express: %w", err)
	}
	return nil
}

// SyncWorking snapshots every expressed folder into its line's open working
// change (amend-in-place), so the working commit always reflects the folder. The
// cache makes an unchanged folder cheap. Self-healing: a corrupt/missing cache is
// treated as empty (full rescan), never an error.
func (r *Repo) SyncWorking() error {
	// Serialize with the cross-process working-copy lock (#86): this scans EVERY
	// expressed branch, reading each line's tip from the shared cairn.db and its
	// commit object from the shared git store. Without the lock a concurrent
	// builder committing a SIBLING branch mid-scan advances that tip and writes a
	// new commit object this process's go-git object cache hasn't seen, so the
	// snapshot aborts "commitTree: object not found". The lock (also reloading
	// wc.json) makes the scan see one consistent snapshot of tips + objects.
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	if active, err := r.eng.BisectActive(); err != nil {
		return fmt.Errorf("worktree.SyncWorking: %w", err)
	} else if active {
		// A bisect session has a historical commit materialized in the folder;
		// never snapshot it into the open working change.
		return nil
	}
	for branch, entry := range r.st.Expressed {
		if err := r.syncBranch(branch, entry); err != nil {
			return err
		}
	}
	return nil
}

// syncOne snapshots a single expressed branch into its working change, skipping
// the snapshot during an active bisect (the folder holds a historical commit).
// Read/write methods call this so a command re-scans ONLY the branch it touches,
// rather than every expressed folder — the cost that dominated `status` on a
// large tree with several branches expressed.
func (r *Repo) syncOne(branch string, entry Entry) error {
	if active, err := r.eng.BisectActive(); err != nil {
		return fmt.Errorf("worktree.syncOne: %w", err)
	} else if active {
		return nil
	}
	return r.syncBranch(branch, entry)
}

// syncBranch snapshots one expressed branch's folder into its open working
// change via amend-in-place, using the per-branch snapshot cache for cheap
// rescans. A corrupt/missing cache is treated as empty (full rescan).
func (r *Repo) syncBranch(branch string, entry Entry) error {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.syncBranch: %w", err)
	}
	// Tracked = the committed tip's paths. Ignore patterns only affect untracked
	// paths, so a committed-then-ignored file is never dropped from the scan.
	var tracked map[string]struct{}
	if line.TipCommit != "" {
		committed, ferr := r.eng.Files(line.TipCommit)
		if ferr != nil {
			return fmt.Errorf("worktree.syncBranch: %w", ferr)
		}
		tracked = trackedSet(committed)
	}
	cachePath := filepath.Join(r.root, ".cairn", "wc-cache", branch+".json")
	cache, err := loadWCCache(cachePath)
	if err != nil {
		cache = map[string]wcCacheEntry{} // SELF-HEAL: corrupt cache → full rescan
	}
	scanStartNs := time.Now().UnixNano()
	entries, newCache, cacheChanged, err := CachedScan(r.eng, filepath.Join(r.root, entry.Path), tracked, cache, scanStartNs)
	if err != nil {
		return fmt.Errorf("worktree.syncBranch: %w", err)
	}
	if _, _, err := r.eng.SnapshotWorking(entry.ChangeID, entries); err != nil {
		return fmt.Errorf("worktree.syncBranch: %w", err)
	}
	if cacheChanged {
		if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
			return fmt.Errorf("worktree.syncBranch: %w", err)
		}
		if err := saveWCCache(cachePath, newCache); err != nil {
			return fmt.Errorf("worktree.syncBranch: %w", err)
		}
	}
	return nil
}

// Commit seals the open working change of an expressed branch: it first
// snapshots the folder into the working change (so the seal captures live
// edits), stamps the message (adopting the parent line via merge-forward),
// advances this branch to the fresh working change opened by the seal, and
// re-materializes to the new working tip. The CommitResult carries the sealed
// head and any conflicts recorded.
func (r *Repo) Commit(branch, message string) (change.CommitResult, error) {
	unlock, err := r.lockState()
	if err != nil {
		return change.CommitResult{}, err
	}
	defer unlock()
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: branch %q is not expressed", branch)
	}
	if err := r.syncBranch(branch, entry); err != nil {
		return change.CommitResult{}, err
	}
	// Refuse to seal while the working change still has UNRESOLVED conflicts (like
	// git's unmerged-paths block). The working snapshot taken above still contains
	// the conflict markers; sealing would accept that marker text as the
	// resolution and silently drop the conflict, baking "<<<<<<<" into history.
	// The user must edit out the markers and `cairn resolve <path>` first.
	if open, cerr := r.eng.Conflicts(entry.ChangeID); cerr != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", cerr)
	} else if len(open) > 0 {
		paths := make([]string, len(open))
		for i, c := range open {
			paths[i] = c.Path
		}
		return change.CommitResult{}, fmt.Errorf(
			"worktree.Commit: %d unresolved conflict(s) in: %s — edit out the <<<<<<< markers, run 'cairn resolve %s <path>' for each, then commit",
			len(open), strings.Join(paths, ", "), branch)
	}
	prevChangeID := entry.ChangeID
	newChangeID, conflicts, err := r.eng.Seal(entry.ChangeID, message)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err)
	}
	// This branch now tracks the fresh working change opened by Seal.
	entry.ChangeID = newChangeID
	r.st.Expressed[branch] = entry
	if err := SaveState(r.stPath, r.st); err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err)
	}
	// The branch's working-cache referenced the old change's snapshot fingerprints
	// and stays valid (same folder paths); the fresh change's first SyncWorking
	// will re-amend off it. Re-materialize to the new working tip (== sealed tree;
	// folder content unchanged, but merged-in parent state is reflected).
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err)
	}
	if line.TipCommit != "" {
		if err := Materialize(r.eng, r.cacheDir(), line.TipCommit, filepath.Join(r.root, entry.Path)); err != nil {
			return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err)
		}
	}

	// A conflicting seal records the conflicts on the now-sealed change, but the
	// live unresolved state belongs to the FRESH working change. Snapshot the
	// conflict-marked content (just materialized to disk) into the fresh change so
	// it has a head to resolve against, then move the conflict rows onto it — so
	// `status`/`resolve` operate on the working change as the operator expects.
	if len(conflicts) > 0 {
		if err := r.syncBranch(branch, entry); err != nil {
			return change.CommitResult{}, err
		}
		if err := r.eng.ReassignConflicts(prevChangeID, entry.ChangeID); err != nil {
			return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err)
		}
	}

	// Best-effort, opt-in auto-sync AFTER the seal. The seal has already
	// succeeded above; the pull is purely additive and never alters res or the
	// (nil) error returned here. Any sync failure (offline, fetch error, conflict)
	// is captured as a note for the CLI, not propagated.
	r.lastSyncNote = r.autoSync()

	return change.CommitResult{HeadCommit: line.TipCommit, Conflicts: conflicts}, nil
}

// autoSync runs the opt-in commit-time sync and returns a note describing the
// outcome for the CLI: "" when autosync is off, "synced" on a successful pull,
// or "skipped:<reason>" otherwise. It never returns an error — the commit's
// success is independent of the sync.
func (r *Repo) autoSync() string {
	v, ok, err := r.eng.GetConfig("autosync")
	if err != nil || !ok || !change.ConfigTruthy(v) {
		return ""
	}
	rems, err := r.eng.ListRemotes()
	if err != nil {
		return "skipped:list-remotes-error"
	}
	hasOrigin := false
	for _, rem := range rems {
		if rem.Name == "origin" {
			hasOrigin = true
			break
		}
	}
	if !hasOrigin {
		return "skipped:no origin"
	}
	sum, perr := r.Pull("origin")
	if perr != nil {
		return "skipped:offline"
	}
	for _, lr := range sum.Lines {
		if lr.Conflicts > 0 {
			return "skipped:conflicts"
		}
	}
	return "synced"
}

// LastSyncNote returns the outcome of the most recent Commit's best-effort
// commit-time auto-sync, for the CLI to surface. It is "" when autosync was off,
// "synced" on success, or "skipped:<reason>" otherwise.
func (r *Repo) LastSyncNote() string { return r.lastSyncNote }

// Fold folds an expressed branch's line back into its parent, fast-forwarding the
// parent tip, then unexpresses the branch. Any expressed line whose ID is the
// folded line's parent is re-materialized to the new parent tip so its folder
// reflects the adopted work. force allows discarding uncommitted changes; without
// it, Fold refuses if the branch has uncommitted edits.
func (r *Repo) Fold(branch string, force bool) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	if !force {
		dirty, derr := r.isDirty(branch)
		if derr != nil {
			return fmt.Errorf("worktree.Fold: %w", derr)
		}
		if dirty {
			return fmt.Errorf("worktree.Fold: branch %q has un-sealed work (recoverable with 'cairn undo'); commit it or pass --force to discard", branch)
		}
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.Fold: %w", err)
	}
	parentLineID := line.ParentLine
	// Guard: folding into a remote-tracked (upstream) line advances it locally,
	// which diverges from how the remote integrates the change (a PR / its own
	// merge) and a protected remote will reject the push. Refuse by default, with
	// a consequence warning; --force lets the user do it anyway.
	if !force && parentLineID != "" {
		tracks, terr := r.eng.LineTracksRemote(parentLineID)
		if terr != nil {
			return fmt.Errorf("worktree.Fold: %w", terr)
		}
		if tracks {
			parent, perr := r.eng.LineByID(parentLineID)
			if perr != nil {
				return fmt.Errorf("worktree.Fold: %w", perr)
			}
			return fmt.Errorf("worktree.Fold: %q tracks the remote — folding %q into it locally diverges from upstream (a protected remote will reject the push). Push %q and open a PR instead; 'cairn undo' reverts a fold. Pass --force to fold anyway", parent.Name, branch, branch)
		}
	}
	if err := r.eng.FoldLine(line.ID); err != nil {
		return fmt.Errorf("worktree.Fold: %w", err)
	}
	if err := r.Unexpress(branch, true); err != nil {
		return err
	}
	if parentLineID == "" {
		return nil
	}
	// Partial-failure gap: FoldLine + Unexpress have already committed by this
	// point, so the engine and wc.json stay consistent even if the re-materialize
	// below fails. The only casualty is a stale parent folder on disk; the caller
	// should re-express/re-materialize the parent to refresh it.
	for name, entry := range r.st.Expressed {
		pl, err := r.eng.LineByName(name)
		if err != nil {
			return fmt.Errorf("worktree.Fold: %w", err)
		}
		if pl.ID != parentLineID {
			continue
		}
		dir := filepath.Join(r.root, entry.Path)
		if pl.TipCommit != "" {
			if err := Materialize(r.eng, r.cacheDir(), pl.TipCommit, dir); err != nil {
				return fmt.Errorf("worktree.Fold: %w", err)
			}
		}
	}
	return nil
}

// Abandon throws away an expressed branch's line (nothing reaches the parent) and
// unexpresses the branch. force allows discarding uncommitted changes; without it,
// Abandon refuses if the branch has uncommitted edits.
func (r *Repo) Abandon(branch string, force bool) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	if !force {
		dirty, derr := r.isDirty(branch)
		if derr != nil {
			return fmt.Errorf("worktree.Abandon: %w", derr)
		}
		if dirty {
			return fmt.Errorf("worktree.Abandon: branch %q has un-sealed work (recoverable with 'cairn undo'); commit it or pass --force to discard", branch)
		}
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.Abandon: %w", err)
	}
	if line.ParentLine == "" {
		return fmt.Errorf("worktree.Abandon: cannot abandon the root line %q", branch)
	}
	if err := r.eng.AbandonLine(line.ID); err != nil {
		return fmt.Errorf("worktree.Abandon: %w", err)
	}
	return r.Unexpress(branch, true)
}

// Unexpress removes an expressed branch's folder and forgets it from state.
// force allows discarding uncommitted changes; without it, Unexpress refuses if
// the branch has uncommitted edits.
func (r *Repo) Unexpress(branch string, force bool) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	// Structural root guard: refuse only when the branch resolves to the root
	// line (ParentLine == ""). If the line is already gone (ErrNotFound — e.g.
	// after a fold/abandon removed it), don't block; proceed to remove the
	// folder/state below. Any other resolution error is fatal.
	line, err := r.eng.LineByName(branch)
	switch {
	case err == nil:
		if line.ParentLine == "" {
			return fmt.Errorf("worktree.Unexpress: cannot unexpress the root line %q", branch)
		}
	case errors.Is(err, change.ErrNotFound):
		// line already gone — fall through to folder/state cleanup
	default:
		return fmt.Errorf("worktree.Unexpress: %w", err)
	}
	if !force {
		dirty, derr := r.isDirty(branch)
		if derr != nil {
			return fmt.Errorf("worktree.Unexpress: %w", derr)
		}
		if dirty {
			return fmt.Errorf("worktree.Unexpress: branch %q has un-sealed work (recoverable with 'cairn undo'); commit it or pass --force to discard", branch)
		}
	}
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return fmt.Errorf("worktree.Unexpress: branch %q is not expressed", branch)
	}
	if err := os.RemoveAll(filepath.Join(r.root, entry.Path)); err != nil {
		return fmt.Errorf("worktree.Unexpress: %w", err)
	}
	delete(r.st.Expressed, branch)
	if err := SaveState(r.stPath, r.st); err != nil {
		return fmt.Errorf("worktree.Unexpress: %w", err)
	}
	return nil
}

// Resolve resolves a conflicting path on an expressed branch by taking the file's
// current on-disk content as the resolution, then re-materializes the branch.
// It refuses content that still contains diff3 conflict markers (unless force):
// accepting it would close the conflict row — so status stops reporting the
// conflict — while the markers live on in the file and the new tip commit.
func (r *Repo) Resolve(branch, path string, force bool) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return fmt.Errorf("worktree.Resolve: branch %q is not expressed", branch)
	}
	dir := filepath.Join(r.root, entry.Path)
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		return fmt.Errorf("worktree.Resolve: %w", err)
	}
	if !force && diff3.HasMarkers(data) {
		return fmt.Errorf(
			"worktree.Resolve: %q still contains conflict markers (<<<<<<< / ======= / >>>>>>>) — edit them out and re-run, or pass --force if the content is intentional",
			path)
	}
	if err := r.eng.ResolveConflict(entry.ChangeID, path, data); err != nil {
		return fmt.Errorf("worktree.Resolve: %w", err)
	}
	ch, err := r.eng.GetChange(entry.ChangeID)
	if err != nil {
		return fmt.Errorf("worktree.Resolve: %w", err)
	}
	if ch.HeadCommit != "" {
		if err := Materialize(r.eng, r.cacheDir(), ch.HeadCommit, dir); err != nil {
			return fmt.Errorf("worktree.Resolve: %w", err)
		}
	}
	return nil
}

// Status reports the state of an expressed branch.
func (r *Repo) Status(branch string) (StatusInfo, error) {
	unlock, err := r.lockState()
	if err != nil {
		return StatusInfo{}, err
	}
	defer unlock()
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return StatusInfo{}, fmt.Errorf("worktree.Status: branch %q is not expressed", branch)
	}
	if err := r.syncOne(branch, entry); err != nil {
		return StatusInfo{}, err
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return StatusInfo{}, fmt.Errorf("worktree.Status: %w", err)
	}
	lineage, err := r.eng.GetLineage(line.ID)
	if err != nil {
		return StatusInfo{}, fmt.Errorf("worktree.Status: %w", err)
	}
	names := make([]string, 0, len(lineage))
	for _, l := range lineage {
		names = append(names, l.Name)
	}
	// Ahead = SEALED commits since this line's branch point. The open working
	// change's commit sits at the line tip (an unsealed amend); it must not
	// inflate the count, so when present we measure height from its parent (the
	// sealed tip) instead of the raw line tip.
	sealedLine := line
	if ch, cerr := r.eng.GetChange(entry.ChangeID); cerr == nil && !ch.Sealed && ch.HeadCommit != "" {
		parent, perr := r.eng.FirstParent(ch.HeadCommit)
		if perr != nil {
			return StatusInfo{}, fmt.Errorf("worktree.Status: %w", perr)
		}
		sealedLine.TipCommit = parent
	}
	ahead, err := r.eng.LineHeight(sealedLine)
	if err != nil {
		return StatusInfo{}, fmt.Errorf("worktree.Status: %w", err)
	}
	diffs, err := r.WorkingDiff(branch)
	if err != nil {
		return StatusInfo{}, fmt.Errorf("worktree.Status: %w", err)
	}
	var added, modified, deleted []string
	for _, d := range diffs {
		switch d.Status {
		case change.Added:
			added = append(added, d.Path)
		case change.Modified:
			modified = append(modified, d.Path)
		case change.Deleted:
			deleted = append(deleted, d.Path)
		}
	}
	sort.Strings(added)
	sort.Strings(modified)
	sort.Strings(deleted)
	conflicts, err := r.eng.Conflicts(entry.ChangeID)
	if err != nil {
		return StatusInfo{}, fmt.Errorf("worktree.Status: %w", err)
	}
	paths := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		paths = append(paths, c.Path)
	}
	expressed := make([]string, 0, len(r.st.Expressed))
	for name := range r.st.Expressed {
		expressed = append(expressed, name)
	}
	sort.Strings(expressed)
	return StatusInfo{
		Branch:    branch,
		Lineage:   names,
		Ahead:     ahead,
		Conflicts: paths,
		Expressed: expressed,
		Added:     added,
		Modified:  modified,
		Deleted:   deleted,
	}, nil
}

// WorkingDiff returns the per-path diff between an expressed branch's open
// working change and its parent commit — the change introduced by the working
// commit. Since SyncWorking keeps the working commit equal to the folder, this
// is the live "what's uncommitted" view. A working change with no snapshot yet
// (empty head) yields an empty diff. Callers must SyncWorking first to capture
// on-disk edits into the working commit.
func (r *Repo) WorkingDiff(branch string) ([]change.FileDiff, error) {
	unlock, err := r.lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return nil, fmt.Errorf("worktree.WorkingDiff: branch %q is not expressed", branch)
	}
	if err := r.syncOne(branch, entry); err != nil {
		return nil, err
	}
	ch, err := r.eng.GetChange(entry.ChangeID)
	if err != nil {
		return nil, fmt.Errorf("worktree.WorkingDiff: %w", err)
	}
	if ch.HeadCommit == "" {
		// No working snapshot yet: nothing differs from the parent.
		return nil, nil
	}
	parent, err := r.eng.FirstParent(ch.HeadCommit)
	if err != nil {
		return nil, fmt.Errorf("worktree.WorkingDiff: %w", err)
	}
	diffs, err := r.eng.DiffCommits(parent, ch.HeadCommit)
	if err != nil {
		return nil, fmt.Errorf("worktree.WorkingDiff: %w", err)
	}
	return diffs, nil
}

// DiffCommits returns the per-path diff between two commits, passing through to
// the change engine.
func (r *Repo) DiffCommits(a, b string) ([]change.FileDiff, error) {
	fullA, err := r.eng.ResolveCommit(a)
	if err != nil {
		return nil, fmt.Errorf("worktree.DiffCommits: %w", err)
	}
	fullB, err := r.eng.ResolveCommit(b)
	if err != nil {
		return nil, fmt.Errorf("worktree.DiffCommits: %w", err)
	}
	diffs, err := r.eng.DiffCommits(fullA, fullB)
	if err != nil {
		return nil, fmt.Errorf("worktree.DiffCommits: %w", err)
	}
	return diffs, nil
}

// DiffMergeBase returns the per-path diff from the merge-base of a and b to
// b's tip — target...source ("three-dot") semantics, passing through to the
// change engine. Unlike DiffCommits (a literal tip-to-tip diff), this stays
// correct when a (the target) has advanced with commits b never saw: only the
// changes b actually introduced appear, never a spurious revert of a's
// unrelated commits. Returns change.ErrNoCommonAncestor if a and b share no
// history.
func (r *Repo) DiffMergeBase(a, b string) ([]change.FileDiff, error) {
	diffs, err := r.eng.DiffMergeBase(a, b)
	if err != nil {
		return nil, fmt.Errorf("worktree.DiffMergeBase: %w", err)
	}
	return diffs, nil
}

// DefaultBranch returns the name of the structural root line (the parent_line IS
// NULL line), whatever it is called. After a clone of a remote whose default
// branch is e.g. "master", this is "master" rather than the literal "main".
func (r *Repo) DefaultBranch() (string, error) {
	// If the user is standing inside a branch folder, that branch is the default —
	// like git acting on your current branch. Otherwise fall back to the root line.
	if r.branchHint != "" {
		return r.branchHint, nil
	}
	root, err := r.eng.RootLine()
	if err != nil {
		return "", fmt.Errorf("worktree.DefaultBranch: %w", err)
	}
	return root.Name, nil
}

// SetBranchHint records the branch whose expressed folder the user is standing in
// (computed by the CLI from the working directory). See DefaultBranch / CWDBranch.
func (r *Repo) SetBranchHint(branch string) { r.branchHint = branch }

// CWDBranch returns the branch whose expressed folder the user is in, if any. Used
// by commands (e.g. commit) that require a branch but can infer it from location.
func (r *Repo) CWDBranch() (string, bool) { return r.branchHint, r.branchHint != "" }

// IsExpressed reports whether the named branch has an expressed working folder.
// Used by commands (e.g. diff) to disambiguate a positional arg between a branch
// name and a file path.
func (r *Repo) IsExpressed(branch string) bool {
	_, ok := r.st.Expressed[branch]
	return ok
}

// Reparent changes branch's recorded parent line to newParent (by name), so a
// stacked branch flat-projected onto the root at import can be restored to its
// real parent. See change.Reparent.
func (r *Repo) Reparent(branch, newParent string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.Reparent: %w", err)
	}
	np, err := r.eng.LineByName(newParent)
	if err != nil {
		return fmt.Errorf("worktree.Reparent: parent %q: %w", newParent, err)
	}
	return r.eng.Reparent(line.ID, np.ID)
}

// BranchForFolder returns the branch whose expressed working folder is folder
// (the flat on-disk name, e.g. "base-5-0" for branch "base/5-0"), if any.
func (r *Repo) BranchForFolder(folder string) (string, bool) {
	for branch, e := range r.st.Expressed {
		if e.Path == folder {
			return branch, true
		}
	}
	return "", false
}

// Tree returns the line tree from the engine.
func (r *Repo) Tree() ([]change.LineNode, error) {
	nodes, err := r.eng.GetLineTree()
	if err != nil {
		return nil, fmt.Errorf("worktree.Tree: %w", err)
	}
	return nodes, nil
}

// Push projects the change-graph onto git refs and publishes branches + tags to
// the named remote. force overwrites a diverged remote branch.
//
// On a non-fast-forward rejection (the remote advanced since we last synced) and
// when not forcing, Push reconciles automatically: it pulls (fetch + 3-way merge,
// re-materializing folders) and retries the push once. If the merge produced any
// conflict, it stops with a clear "resolve, then push" error rather than retrying,
// leaving the conflict markers on disk for the operator to resolve.
func (r *Repo) Push(remote string, force bool) error {
	// Serialize with the cross-process working-copy lock (#84, sibling of #81):
	// concurrent `cairn push` processes on ONE shared clone otherwise race on
	// the shared push/reconcile state and a branch can silently fail to land.
	// The lock is reentrant, so the diverged-retry path's Pull below shares it.
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	err = r.eng.PushToRemote(remote, force)
	if err == nil || force || !change.IsNonFastForward(err) {
		return err
	}
	// remote diverged → reconcile + retry once
	sum, perr := r.Pull(remote)
	if perr != nil {
		return fmt.Errorf("worktree.Push: %w", perr)
	}
	for _, lr := range sum.Lines {
		if lr.Conflicts > 0 {
			return fmt.Errorf("worktree.Push: %w", ErrPushConflict)
		}
	}
	return r.eng.PushToRemote(remote, force)
}

// PushBranch publishes a SINGLE line (plus tags) to remote — for feeding one
// feature line to a remote to open a PR, without touching other lines (notably
// the remote-tracked main). Unlike Push it does not auto-pull-retry on
// divergence; a diverged remote branch surfaces a guided error naming the
// branch and the remedies (`cairn push --reconcile`, `cairn pull`, or
// `cairn push --force`) so the operator can choose deliberately, rather than
// Push's blanket all-lines auto-reconcile.
func (r *Repo) PushBranch(remote, branch string, force bool) error {
	// Serialize with the cross-process working-copy lock (#84): this is the
	// single-line push the issue's repro exercises (`cairn push origin <branch>`).
	// Concurrent single-line pushes from ONE shared clone otherwise race on the
	// shared push/re-materialize state (tracking refs, projection) and a branch
	// can silently fail to land even though the push reports success.
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := r.eng.LineByName(branch); err != nil {
		return fmt.Errorf("worktree.PushBranch: %w", err)
	}
	err = r.eng.PushToRemoteBranch(remote, branch, force)
	if err != nil && !force && change.IsNonFastForward(err) {
		// Build the final message from THIS layer's branch-scoped remedies
		// (name, --reconcile, cairn pull, --force) plus only the engine's
		// 'cairn undo' hint sentence — not its generic "fetch/sync first or
		// push --force" prose, which would just duplicate the remedies above.
		// %w wraps the underlying (unwrapped) engine error, not the engine's
		// prose, so errors.Is/change.IsNonFastForward and mapRemoteErr's
		// errors.As still see the real cause.
		cause := err
		if u := errors.Unwrap(err); u != nil {
			cause = u
		}
		return fmt.Errorf(
			"worktree.PushBranch: remote branch %q has advanced; run 'cairn push --reconcile' to pull+retry just this line, 'cairn pull' (reconciles ALL lines) then push, or 'cairn push --force' to overwrite. If you folded/committed into this branch locally and didn't mean to, 'cairn undo' rewinds it: %w",
			branch, cause)
	}
	return err
}

// PushBranchReconcile is the opt-in single-line counterpart to Push's
// auto-reconcile (`cairn push --reconcile`): on a non-fast-forward rejection
// it pulls + retries once, exactly like Push, but scoped to ONLY the named
// branch — no other line's tracking refs or catalogue state are touched. If
// the reconcile merge produces conflicts, it stops with a clear "resolve,
// then push" error naming the branch rather than retrying. Never forces.
func (r *Repo) PushBranchReconcile(remote, branch string) error {
	// Serialize with the cross-process working-copy lock (#84), matching Push
	// and PushBranch. Reentrant, so pullBranch below (called while held) is safe.
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := r.eng.LineByName(branch); err != nil {
		return fmt.Errorf("worktree.PushBranchReconcile: %w", err)
	}
	err = r.eng.PushToRemoteBranch(remote, branch, false)
	if err == nil || !change.IsNonFastForward(err) {
		return err
	}
	// remote diverged → reconcile just this line + retry once
	sum, perr := r.pullBranch(remote, branch)
	if perr != nil {
		return fmt.Errorf("worktree.PushBranchReconcile: %w", perr)
	}
	for _, lr := range sum.Lines {
		if lr.Conflicts > 0 {
			return fmt.Errorf("worktree.PushBranchReconcile: branch %q: %w", branch, ErrPushConflict)
		}
	}
	return r.eng.PushToRemoteBranch(remote, branch, false)
}

// pullBranch fetches remote and reconciles ONLY branch (fast-forward or 3-way
// merge, conflicts-as-data), then — if branch is expressed — re-materializes
// just that one folder so disk reflects the result. Every other line's
// catalogue state and expressed folder is left untouched, unlike Pull. Callers
// must already hold the working-copy lock (it does not acquire one itself).
func (r *Repo) pullBranch(remote, branch string) (change.PullSummary, error) {
	sum, err := r.eng.PullFromRemoteBranch(remote, branch)
	if err != nil {
		return sum, fmt.Errorf("worktree.pullBranch: %w", err)
	}
	if entry, ok := r.st.Expressed[branch]; ok {
		line, lerr := r.eng.LineByName(branch)
		if lerr == nil && line.TipCommit != "" {
			dir := filepath.Join(r.root, entry.Path)
			if merr := Materialize(r.eng, r.cacheDir(), line.TipCommit, dir); merr != nil {
				return sum, fmt.Errorf("worktree.pullBranch: %w", merr)
			}
		}
	}
	if err := SaveState(r.stPath, r.st); err != nil {
		return sum, fmt.Errorf("worktree.pullBranch: %w", err)
	}
	return sum, nil
}

// Fetch fetches the named remote into tracking refs (refs/remotes/<remote>/*)
// without reconciling local lines — the read-only half of a pull. Local work is
// never clobbered. Does not prune (unchanged behavior — see FetchPruned).
func (r *Repo) Fetch(remote string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	if err := r.eng.FetchTracking(remote); err != nil {
		return fmt.Errorf("worktree.Fetch: %w", err)
	}
	return nil
}

// FetchPruned is Fetch with pruning on: a tracking ref whose remote-side
// branch was deleted is removed instead of left stale. Used by `pr diff` so a
// PR branch deleted on the remote fails with a clear "not found" error rather
// than silently diffing against the last-known (now-deleted) tip.
func (r *Repo) FetchPruned(remote string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	if err := r.eng.FetchTrackingPruned(remote); err != nil {
		return fmt.Errorf("worktree.FetchPruned: %w", err)
	}
	return nil
}

// Pull fetches the named remote and reconciles every open local line against its
// remote branch (fast-forward or 3-way merge with conflicts-as-data), then
// re-materializes every expressed folder to its line's (possibly merged) tip so
// disk reflects the result. Conflicts are returned as data on the PullSummary,
// not as an error.
func (r *Repo) Pull(remote string) (change.PullSummary, error) {
	unlock, err := r.lockState()
	if err != nil {
		return change.PullSummary{}, err
	}
	defer unlock()
	sum, err := r.eng.PullFromRemote(remote)
	if err != nil {
		return sum, fmt.Errorf("worktree.Pull: %w", err)
	}
	for branch, entry := range r.st.Expressed {
		line, lerr := r.eng.LineByName(branch)
		if lerr != nil || line.TipCommit == "" {
			continue
		}
		dir := filepath.Join(r.root, entry.Path)
		if merr := Materialize(r.eng, r.cacheDir(), line.TipCommit, dir); merr != nil {
			return sum, fmt.Errorf("worktree.Pull: %w", merr)
		}
	}
	if err := SaveState(r.stPath, r.st); err != nil {
		return sum, fmt.Errorf("worktree.Pull: %w", err)
	}
	return sum, nil
}

// AddRemote registers (or re-points) a git remote and records its cairn kind
// ("git" or "cairn"; defaulting to "git" when empty).
func (r *Repo) AddRemote(name, url, kind string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return r.eng.AddRemote(name, url, kind)
}

// Remotes returns every configured remote with its URL and cairn kind.
func (r *Repo) Remotes() ([]change.RemoteInfo, error) { return r.eng.ListRemotes() }

// GetConfig returns the stored value for key; ok is false when unset.
func (r *Repo) GetConfig(key string) (string, bool, error) { return r.eng.GetConfig(key) }

// SetConfig stores value under key.
func (r *Repo) SetConfig(key, value string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return r.eng.SetConfig(key, value)
}

// Ls returns a copy of the currently expressed branch entries.
func (r *Repo) Ls() map[string]Entry {
	out := make(map[string]Entry, len(r.st.Expressed))
	for k, v := range r.st.Expressed {
		out[k] = v
	}
	return out
}

// Root returns the working-copy root directory (for config file resolution).
func (r *Repo) Root() string { return r.root }

// Log returns the commit history for branch (newest first, up to limit entries).
func (r *Repo) Log(branch string, limit int) ([]change.CommitInfo, error) {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return nil, fmt.Errorf("worktree.Log: %w", err)
	}
	if line.TipCommit == "" {
		return nil, nil
	}
	return r.eng.Log(line.TipCommit, limit)
}

// Show returns a commit's metadata and the diff against its first parent.
func (r *Repo) Show(commit string) (change.CommitInfo, []change.FileDiff, error) {
	full, err := r.eng.ResolveCommit(commit)
	if err != nil {
		return change.CommitInfo{}, nil, fmt.Errorf("worktree.Show: %w", err)
	}
	return r.eng.Show(full)
}

// Blame returns per-line provenance for path at the tip of branch.
func (r *Repo) Blame(branch, path string) ([]change.BlameLine, error) {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return nil, fmt.Errorf("worktree.Blame: %w", err)
	}
	if line.TipCommit == "" {
		return nil, fmt.Errorf("worktree.Blame: branch %q has no commits", branch)
	}
	return r.eng.Blame(line.TipCommit, path)
}

// IsWorkingCommit reports whether sha is the head of an open (un-sealed) change.
func (r *Repo) IsWorkingCommit(sha string) (bool, error) { return r.eng.IsWorkingHead(sha) }

// Tag names the tip of branch with the given tag name.
func (r *Repo) Tag(name, branch string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.Tag: %w", err)
	}
	if line.TipCommit == "" {
		return fmt.Errorf("worktree.Tag: branch %q has no commits to tag", branch)
	}
	return r.eng.Tag(name, line.TipCommit, r.author)
}

// MarkPrivate withholds a path (and everything beneath it) from every push. omit
// (the default) drops it from the pushed projection entirely; shapeOnly keeps the
// path but replaces its bytes with a placeholder.
func (r *Repo) MarkPrivate(path string, shapeOnly bool) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	mode := change.PrivacyOmit
	if shapeOnly {
		mode = change.PrivacyShapeOnly
	}
	return r.eng.MarkPrivate(path, mode)
}

// UnmarkPrivate stops withholding a path. Idempotent.
func (r *Repo) UnmarkPrivate(path string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return r.eng.UnmarkPrivate(path)
}

// ListPrivate returns every privacy flag, ordered by path.
func (r *Repo) ListPrivate() ([]change.PrivateEntry, error) { return r.eng.ListPrivate() }

// PathOnRemote returns the remote-tracking refs that already carry path (short
// names like "origin/main"), so the CLI can warn that withholding won't remove
// an already-pushed copy.
func (r *Repo) PathOnRemote(path string) ([]string, error) { return r.eng.PathOnRemote(path) }

// MarkEmbargo flags a commit (any revision — full/short sha, tag) as embargoed:
// it and everything after it are held out of the public projection until
// disclosed. Returns the resolved full sha.
func (r *Repo) MarkEmbargo(rev string) (string, error) {
	unlock, err := r.lockState()
	if err != nil {
		return "", err
	}
	defer unlock()
	sha, err := r.eng.ResolveCommit(rev)
	if err != nil {
		return "", err
	}
	return sha, r.eng.MarkEmbargo(sha)
}

// ListEmbargo returns the embargoed commit shas.
func (r *Repo) ListEmbargo() ([]string, error) { return r.eng.ListEmbargo() }

// DiscloseCommit lifts an embargo if rev resolves to an embargoed commit. It
// returns handled=true when it did; handled=false (no error) means rev is not an
// embargoed commit, so the caller can fall back to disclosing a privacy path.
func (r *Repo) DiscloseCommit(rev string) (handled bool, err error) {
	unlock, lerr := r.lockState()
	if lerr != nil {
		return false, lerr
	}
	defer unlock()
	sha, rerr := r.eng.ResolveCommit(rev)
	if rerr != nil {
		return false, nil // not a resolvable commit → not an embargo disclose
	}
	emb, err := r.eng.IsEmbargoed(sha)
	if err != nil {
		return false, err
	}
	if !emb {
		return false, nil
	}
	return true, r.eng.UnmarkEmbargo(sha)
}

// PendingBump returns the recorded explicit bump intent ("" if none).
func (r *Repo) PendingBump() (string, error) {
	v, _, err := r.eng.GetConfig("version.pending_bump")
	return v, err
}

// SetPendingBump records explicit bump intent for the next release.
func (r *Repo) SetPendingBump(level string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return r.eng.SetConfig("version.pending_bump", level)
}

// DeriveInput assembles the facts version.Derive needs for branch.
func (r *Repo) DeriveInput(branch string, cfg version.Config) (version.DeriveInput, error) {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return version.DeriveInput{}, fmt.Errorf("worktree.DeriveInput: %w", err)
	}
	if line.TipCommit == "" {
		return version.DeriveInput{}, fmt.Errorf("worktree.DeriveInput: branch %q has no commits", branch)
	}
	tag, dist, err := r.eng.DescribeVersion(line.TipCommit)
	if err != nil {
		return version.DeriveInput{}, fmt.Errorf("worktree.DeriveInput: %w", err)
	}
	height, err := r.eng.LineHeight(line)
	if err != nil {
		return version.DeriveInput{}, fmt.Errorf("worktree.DeriveInput: %w", err)
	}
	bump, err := r.PendingBump()
	if err != nil {
		return version.DeriveInput{}, fmt.Errorf("worktree.DeriveInput: %w", err)
	}
	short := line.TipCommit
	if len(short) > 7 {
		short = short[:7]
	}
	return version.DeriveInput{
		BaseTag:      tag,
		Distance:     dist,
		LineName:     branch,
		IsTrunk:      line.ParentLine == "",
		LineDistance: height,
		PendingBump:  bump,
		ShortSHA:     short,
		Config:       cfg,
	}, nil
}

// ReleasePort adapts a branch's working copy to release.RepoPort.
func (r *Repo) ReleasePort(branch, eco string) (release.RepoPort, error) {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return nil, fmt.Errorf("worktree.ReleasePort: %w", err)
	}
	return &releaseAdapter{r: r, branch: branch, line: line, eco: eco}, nil
}

type releaseAdapter struct {
	r      *Repo
	branch string
	line   change.Line
	eco    string
}

// Dirty reports whether the release branch has un-sealed work. The release flow
// opens the repo WITHOUT a command-start SyncWorking (openRepo, not
// openRepoSynced) and stamps the manifest on disk without committing, so the
// working change does not yet reflect that stamp. Sync the branch first so the
// working-delta isDirty sees the stamped-but-uncommitted manifest and the
// guardrail refuses a second release on a stamped tree.
func (a *releaseAdapter) Dirty() (bool, error) {
	unlock, err := a.r.lockState()
	if err != nil {
		return false, err
	}
	defer unlock()
	entry, ok := a.r.st.Expressed[a.branch]
	if !ok {
		return false, nil
	}
	if err := a.r.syncBranch(a.branch, entry); err != nil {
		return false, fmt.Errorf("worktree.releaseAdapter.Dirty: %w", err)
	}
	return a.r.isDirty(a.branch)
}

// LatestTag returns the nearest tag reachable via first-parent ancestry — the
// monotonicity base for a release. cairn's linear fold model keeps release tags
// on the trunk's first-parent chain so this finds them; a tag off that chain
// (rebased/forked history) would not constrain the guard. Acceptable for slice 1.
func (a *releaseAdapter) LatestTag() (string, error) {
	tag, _, err := a.r.eng.DescribeVersion(a.line.TipCommit)
	return tag, err
}

func (a *releaseAdapter) ReadManifest(eco string) ([]byte, string, error) {
	p, err := a.manifestPath(eco)
	if err != nil {
		return nil, "", err
	}
	if p == "" {
		return nil, "", nil // tag-only ecosystem (oci/go) or no manifest present
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, p, nil
		}
		return nil, "", fmt.Errorf("worktree.ReadManifest: %w", err)
	}
	return b, p, nil
}

// manifestPath resolves the manifest file to stamp for an ecosystem within the
// branch folder, or "" when there is nothing to stamp (oci/go are tag-only; a
// missing nuget .csproj is tolerated).
func (a *releaseAdapter) manifestPath(eco string) (string, error) {
	dir := filepath.Join(a.r.root, a.branch)
	switch eco {
	case "npm":
		return filepath.Join(dir, "package.json"), nil
	case "pypi":
		return filepath.Join(dir, "pyproject.toml"), nil
	case "nuget":
		matches, err := filepath.Glob(filepath.Join(dir, "*.csproj"))
		if err != nil {
			return "", fmt.Errorf("worktree.manifestPath: %w", err)
		}
		if len(matches) == 0 {
			return "", nil
		}
		return matches[0], nil // first .csproj; single-project assumption for slice 1
	default:
		return "", nil
	}
}

func (a *releaseAdapter) WriteManifest(path string, b []byte) error {
	unlock, err := a.r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return os.WriteFile(path, b, 0o644)
}

// CreateTag delegates to Repo.Tag, which itself takes the wc.lock; the lock
// here is redundant (reentrant) but kept explicit so this entry point reads
// correctly in isolation.
func (a *releaseAdapter) CreateTag(name string) error {
	unlock, err := a.r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return a.r.Tag(name, a.branch)
}

// DeleteTag writes the catalogue directly (bypassing Repo.Tag), so it takes
// its own lock.
func (a *releaseAdapter) DeleteTag(name string) error {
	unlock, err := a.r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return a.r.eng.DeleteTag(name)
}

// ClearPendingBump delegates to Repo.SetPendingBump, which itself takes the
// wc.lock; the lock here is redundant (reentrant) but kept explicit.
func (a *releaseAdapter) ClearPendingBump() error {
	unlock, err := a.r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return a.r.SetPendingBump("")
}

func (a *releaseAdapter) TagExists(name string) (bool, error) {
	tags, err := a.r.eng.ListTags()
	if err != nil {
		return false, err
	}
	for _, t := range tags {
		if t.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// Undo reverts the most recent operation (restoring each line's tip to its prior
// state) and re-materializes every expressed branch so disk matches the catalogue.
// Phase-1 limitation: it restores line tips only — it does not delete lines that
// the undone operation created.
func (r *Repo) Undo() error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	if err := r.eng.Undo(); err != nil {
		return fmt.Errorf("worktree.Undo: %w", err)
	}
	// eng.Undo restored line tips but NOT change heads. The open change's stale
	// head can still point at the undone commit, which would resurface it on the
	// next snapshot (it would be picked as the working commit's parent). Reconcile
	// each expressed branch's open change head against the restored line tip:
	//   - tip == ""            → clear head (start fresh on the empty baseline)
	//   - tip IS our working   → keep head=tip (amend continues on the restored tip)
	//   - tip is a different/   → clear head, so the next SnapshotWorking parents a
	//     sealed commit          fresh working commit ON TOP of the restored tip,
	//                            never on the undone commit.
	for branch, entry := range r.st.Expressed {
		line, err := r.eng.LineByName(branch)
		if err != nil {
			if errors.Is(err, change.ErrNotFound) {
				continue // line gone — nothing to reconcile/re-materialize
			}
			return fmt.Errorf("worktree.Undo: %w", err)
		}
		tip := line.TipCommit
		switch {
		case tip == "":
			if err := r.eng.SetWorkingHead(entry.ChangeID, ""); err != nil {
				return fmt.Errorf("worktree.Undo: %w", err)
			}
		default:
			cid, err := r.eng.ChangeIDOf(tip)
			if err != nil {
				return fmt.Errorf("worktree.Undo: %w", err)
			}
			if cid == entry.ChangeID {
				// The restored tip IS this change's working commit → amend continues.
				if err := r.eng.SetWorkingHead(entry.ChangeID, tip); err != nil {
					return fmt.Errorf("worktree.Undo: %w", err)
				}
			} else {
				// Restored tip is a different/sealed commit (or carries no trailer):
				// start a fresh working commit on top of it (parent = tip).
				if err := r.eng.SetWorkingHead(entry.ChangeID, ""); err != nil {
					return fmt.Errorf("worktree.Undo: %w", err)
				}
			}
		}
		dir := filepath.Join(r.root, entry.Path)
		if line.TipCommit != "" {
			if err := Materialize(r.eng, r.cacheDir(), line.TipCommit, dir); err != nil {
				return fmt.Errorf("worktree.Undo: %w", err)
			}
		} else {
			// Restored to the pre-first-commit baseline: the committed tree is
			// empty, so the expressed folder must be emptied to match.
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("worktree.Undo: %w", err)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("worktree.Undo: %w", err)
			}
		}
	}
	return nil
}

// BisectActive reports whether a bisect session is in progress.
func (r *Repo) BisectActive() (bool, error) { return r.eng.BisectActive() }

// BisectStatus returns the active session's status (Active=false when none).
func (r *Repo) BisectStatus() (change.BisectInfo, error) { return r.eng.BisectInfo() }

// BisectStart begins a bisect on branch between known-good and known-bad commits.
// It refuses if the branch has un-sealed work (the session would shadow it), then
// materializes the first midpoint into the branch folder for the operator to test.
func (r *Repo) BisectStart(branch, good, bad string) (change.BisectStep, error) {
	unlock, err := r.lockState()
	if err != nil {
		return change.BisectStep{}, err
	}
	defer unlock()
	dirty, err := r.isDirty(branch)
	if err != nil {
		return change.BisectStep{}, fmt.Errorf("worktree.BisectStart: %w", err)
	}
	if dirty {
		return change.BisectStep{}, errors.New("worktree.BisectStart: stash or commit your work before bisecting")
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return change.BisectStep{}, fmt.Errorf("worktree.BisectStart: %w", err)
	}
	if good, err = r.eng.ResolveCommit(good); err != nil {
		return change.BisectStep{}, fmt.Errorf("worktree.BisectStart: good: %w", err)
	}
	if bad, err = r.eng.ResolveCommit(bad); err != nil {
		return change.BisectStep{}, fmt.Errorf("worktree.BisectStart: bad: %w", err)
	}
	step, err := r.eng.BisectStart(line.ID, branch, good, bad)
	if err != nil {
		return change.BisectStep{}, fmt.Errorf("worktree.BisectStart: %w", err)
	}
	// An immediate-done start creates NO session and tested nothing — leave the
	// folder at the working tip rather than materializing a historical commit with
	// no session to suspend the auto-snapshot. Only a real midpoint is shown.
	if !step.Done {
		if err := r.materializeBisect(branch, step); err != nil {
			return change.BisectStep{}, err
		}
	}
	return step, nil
}

// BisectMark records the verdict ("good"|"bad") for the current commit and
// materializes the next midpoint (or the first-bad answer on Done).
func (r *Repo) BisectMark(verdict string) (change.BisectStep, error) {
	unlock, err := r.lockState()
	if err != nil {
		return change.BisectStep{}, err
	}
	defer unlock()
	info, err := r.eng.BisectInfo()
	if err != nil {
		return change.BisectStep{}, fmt.Errorf("worktree.BisectMark: %w", err)
	}
	if !info.Active {
		return change.BisectStep{}, errors.New("worktree.BisectMark: no bisect in progress")
	}
	step, err := r.eng.BisectMark(verdict)
	if err != nil {
		return change.BisectStep{}, fmt.Errorf("worktree.BisectMark: %w", err)
	}
	if err := r.materializeBisect(info.Branch, step); err != nil {
		return change.BisectStep{}, err
	}
	return step, nil
}

// BisectSkip steps over an untestable midpoint and materializes the new current.
func (r *Repo) BisectSkip() (change.BisectStep, error) {
	unlock, err := r.lockState()
	if err != nil {
		return change.BisectStep{}, err
	}
	defer unlock()
	info, err := r.eng.BisectInfo()
	if err != nil {
		return change.BisectStep{}, fmt.Errorf("worktree.BisectSkip: %w", err)
	}
	if !info.Active {
		return change.BisectStep{}, errors.New("worktree.BisectSkip: no bisect in progress")
	}
	step, err := r.eng.BisectSkip()
	if err != nil {
		return change.BisectStep{}, fmt.Errorf("worktree.BisectSkip: %w", err)
	}
	if err := r.materializeBisect(info.Branch, step); err != nil {
		return change.BisectStep{}, err
	}
	return step, nil
}

// BisectReset is the sole place a bisect session is cleared. It restores the
// session branch's folder to the recorded restore tip (the line tip captured at
// start) and deletes the session. The session stays alive through the done state
// (after convergence) precisely so the auto-snapshot stays suspended until here —
// so reset always finds a live session unless none was ever started.
func (r *Repo) BisectReset() error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	info, err := r.eng.BisectInfo()
	if err != nil {
		return fmt.Errorf("worktree.BisectReset: %w", err)
	}
	if !info.Active {
		return errors.New("worktree.BisectReset: no bisect in progress")
	}
	branch := info.Branch
	tip, err := r.eng.BisectReset()
	if err != nil {
		return fmt.Errorf("worktree.BisectReset: %w", err)
	}
	if entry, ok := r.st.Expressed[branch]; ok && tip != "" {
		if err := Materialize(r.eng, r.cacheDir(), tip, filepath.Join(r.root, entry.Path)); err != nil {
			return fmt.Errorf("worktree.BisectReset: %w", err)
		}
	}
	return nil
}

// materializeBisect puts the step's current commit (the midpoint to test, or the
// first-bad when Done) into the branch's expressed folder. A non-expressed branch
// or empty target is a no-op.
func (r *Repo) materializeBisect(branch string, step change.BisectStep) error {
	target := step.Current
	if step.Done {
		target = step.FirstBad
	}
	if target == "" {
		return nil
	}
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return nil
	}
	if err := Materialize(r.eng, r.cacheDir(), target, filepath.Join(r.root, entry.Path)); err != nil {
		return fmt.Errorf("worktree.materializeBisect: %w", err)
	}
	return nil
}

// rematerialize refreshes the expressed folder to the branch's current line tip.
func (r *Repo) rematerialize(branch string, entry Entry) error {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.rematerialize: %w", err)
	}
	if line.TipCommit != "" {
		if err := Materialize(r.eng, r.cacheDir(), line.TipCommit, filepath.Join(r.root, entry.Path)); err != nil {
			return fmt.Errorf("worktree.rematerialize: %w", err)
		}
	}
	return nil
}

// Stash shelves the expressed branch's current working delta (un-sealed edits)
// onto the stash stack with message, then re-materializes the folder to the
// sealed tip (folder is reset to the clean committed state). Errors "nothing to
// stash" when the working change has no un-sealed edits.
func (r *Repo) Stash(branch, message string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return fmt.Errorf("worktree.Stash: branch %q is not expressed", branch)
	}
	if err := r.syncBranch(branch, entry); err != nil {
		return err
	}
	if _, err := r.eng.StashPush(entry.ChangeID, message); err != nil {
		return fmt.Errorf("worktree.Stash: %w", err)
	}
	return r.rematerialize(branch, entry)
}

// StashPop restores the most recent stash entry onto the expressed branch's
// working change, then re-materializes the folder so the restored files appear
// on disk. The stash entry is dropped after a successful apply.
func (r *Repo) StashPop(branch string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return fmt.Errorf("worktree.StashPop: branch %q is not expressed", branch)
	}
	if err := r.syncBranch(branch, entry); err != nil {
		return err
	}
	if err := r.eng.StashApply(entry.ChangeID, 0, true); err != nil {
		return fmt.Errorf("worktree.StashPop: %w", err)
	}
	return r.rematerialize(branch, entry)
}

// StashList returns the stash stack newest-first, passing through to the engine.
func (r *Repo) StashList() ([]change.StashEntry, error) { return r.eng.StashList() }

// StashDrop deletes a stash entry without applying it. id 0 drops the top.
func (r *Repo) StashDrop(id int64) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return r.eng.StashDrop(id)
}

// OperationLog returns the full operation log in chronological order.
func (r *Repo) OperationLog() ([]change.Operation, error) {
	return r.eng.OperationLog()
}

// applyEdit runs an engine edit verb (reword/squash/drop) identified by name,
// then re-materializes every expressed branch whose line was affected. It
// resolves the branch name from the commit sha BEFORE the edit (squash/drop
// deletes the change row) and returns the new line tip plus any rebase
// conflicts as a CommitResult.
func (r *Repo) applyEdit(commit string, editFn func() ([]change.Conflict, error)) (change.CommitResult, error) {
	// Resolve which branch (line name) owns this commit BEFORE the edit,
	// because squash/drop deletes the change row.
	branchName, err := r.lineNameOfCommit(commit)
	if err != nil {
		return change.CommitResult{}, err
	}

	conflicts, err := editFn()
	if err != nil {
		return change.CommitResult{}, err
	}

	// Re-materialize any expressed branch whose line name matches.
	if entry, ok := r.st.Expressed[branchName]; ok {
		if merr := r.rematerialize(branchName, entry); merr != nil {
			return change.CommitResult{}, merr
		}
	}

	// Read the new line tip to return in the result.
	line, err := r.eng.LineByName(branchName)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.applyEdit: %w", err)
	}
	return change.CommitResult{HeadCommit: line.TipCommit, Conflicts: conflicts}, nil
}

// lineNameOfCommit resolves the line name that owns the given commit sha by
// walking all expressed branches and checking the engine's line tip. For
// expressed branches the line is in the expressed set; for non-expressed lines
// we fall back to a full scan of engine lines (future work). The commit must
// carry a Change-Id trailer; the engine's GetChange lookup does the resolution.
func (r *Repo) lineNameOfCommit(commit string) (string, error) {
	// Ask the engine: the commit carries a Change-Id trailer that maps back to a
	// change row which carries the line_id. Use LineByName to go from line_id to
	// the line name — but we need the reverse direction. Walk expressed branches
	// first (cheap), then fall back to a full engine query via the change row.
	//
	// Strategy: resolve via the engine's full short-sha / change-id path:
	// Engine.Reword/Squash/Drop call guardEditable which maps commit → change →
	// line. We replicate just enough here to get the line name.
	//
	// We ask every expressed branch's line whether its tip (or any sealed commit
	// on it) includes this sha. The simplest reliable approach: call
	// eng.LineByName for each expressed branch and check if the sha appears in
	// its log. That would be O(N*depth). Instead we use the change catalogue:
	// the commit's Change-Id points to a change row with a line_id.
	//
	// Since we need the change package's internal accessor we expose a thin
	// wrapper on Engine: LineOfCommit.
	name, err := r.eng.LineOfCommit(commit)
	if err != nil {
		return "", fmt.Errorf("worktree.lineNameOfCommit: %w", err)
	}
	return name, nil
}

// Reword changes the commit message of a sealed commit on its line.
// The branch folder is re-materialized to the rebased tip.
func (r *Repo) Reword(commit, message string) (change.CommitResult, error) {
	unlock, err := r.lockState()
	if err != nil {
		return change.CommitResult{}, err
	}
	defer unlock()
	return r.applyEdit(commit, func() ([]change.Conflict, error) {
		return r.eng.Reword(commit, message)
	})
}

// Squash folds a sealed commit into its parent on the same line.
// The branch folder is re-materialized to the rebased tip.
func (r *Repo) Squash(commit string) (change.CommitResult, error) {
	unlock, err := r.lockState()
	if err != nil {
		return change.CommitResult{}, err
	}
	defer unlock()
	return r.applyEdit(commit, func() ([]change.Conflict, error) {
		return r.eng.Squash(commit)
	})
}

// Drop removes a sealed commit from its line, rebasing later commits.
// The branch folder is re-materialized to the rebased tip.
func (r *Repo) Drop(commit string) (change.CommitResult, error) {
	unlock, err := r.lockState()
	if err != nil {
		return change.CommitResult{}, err
	}
	defer unlock()
	return r.applyEdit(commit, func() ([]change.Conflict, error) {
		return r.eng.Drop(commit)
	})
}

// Reauthor rewrites the author/committer identity of every matching commit across
// the whole repo (see change.Reauthor). Because only identity changes — trees are
// preserved — the on-disk content of every expressed folder is byte-identical
// afterward; we still re-materialize each so its working state tracks the new
// commit SHAs. A dry run touches nothing.
func (r *Repo) Reauthor(spec change.ReauthorSpec) (change.ReauthorResult, error) {
	unlock, err := r.lockState()
	if err != nil {
		return change.ReauthorResult{}, err
	}
	defer unlock()
	res, err := r.eng.Reauthor(spec)
	if err != nil {
		return change.ReauthorResult{}, err
	}
	if spec.DryRun {
		return res, nil
	}
	for branch, entry := range r.st.Expressed {
		if merr := r.rematerialize(branch, entry); merr != nil {
			return change.ReauthorResult{}, merr
		}
	}
	return res, nil
}

// CherryPick applies the delta of the given commit onto branch as a new sealed
// commit, then rebases the open working change on top. The result carries the
// new line tip and any conflicts (conflicts-as-data). If branch is expressed its
// folder is re-materialized to reflect the pick.
func (r *Repo) CherryPick(branch, commit string) (change.CommitResult, error) {
	unlock, err := r.lockState()
	if err != nil {
		return change.CommitResult{}, err
	}
	defer unlock()
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
	}
	commit, err = r.eng.ResolveCommit(commit)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
	}
	newID, conflicts, err := r.eng.CherryPick(commit, line.ID)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
	}
	// Pick-level conflicts are recorded on the NEW sealed change the pick mints,
	// but resolve/status operate on the working change W (entry.ChangeID). Mirror
	// Commit: reassign the pick conflicts from newID onto W so they're reachable.
	// (W-rebase conflicts are already on W, so this consolidates ALL conflicts.)
	if len(conflicts) > 0 {
		if entry, ok := r.st.Expressed[branch]; ok {
			if err := r.eng.ReassignConflicts(newID, entry.ChangeID); err != nil {
				return change.CommitResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
			}
		}
	}
	if entry, ok := r.st.Expressed[branch]; ok {
		if err := r.rematerialize(branch, entry); err != nil {
			return change.CommitResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
		}
	}
	line, err = r.eng.LineByName(branch)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.CherryPick: %w", err)
	}
	return change.CommitResult{HeadCommit: line.TipCommit, Conflicts: conflicts}, nil
}

// isDirty reports whether the branch has un-sealed work: a non-empty delta in
// its OPEN working change vs that change's parent commit. Under working-copy-is-
// a-commit the expressed folder always equals the working commit (line tip) after
// SyncWorking, so a disk-vs-tip comparison is always clean and would silently let
// abandon/fold discard unsealed edits. Comparing the working commit against its
// parent instead catches exactly the work a destructive op would lose.
//
// It returns (false, nil) when the branch is not expressed, when the engine line/
// change is gone (ErrNotFound — already folded/abandoned), or when the open change
// has no working commit yet, so callers running a dirty-check before a destructive
// op can safely proceed.
//
// Callers reach isDirty via openRepoSynced (SyncWorking runs first), so the
// working commit already reflects the latest on-disk edits.
func (r *Repo) isDirty(branch string) (bool, error) {
	entry, ok := r.st.Expressed[branch]
	if !ok {
		// Branch not in working-copy state: nothing to compare, not dirty.
		return false, nil
	}
	ch, err := r.eng.GetChange(entry.ChangeID)
	if err != nil {
		if errors.Is(err, change.ErrNotFound) {
			return false, nil // line/change gone — treat as clean
		}
		return false, fmt.Errorf("worktree.isDirty: %w", err)
	}
	if ch.HeadCommit == "" {
		return false, nil // no working commit yet → nothing unsealed
	}
	parent, err := r.eng.FirstParent(ch.HeadCommit)
	if err != nil {
		return false, fmt.Errorf("worktree.isDirty: %w", err)
	}
	diffs, err := r.eng.DiffCommits(parent, ch.HeadCommit)
	if err != nil {
		return false, fmt.Errorf("worktree.isDirty: %w", err)
	}
	if len(diffs) > 0 {
		return true, nil
	}
	// DiffCommits is content-only, so a mode-only delta (e.g. chmod +x) shows no
	// file diff yet is still un-sealed work. Compare the working commit's modes
	// against its parent's so a chmod-only change is caught.
	headModes, err := r.eng.FileModes(ch.HeadCommit)
	if err != nil {
		return false, fmt.Errorf("worktree.isDirty: %w", err)
	}
	var parentModes map[string]change.EntryMode
	if parent != "" {
		parentModes, err = r.eng.FileModes(parent)
		if err != nil {
			return false, fmt.Errorf("worktree.isDirty: %w", err)
		}
	}
	return !sameModes(parentModes, headModes), nil
}

// sameModes compares two sparse mode maps (absent ⇒ regular). They are equal
// iff they carry the same keys with the same values.
func sameModes(a, b map[string]change.EntryMode) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		if vb, ok := b[k]; !ok || vb != va {
			return false
		}
	}
	return true
}

// trackedSet turns a committed file map's keys into a set of tracked paths for
// Scan. A nil files map yields a nil set ("nothing tracked").
func trackedSet(files map[string][]byte) map[string]struct{} {
	if files == nil {
		return nil
	}
	tracked := make(map[string]struct{}, len(files))
	for k := range files {
		tracked[k] = struct{}{}
	}
	return tracked
}
