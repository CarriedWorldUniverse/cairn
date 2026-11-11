package worktree

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Ref-shaped verbs: branch / tag / remote / push / fetch / pull options, the
// maintenance verbs, and revision lookup for them. Every mutation takes the
// working-copy lock and records an oplog entry in the engine.

// ResolveRefsRev resolves a revision for the ref verbs: HEAD or empty (the line
// you stand in), a line name (its sealed tip), either followed by ~N / ^N
// suffixes, a tag, a remote-tracking name (origin/x), or any sha / revision
// the object store understands.
func (r *Repo) ResolveRefsRev(rev string) (string, error) {
	if rev == "" {
		rev = "HEAD"
	}
	return r.eng.ResolveCommit(rev)
}

// BranchInfo is one line as `branch` lists it.
type BranchInfo struct {
	Name      string
	Tip       string // sealed tip, "" when nothing is sealed
	Subject   string
	Parent    string
	Expressed bool
	Remote    *change.RemoteState // vs the tree remote's same-named branch
}

// RemoteLabel names the remote the Remote states are measured against.
func (r *Repo) RemoteLabel() string { n, _ := r.TreeRemote(); return n }

// Branches lists every open line with its sealed tip, subject and remote state.
func (r *Repo) Branches() ([]BranchInfo, error) {
	lines, err := r.eng.ListOpenLines()
	if err != nil {
		return nil, err
	}
	_, states, err := r.remoteStates()
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, l := range lines {
		names[l.ID] = l.Name
	}
	out := make([]BranchInfo, 0, len(lines))
	for _, l := range lines {
		tip, err := r.eng.SealedTipOf(l)
		if err != nil {
			return nil, err
		}
		bi := BranchInfo{Name: l.Name, Tip: tip, Parent: names[l.ParentLine]}
		if tip != "" {
			bi.Subject, _ = r.eng.CommitSubject(tip)
		}
		_, bi.Expressed = r.st.Expressed[l.Name]
		if st, ok := states[l.Name]; ok {
			s := st
			bi.Remote = &s
		}
		out = append(out, bi)
	}
	return out, nil
}

// TrackingBranches lists remote-tracking branches: remote -> short name -> sha.
func (r *Repo) TrackingBranches() (map[string]map[string]string, error) {
	rems, err := r.eng.ListRemotes()
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]string{}
	for _, rem := range rems {
		h, err := r.eng.TrackingHeads(rem.Name)
		if err != nil {
			return nil, err
		}
		out[rem.Name] = h
	}
	return out, nil
}

// CommitSubject is the first line of a commit's message.
func (r *Repo) CommitSubject(sha string) string {
	s, _ := r.eng.CommitSubject(sha)
	return s
}

// IsAncestor reports whether a is an ancestor of (or equal to) b.
func (r *Repo) IsAncestor(a, b string) (bool, error) { return r.eng.IsAncestorOf(a, b) }

// moveExpressed re-homes an expressed folder after a line rename (or its undo).
func (r *Repo) moveExpressed(from, to string) error {
	entry, ok := r.st.Expressed[from]
	if !ok {
		return nil
	}
	newFolder := FolderName(to)
	for b, e := range r.st.Expressed {
		if b != from && e.Path == newFolder {
			return fmt.Errorf("folder %q is already used by line %q", newFolder, b)
		}
	}
	if entry.Path != newFolder {
		oldDir, newDir := filepath.Join(r.root, entry.Path), filepath.Join(r.root, newFolder)
		if _, err := os.Stat(oldDir); err == nil {
			if err := renameWithRetry(oldDir, newDir); err != nil {
				return fmt.Errorf("moving folder %s -> %s: %w", entry.Path, newFolder, err)
			}
		}
		entry.Path = newFolder
	}
	delete(r.st.Expressed, from)
	r.st.Expressed[to] = entry
	if _, err := os.Stat(r.wcCachePath(from)); err == nil {
		_ = os.MkdirAll(filepath.Dir(r.wcCachePath(to)), 0o755)
		_ = renameWithRetry(r.wcCachePath(from), r.wcCachePath(to))
	}
	return SaveState(r.stPath, r.st)
}

// RenameLine renames a line (git branch -m / -M) and its expressed folder.
func (r *Repo) RenameLine(oldName, newName string, force bool) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	if err := r.SyncWorking(); err != nil {
		return fmt.Errorf("worktree.RenameLine: %w", err)
	}
	if force {
		if _, expressed := r.st.Expressed[newName]; expressed && newName != oldName {
			return fmt.Errorf("worktree.RenameLine: %q is expressed; unexpress it before replacing it", newName)
		}
	}
	newFolder := FolderName(newName)
	for b, e := range r.st.Expressed {
		if b != oldName && e.Path == newFolder {
			return fmt.Errorf("worktree.RenameLine: folder %q is already used by line %q", newFolder, b)
		}
	}
	// An unrelated directory already sitting at the destination would make the
	// folder move fail AFTER the catalogue rename: check first.
	if entry, ok := r.st.Expressed[oldName]; ok && entry.Path != newFolder {
		if _, err := os.Lstat(filepath.Join(r.root, newFolder)); err == nil {
			return fmt.Errorf("worktree.RenameLine: %q already exists on disk and is not line %q's folder; move it away first", newFolder, oldName)
		}
	}
	if err := r.eng.RenameLine(oldName, newName, force); err != nil {
		return fmt.Errorf("worktree.RenameLine: %w", err)
	}
	if err := r.moveExpressed(oldName, newName); err != nil {
		// Keep the command atomic: put the catalogue back.
		if uerr := r.eng.Undo(); uerr != nil {
			return fmt.Errorf("worktree.RenameLine: %w (and rolling back the rename failed: %v)", err, uerr)
		}
		return fmt.Errorf("worktree.RenameLine: %w (rename rolled back)", err)
	}
	return nil
}

// CopyLine copies a line's sealed tip to a new line (git branch -c).
func (r *Repo) CopyLine(oldName, newName string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return r.eng.CopyLine(oldName, newName)
}

// DeleteBranch removes a line (git branch -d / -D). Without force it refuses a
// line whose work is neither folded into its parent nor pushed. Returns the
// sealed tip it had.
func (r *Repo) DeleteBranch(name string, force bool) (string, error) {
	unlock, err := r.lockState()
	if err != nil {
		return "", err
	}
	defer unlock()
	line, err := r.eng.LineByName(name)
	if err != nil || line.Status == "abandoned" {
		return "", fmt.Errorf("line %q not found", name)
	}
	tip, err := r.eng.SealedTipOf(line)
	if err != nil {
		return "", err
	}
	if !force && line.ParentLine != "" {
		ahead, err := r.eng.AheadOfParent(line)
		if err != nil {
			return "", err
		}
		if ahead > 0 {
			_, states, err := r.remoteStates()
			if err != nil {
				return "", err
			}
			if st, ok := states[name]; !ok || st.Kind != "pushed" {
				return "", fmt.Errorf("the line %q is not fully merged: %d commit(s) are neither folded into its parent nor pushed (if you are sure you want to delete it, run 'cairn branch -D %s')", name, ahead, name)
			}
		}
	}
	if err := r.Abandon(name, force); err != nil {
		return "", err
	}
	return tip, nil
}

// ---- tags ----

// CreateTag tags the commit rev resolves to; with message (or annotated) it
// writes an annotated tag object.
func (r *Repo) CreateTag(name, rev, message string, annotated, force bool) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	sha, err := r.resolveTagTarget(rev)
	if err != nil {
		return fmt.Errorf("worktree.CreateTag: %w", err)
	}
	return r.eng.CreateTag(name, sha, message, annotated, force, r.author)
}

// resolveTagTarget is ResolveRefsRev, except that a bare line name (or HEAD)
// means the line's TIP — its current head, as `tag <name> [branch]` has always
// meant and as version derivation reads it — rather than its sealed tip.
func (r *Repo) resolveTagTarget(rev string) (string, error) {
	base := rev
	if rev == "" || rev == "HEAD" || rev == "@" {
		b, err := r.DefaultBranch()
		if err != nil {
			return "", err
		}
		base = b
	}
	if l, err := r.eng.LineByName(base); err == nil && l.Status != "abandoned" && (base == rev || rev == "" || rev == "HEAD" || rev == "@") {
		if l.TipCommit == "" {
			return "", fmt.Errorf("line %q has no commits to tag", base)
		}
		return l.TipCommit, nil
	}
	return r.ResolveRefsRev(rev)
}

// DeleteTag removes a tag, returning the commit it named.
func (r *Repo) DeleteTag(name string) (string, error) {
	unlock, err := r.lockState()
	if err != nil {
		return "", err
	}
	defer unlock()
	return r.eng.RemoveTag(name)
}

// TagInfos lists tags with annotation and date.
func (r *Repo) TagInfos() ([]change.TagInfo, error) { return r.eng.ListTagInfos() }

// ---- remotes ----

// GetRemote returns one remote.
func (r *Repo) GetRemote(name string) (change.RemoteInfo, error) { return r.eng.GetRemote(name) }

// RemoveRemote deletes a remote.
func (r *Repo) RemoveRemote(name string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return r.eng.RemoveRemote(name)
}

// RenameRemote renames a remote and its tracking refs.
func (r *Repo) RenameRemote(from, to string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return r.eng.RenameRemote(from, to)
}

// SetRemoteURL re-points a remote.
func (r *Repo) SetRemoteURL(name, url string) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	return r.eng.SetRemoteURL(name, url)
}

// ListRemoteRefs is a live listing of what the remote advertises, plus the
// branch its HEAD points at ("" if it does not say).
func (r *Repo) ListRemoteRefs(remote string) (map[string]string, string, error) {
	var refs map[string]string
	var head string
	err := r.withRemoteLock(remote, func() error {
		var e error
		refs, head, e = r.eng.ListRemoteRefs(remote)
		return e
	})
	return refs, head, err
}

// RemoteStatesFor classifies every line against the named remote's tracking
// refs (as of the last fetch or push).
func (r *Repo) RemoteStatesFor(remote string) (map[string]change.RemoteState, error) {
	return r.eng.RemoteStates(remote)
}

// ---- push / fetch / pull options ----

// PushOptions selects what PushWith publishes.
type PushOptions struct {
	Remote   string
	Branch   string // source line; "" = every line
	Dst      string // remote branch name when it differs from Branch
	Force    bool
	Lease    bool // --force-with-lease: force only if the remote still matches the tracking ref
	TagsOnly bool
	Tag      string // publish just this one tag (push <remote> <tag>)
}

// PushWith is Push/PushBranch with the extra git options: tags only, a
// differently-named destination branch, and a lease check.
func (r *Repo) PushWith(o PushOptions) error {
	unlock, err := r.lockState()
	if err != nil {
		return err
	}
	defer unlock()
	force := o.Force
	if o.Lease {
		var heads []string
		if o.Branch != "" {
			dst := o.Dst
			if dst == "" {
				dst = o.Branch
			}
			heads = []string{dst}
		} else if !o.TagsOnly {
			lines, err := r.eng.ListOpenLines()
			if err != nil {
				return err
			}
			for _, l := range lines {
				heads = append(heads, l.Name)
			}
		}
		// A line with no tracking ref is expected ABSENT on the remote, so a
		// remote that has it anyway is stale info (LeaseCheck handles that).
		for _, h := range heads {
			if err := r.eng.LeaseCheck(o.Remote, h); err != nil {
				return fmt.Errorf("worktree.PushWith: %w", err)
			}
		}
	}
	tags := cfgSpec("refs/tags/*:refs/tags/*")
	forceTags := cfgSpec("+refs/tags/*:refs/tags/*")
	var tagSpecs []cfgSpec
	if o.Lease && !force {
		// go-git does not refuse a non-forced tag update, so the lease itself
		// keeps remote tags still: publish only tags the remote lacks (or has
		// identical); a differing one needs an explicit --force.
		live, _, err := r.eng.ListRemoteRefs(o.Remote)
		if err != nil {
			return err
		}
		infos, err := r.eng.ListTagInfos()
		if err != nil {
			return err
		}
		for _, t := range infos {
			v := t.Commit
			if t.Obj != "" {
				v = t.Obj
			}
			if have, ok := live["refs/tags/"+t.Name]; !ok || have == v {
				tagSpecs = append(tagSpecs, cfgSpec("refs/tags/"+t.Name+":refs/tags/"+t.Name))
			} else {
				fmt.Fprintf(os.Stderr, "cairn: not moving remote tag %s (it differs; use --force to overwrite tags)\n", t.Name)
			}
		}
	}
	leaseTags := func(specs ...cfgSpec) []cfgSpec {
		if tagSpecs != nil || o.Lease && !force {
			return append(specs, tagSpecs...)
		}
		return append(specs, tags)
	}
	// prepare pushes specs; the lease forces BRANCHES only (their '+'), never
	// tags and never via the global force option.
	prepare := func(branch string, specs []cfgSpec) error {
		return r.pushViaPin(o.Remote, func() (*change.PreparedPush, error) {
			pp, err := r.eng.PreparePush("worktree.PushWith", o.Remote, branch, toRefSpecs(specs), force || o.Lease)
			if err == nil && o.Lease && !force {
				pp.DisableGlobalForce()
			}
			return pp, err
		})
	}
	hp := ""
	if force || o.Lease {
		hp = "+"
	}
	switch {
	case o.Tag != "":
		spec := cfgSpec(hp + "refs/tags/" + o.Tag + ":refs/tags/" + o.Tag)
		return prepare("", []cfgSpec{spec})
	case o.TagsOnly:
		if o.Lease && !force {
			if len(tagSpecs) == 0 {
				return nil
			}
			return prepare("", tagSpecs)
		}
		ts := tags
		if force {
			ts = forceTags
		}
		return prepare("", []cfgSpec{ts})
	case o.Branch != "" && o.Dst != "" && o.Dst != o.Branch:
		dst := o.Dst
		if !strings.HasPrefix(dst, "refs/") {
			dst = "refs/heads/" + dst
		}
		// Only the named ref: no tags ride along on a src:dst push.
		return prepare(o.Branch, []cfgSpec{cfgSpec(hp + "refs/heads/" + o.Branch + ":" + dst)})
	case o.Lease && o.Branch != "":
		return prepare(o.Branch, leaseTags(cfgSpec("+refs/heads/"+o.Branch+":refs/heads/"+o.Branch)))
	case o.Lease:
		return prepare("", leaseTags(cfgSpec("+refs/heads/*:refs/heads/*")))
	case o.Branch != "":
		return r.pushViaPin(o.Remote, func() (*change.PreparedPush, error) {
			return r.eng.PreparePushToRemoteBranch(o.Remote, o.Branch, force)
		})
	default:
		return r.pushViaPin(o.Remote, func() (*change.PreparedPush, error) {
			return r.eng.PreparePushToRemote(o.Remote, force)
		})
	}
}

func (r *Repo) trackingHead(remote, branch string) (string, bool, error) {
	h, err := r.eng.TrackingHeads(remote)
	if err != nil {
		return "", false, err
	}
	v, ok := h[branch]
	return v, ok, nil
}

// PushDelete removes a branch or tag from the remote (push --delete). name is
// a short name or a full ref; a short name matching both a branch and a tag is
// ambiguous, like git. With lease, a branch delete first checks the remote
// still matches the tracking ref. Returns the full ref deleted.
func (r *Repo) PushDelete(remote, name string, lease bool) (string, error) {
	var ref string
	err := r.withRemoteLock(remote, func() error {
		refs, _, err := r.eng.ListRemoteRefs(remote)
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(name, "refs/"):
			if _, ok := refs[name]; !ok {
				return fmt.Errorf("remote ref %s does not exist on %s", name, remote)
			}
			ref = name
		default:
			_, isHead := refs["refs/heads/"+name]
			_, isTag := refs["refs/tags/"+name]
			switch {
			case isHead && isTag:
				return fmt.Errorf("%q is both a branch and a tag on %s; name it in full (refs/heads/%s or refs/tags/%s)", name, remote, name, name)
			case isHead:
				ref = "refs/heads/" + name
			case isTag:
				ref = "refs/tags/" + name
			default:
				return fmt.Errorf("remote ref %q does not exist on %s", name, remote)
			}
		}
		if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok && lease {
			// --force-with-lease: only delete a branch still where we last saw it.
			if err := r.eng.LeaseCheck(remote, b); err != nil {
				return err
			}
		}
		return r.eng.DeleteRemoteRef(remote, ref)
	})
	return ref, err
}

// FetchBranches fetches only the named branches into their tracking refs.
func (r *Repo) FetchBranches(remote string, branches []string) error {
	return r.withRemoteLock(remote, func() error { return r.eng.FetchTrackingBranches(remote, branches) })
}

// PullBranch is Pull scoped to one line (pull <remote> <branch>).
func (r *Repo) PullBranch(remote, branch string) (change.PullSummary, error) {
	unlock, err := r.lockState()
	if err != nil {
		return change.PullSummary{}, err
	}
	defer unlock()
	if _, err := r.eng.LineByName(branch); err != nil {
		return change.PullSummary{}, fmt.Errorf("worktree.PullBranch: no line named %q", branch)
	}
	return r.pullBranch(remote, branch)
}

// ---- config ----

// UnsetConfig removes a repo config key.
func (r *Repo) UnsetConfig(key string) (bool, error) {
	unlock, err := r.lockState()
	if err != nil {
		return false, err
	}
	defer unlock()
	return r.eng.UnsetConfig(key)
}

// AllConfig lists every repo config key.
func (r *Repo) AllConfig() (map[string]string, error) { return r.eng.AllConfig() }

// ---- maintenance ----

// Fsck verifies object connectivity and catalogue consistency.
func (r *Repo) Fsck() (change.FsckReport, error) { return r.eng.Fsck() }

// CountObjects tallies loose and packed objects.
func (r *Repo) CountObjects() (change.ObjectCounts, error) { return r.eng.CountObjects() }

// GcReport extends the engine's with what the working-copy caches gave back.
type GcReport struct {
	change.GcReport
	CacheFiles int
	CacheBytes int64
	cutoff     time.Time
}

// Gc repacks the object store, vacuums the catalogue and prunes stale
// caches. pruneNow drops the 14-day grace on unreferenced loose objects,
// superseded packs and cache blobs.
func (r *Repo) Gc(pruneNow bool) (GcReport, error) {
	unlock, err := r.lockState()
	if err != nil {
		return GcReport{}, err
	}
	defer unlock()
	if err := r.SyncWorking(); err != nil {
		return GcReport{}, fmt.Errorf("worktree.Gc: %w", err)
	}
	// Fetches write loose objects holding only the per-remote lock, so gc takes
	// every remote lock too (order: wc.lock, then remote locks — a remote-lock
	// holder never waits on wc.lock) before it decides what is reachable.
	rems, err := r.eng.ListRemotes()
	if err != nil {
		return GcReport{}, err
	}
	var out GcReport
	var run func(i int) error
	run = func(i int) error {
		if i < len(rems) {
			return r.withRemoteLock(rems[i].Name, func() error { return run(i + 1) })
		}
		// The cutoff is fixed before the reach set is computed: an object
		// written after this instant is never pruned.
		cutoff := time.Now().Add(-14 * 24 * time.Hour)
		if pruneNow {
			cutoff = time.Now()
		}
		rep, err := r.eng.Gc(cutoff, cutoff)
		out.GcReport = rep
		if err != nil {
			return err
		}
		out.cutoff = cutoff
		return nil
	}
	if err := run(0); err != nil {
		return out, err
	}
	cutoff := out.cutoff
	// wc-cache files of lines that are no longer expressed.
	wcDir := filepath.Join(r.root, ".cairn", "wc-cache")
	if ents, err := os.ReadDir(wcDir); err == nil {
		live := map[string]bool{}
		for b := range r.st.Expressed {
			live[b+".json"] = true
		}
		for _, e := range ents {
			if !e.IsDir() && !live[e.Name()] && !strings.Contains(e.Name(), "/") {
				if info, err := e.Info(); err == nil {
					out.CacheBytes += info.Size()
				}
				if os.Remove(filepath.Join(wcDir, e.Name())) == nil {
					out.CacheFiles++
				}
			}
		}
	}
	// Blob cache entries nobody has touched since the cutoff.
	_ = filepath.WalkDir(r.cacheDir(), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) {
			if os.Remove(p) == nil {
				out.CacheFiles++
				out.CacheBytes += info.Size()
			}
		}
		return nil
	})
	return out, nil
}

// CommitTime is a commit's committer time.
func (r *Repo) CommitTime(commit string) (time.Time, error) { return r.eng.CommitTime(commit) }

// WalkCommitFiles walks the files of a commit's tree (see change.WalkCommitFiles).
func (r *Repo) WalkCommitFiles(commit string, fn func(path string, mode filemode.FileMode, f *object.File) error) (time.Time, error) {
	return r.eng.WalkCommitFiles(commit, fn)
}

// ---- check-ignore ----

// IgnoreMatch is the verdict of CheckIgnore.
type IgnoreMatch struct {
	Ignored bool
	Source  string // ignore file, relative to the line folder
	Line    int
	Pattern string
}

type ignoreRule struct {
	pat    gitignore.Pattern
	source string
	line   int
	text   string
}

// CheckIgnore reports whether rel (slash-separated, relative to the line
// folder `folder` under the repo root) is ignored by that folder's
// .gitignore/.cairnignore files — the same files, the same library and the
// same precedence the working-copy scan uses, so the answer is what a commit
// would do. Like git, an ignored parent directory ignores everything in it.
func (r *Repo) CheckIgnore(folder, rel string) (IgnoreMatch, error) {
	base := filepath.Join(r.root, folder)
	parts := strings.Split(rel, "/")
	isDir := false
	if fi, err := os.Stat(filepath.Join(base, filepath.FromSlash(rel))); err == nil {
		isDir = fi.IsDir()
	}
	var rules []ignoreRule
	loadDir := func(domain []string) error {
		dir := filepath.Join(append([]string{base}, domain...)...)
		for _, name := range []string{".gitignore", ".cairnignore"} {
			f, err := os.Open(filepath.Join(dir, name))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			sc := bufio.NewScanner(f)
			n := 0
			for sc.Scan() {
				n++
				text := sc.Text()
				if text == "" || strings.HasPrefix(text, "#") {
					continue
				}
				src := name
				if len(domain) > 0 {
					src = strings.Join(domain, "/") + "/" + name
				}
				rules = append(rules, ignoreRule{gitignore.ParsePattern(text, domain), src, n, text})
			}
			f.Close()
		}
		return nil
	}
	if err := loadDir(nil); err != nil {
		return IgnoreMatch{}, err
	}
	verdict := func(p []string, dir bool) *ignoreRule {
		var last *ignoreRule
		for i := range rules {
			switch rules[i].pat.Match(p, dir) {
			case gitignore.Exclude:
				last = &rules[i]
			case gitignore.Include:
				last = nil
			}
		}
		return last
	}
	for i := 1; i <= len(parts); i++ {
		if i > 1 {
			if err := loadDir(parts[:i-1]); err != nil {
				return IgnoreMatch{}, err
			}
		}
		d := isDir
		if i < len(parts) {
			d = true
		}
		if m := verdict(parts[:i], d); m != nil {
			return IgnoreMatch{Ignored: true, Source: m.source, Line: m.line, Pattern: m.text}, nil
		}
	}
	return IgnoreMatch{}, nil
}

// ---- reflog ----

// ReflogEntry is one oplog entry that moved a line.
type ReflogEntry struct {
	Sha    string
	Op     string
	Detail string
	Prev   string
}

// Reflog is the oplog seen through one line: every operation that changed the
// line's tip (newest first), as `<line>@{N}` entries.
func (r *Repo) Reflog(line string) ([]ReflogEntry, error) {
	ops, err := r.eng.OperationLog()
	if err != nil {
		return nil, err
	}
	var out []ReflogEntry
	for _, op := range ops {
		if op.OpType == "snapshot" {
			continue // working-copy autosaves, not a ref movement
		}
		before, bok := op.ViewBefore[line]
		after, aok := op.ViewAfter[line]
		if bok == aok && before == after {
			continue
		}
		detail := op.Detail
		if detail == "{}" {
			detail = ""
		}
		out = append(out, ReflogEntry{Sha: after, Op: op.OpType, Detail: detail, Prev: before})
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}
