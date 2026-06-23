package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// Repo is the working-copy orchestrator that bridges expressed branch folders on
// disk and the cairn change engine. Each expressed branch is a folder under root
// holding the materialized files of an open change on the corresponding line.
type Repo struct {
	root   string
	author string
	eng    *change.Engine
	st     *State
	stPath string
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
}

// Open opens (creating if needed) the working copy rooted at root with the given
// default author. The change engine lives under root/.cairn and the working-copy
// state under root/.cairn/wc.json. On first run the root line ("main") is
// expressed automatically.
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
	r := &Repo{root: root, author: author, eng: eng, st: st, stPath: stPath}
	if _, ok := st.Expressed[change.RootLineName]; !ok {
		if err := r.Express(change.RootLineName, ""); err != nil {
			_ = eng.Close()
			return nil, err
		}
	}
	return r, nil
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
// (defaulting to "main"). Re-expressing an already-expressed branch is a no-op.
func (r *Repo) Express(branch, parent string) error {
	if _, ok := r.st.Expressed[branch]; ok {
		return nil
	}

	var line change.Line
	if branch == change.RootLineName {
		l, err := r.eng.LineByName(change.RootLineName)
		if err != nil {
			return fmt.Errorf("worktree.Express: %w", err)
		}
		line = l
	} else {
		l, err := r.eng.LineByName(branch)
		switch {
		case err == nil:
			line = l
		case errors.Is(err, change.ErrNotFound):
			if parent == "" {
				parent = change.RootLineName
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
func (r *Repo) Commit(branch, _msg string) (change.CommitResult, error) {
	entry, ok := r.st.Expressed[branch]
	if !ok {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: branch %q is not expressed", branch)
	}
	dir := filepath.Join(r.root, entry.Path)
	files, err := Scan(dir)
	if err != nil {
		return change.CommitResult{}, fmt.Errorf("worktree.Commit: %w", err)
	}
	res, err := r.eng.Commit(entry.ChangeID, files)
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
	return res, nil
}

// Fold folds an expressed branch's line back into its parent, fast-forwarding the
// parent tip, then unexpresses the branch. Any expressed line whose ID is the
// folded line's parent is re-materialized to the new parent tip so its folder
// reflects the adopted work.
func (r *Repo) Fold(branch string) error {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.Fold: %w", err)
	}
	parentLineID := line.ParentLine
	if err := r.eng.FoldLine(line.ID); err != nil {
		return fmt.Errorf("worktree.Fold: %w", err)
	}
	if err := r.Unexpress(branch); err != nil {
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
// unexpresses the branch.
func (r *Repo) Abandon(branch string) error {
	if branch == change.RootLineName {
		return fmt.Errorf("worktree.Abandon: cannot abandon the root line %q", branch)
	}
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.Abandon: %w", err)
	}
	if err := r.eng.AbandonLine(line.ID); err != nil {
		return fmt.Errorf("worktree.Abandon: %w", err)
	}
	return r.Unexpress(branch)
}

// Unexpress removes an expressed branch's folder and forgets it from state.
func (r *Repo) Unexpress(branch string) error {
	if branch == change.RootLineName {
		return fmt.Errorf("worktree.Unexpress: cannot unexpress the root line %q", branch)
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
	ahead := 0
	if line.TipCommit != "" && line.TipCommit != line.BaseCommit {
		ahead = 1
	}
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
	}, nil
}

// Tree returns the line tree from the engine.
func (r *Repo) Tree() ([]change.LineNode, error) {
	nodes, err := r.eng.GetLineTree()
	if err != nil {
		return nil, fmt.Errorf("worktree.Tree: %w", err)
	}
	return nodes, nil
}

// Ls returns a copy of the currently expressed branch entries.
func (r *Repo) Ls() map[string]Entry {
	out := make(map[string]Entry, len(r.st.Expressed))
	for k, v := range r.st.Expressed {
		out[k] = v
	}
	return out
}
