package worktree

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/release"
	"github.com/CarriedWorldUniverse/cairn/internal/version"
)

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
	name := author
	if v, ok, _ := eng.GetConfig("user.name"); ok && v != "" {
		name = v
	}
	email := ""
	if v, ok, _ := eng.GetConfig("user.email"); ok && v != "" {
		email = v
	} else if v := os.Getenv("CAIRN_EMAIL"); v != "" {
		email = v
	} else if v := os.Getenv("GIT_AUTHOR_EMAIL"); v != "" {
		email = v
	}
	eng.SetIdentity(name, email)
	return name
}

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

// Express materializes a branch as a folder under root, creating its line if it
// does not exist. For the root line, branch must equal change.RootLineName and
// parent is ignored. For any other branch, an absent line is forked off parent
// (defaulting to the structural root). Re-expressing an already-expressed
// branch is a no-op.
func (r *Repo) Express(branch, parent string) error {
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

	// Do the filesystem work FIRST so a failed materialize/mkdir cannot leave a
	// dangling change with no wc.json entry. CreateChange has no observable side
	// effect until Express records it below, so deferring it past the FS op is
	// safe and avoids the leak.
	dir := filepath.Join(r.root, branch)
	if line.TipCommit != "" {
		if err := Materialize(r.eng, r.cacheDir(), line.TipCommit, dir); err != nil {
			return fmt.Errorf("worktree.Express: %w", err)
		}
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("worktree.Express: %w", err)
	}

	ch, err := r.eng.CreateChange(line.ID, r.author)
	if err != nil {
		return fmt.Errorf("worktree.Express: %w", err)
	}

	r.st.Expressed[branch] = Entry{Path: branch, ChangeID: ch.ID}
	if err := SaveState(r.stPath, r.st); err != nil {
		return fmt.Errorf("worktree.Express: %w", err)
	}
	return nil
}

// Commit snapshots the on-disk contents of an expressed branch onto its change
// (adopting the parent line via the engine's merge-forward) and re-materializes
// the resulting head, so the folder reflects any merged-in parent state. The
// CommitResult carries the new head and any conflicts recorded.
func (r *Repo) Commit(branch, message string) (change.CommitResult, error) {
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: branch %q is not expressed", branch)
	}
	dir := filepath.Join(r.root, entry.Path)
	files, err := Scan(dir)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err)
	}
	res, err := r.eng.Commit(entry.ChangeID, files, message)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err)
	}
	ch, err := r.eng.GetChange(entry.ChangeID)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err)
	}
	if ch.HeadCommit != "" {
		if err := Materialize(r.eng, r.cacheDir(), ch.HeadCommit, dir); err != nil {
			return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err)
		}
	}

	// Best-effort, opt-in auto-sync AFTER the commit. The commit has already
	// succeeded above; the pull is purely additive and never alters res or the
	// (nil) error returned here. Any sync failure (offline, fetch error, conflict)
	// is captured as a note for the CLI, not propagated.
	r.lastSyncNote = r.autoSync()

	return res, nil
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
	if !force {
		dirty, derr := r.isDirty(branch)
		if derr != nil {
			return fmt.Errorf("worktree.Fold: %w", derr)
		}
		if dirty {
			return fmt.Errorf("worktree.Fold: branch %q has uncommitted changes; commit them or pass --force to discard", branch)
		}
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.Fold: %w", err)
	}
	parentLineID := line.ParentLine
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
	if !force {
		dirty, derr := r.isDirty(branch)
		if derr != nil {
			return fmt.Errorf("worktree.Abandon: %w", derr)
		}
		if dirty {
			return fmt.Errorf("worktree.Abandon: branch %q has uncommitted changes; commit them or pass --force to discard", branch)
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
			return fmt.Errorf("worktree.Unexpress: branch %q has uncommitted changes; commit them or pass --force to discard", branch)
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
func (r *Repo) Resolve(branch, path string) error {
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return fmt.Errorf("worktree.Resolve: branch %q is not expressed", branch)
	}
	dir := filepath.Join(r.root, entry.Path)
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
	if err != nil {
		return fmt.Errorf("worktree.Resolve: %w", err)
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
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return StatusInfo{}, fmt.Errorf("worktree.Status: branch %q is not expressed", branch)
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
	ahead, err := r.eng.LineHeight(line)
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

// WorkingDiff returns the per-path diff between an expressed branch's committed
// tip tree (HEAD) and its current on-disk contents (working). A branch with no
// commits yet diffs the working folder against the empty tree.
func (r *Repo) WorkingDiff(branch string) ([]change.FileDiff, error) {
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return nil, fmt.Errorf("worktree.WorkingDiff: branch %q is not expressed", branch)
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return nil, fmt.Errorf("worktree.WorkingDiff: %w", err)
	}
	var committed map[string][]byte
	if line.TipCommit != "" {
		committed, err = r.eng.Files(line.TipCommit)
		if err != nil {
			return nil, fmt.Errorf("worktree.WorkingDiff: %w", err)
		}
	} else {
		committed = map[string][]byte{}
	}
	working, err := Scan(filepath.Join(r.root, entry.Path))
	if err != nil {
		return nil, fmt.Errorf("worktree.WorkingDiff: %w", err)
	}
	return change.DiffTrees(committed, working, "HEAD", "working"), nil
}

// DiffCommits returns the per-path diff between two commits, passing through to
// the change engine.
func (r *Repo) DiffCommits(a, b string) ([]change.FileDiff, error) {
	diffs, err := r.eng.DiffCommits(a, b)
	if err != nil {
		return nil, fmt.Errorf("worktree.DiffCommits: %w", err)
	}
	return diffs, nil
}

// DefaultBranch returns the name of the structural root line (the parent_line IS
// NULL line), whatever it is called. After a clone of a remote whose default
// branch is e.g. "master", this is "master" rather than the literal "main".
func (r *Repo) DefaultBranch() (string, error) {
	root, err := r.eng.RootLine()
	if err != nil {
		return "", fmt.Errorf("worktree.DefaultBranch: %w", err)
	}
	return root.Name, nil
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
	err := r.eng.PushToRemote(remote, force)
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

// Fetch fetches the named remote into tracking refs (refs/remotes/<remote>/*)
// without reconciling local lines — the read-only half of a pull. Local work is
// never clobbered.
func (r *Repo) Fetch(remote string) error {
	if err := r.eng.FetchTracking(remote); err != nil {
		return fmt.Errorf("worktree.Fetch: %w", err)
	}
	return nil
}

// Pull fetches the named remote and reconciles every open local line against its
// remote branch (fast-forward or 3-way merge with conflicts-as-data), then
// re-materializes every expressed folder to its line's (possibly merged) tip so
// disk reflects the result. Conflicts are returned as data on the PullSummary,
// not as an error.
func (r *Repo) Pull(remote string) (change.PullSummary, error) {
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
func (r *Repo) AddRemote(name, url, kind string) error { return r.eng.AddRemote(name, url, kind) }

// Remotes returns every configured remote with its URL and cairn kind.
func (r *Repo) Remotes() ([]change.RemoteInfo, error) { return r.eng.ListRemotes() }

// GetConfig returns the stored value for key; ok is false when unset.
func (r *Repo) GetConfig(key string) (string, bool, error) { return r.eng.GetConfig(key) }

// SetConfig stores value under key.
func (r *Repo) SetConfig(key, value string) error { return r.eng.SetConfig(key, value) }

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
	return r.eng.Show(commit)
}

// Tag names the tip of branch with the given tag name.
func (r *Repo) Tag(name, branch string) error {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.Tag: %w", err)
	}
	if line.TipCommit == "" {
		return fmt.Errorf("worktree.Tag: branch %q has no commits to tag", branch)
	}
	return r.eng.Tag(name, line.TipCommit, r.author)
}

// PendingBump returns the recorded explicit bump intent ("" if none).
func (r *Repo) PendingBump() (string, error) {
	v, _, err := r.eng.GetConfig("version.pending_bump")
	return v, err
}

// SetPendingBump records explicit bump intent for the next release.
func (r *Repo) SetPendingBump(level string) error {
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

func (a *releaseAdapter) Dirty() (bool, error) { return a.r.isDirty(a.branch) }

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
	return os.WriteFile(path, b, 0o644)
}

func (a *releaseAdapter) CreateTag(name string) error { return a.r.Tag(name, a.branch) }
func (a *releaseAdapter) DeleteTag(name string) error { return a.r.eng.DeleteTag(name) }
func (a *releaseAdapter) ClearPendingBump() error     { return a.r.SetPendingBump("") }

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

// isDirty reports whether the expressed folder differs from the branch's
// committed tip tree. It returns (false, nil) when the branch is not expressed
// or when the engine line is gone (ErrNotFound — already folded/abandoned), so
// callers that run a dirty-check before a destructive op can safely proceed.
func (r *Repo) isDirty(branch string) (bool, error) {
	entry, ok := r.st.Expressed[branch]
	if !ok {
		// Branch not in working-copy state: nothing to compare, not dirty.
		return false, nil
	}
	scanned, err := Scan(filepath.Join(r.root, entry.Path))
	if err != nil {
		return false, fmt.Errorf("worktree.isDirty: %w", err)
	}
	line, err := r.eng.LineByName(branch)
	if errors.Is(err, change.ErrNotFound) {
		// Line already folded/abandoned: no committed tip to diff against, so the
		// folder cannot be "dirty" in a recoverable sense — treat as clean and
		// allow removal.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("worktree.isDirty: %w", err)
	}
	var committed map[string][]byte
	if line.TipCommit != "" {
		committed, err = r.eng.Files(line.TipCommit)
		if err != nil {
			return false, fmt.Errorf("worktree.isDirty: %w", err)
		}
	}
	return !sameFiles(scanned, committed), nil
}

func sameFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || !bytes.Equal(va, vb) {
			return false
		}
	}
	return true
}
