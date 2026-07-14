package change

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func skipOnWindowsSync(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("go-git local-transport flakes under Windows file locking")
	}
}

// originWithCommit creates a bare repo seeded (via a temp working clone) with a
// readme.txt on its default branch, and returns (barePath, defaultBranchName).
func originWithCommit(t *testing.T) (string, string) {
	t.Helper()
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	work := t.TempDir()
	repo, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatalf("PlainInit work: %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{bare}}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "readme.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if _, err := wt.Add("readme.txt"); err != nil {
		t.Fatalf("add readme: %v", err)
	}
	if _, err := wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "o", Email: "o@x"},
	}); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	if err := repo.Push(&git.PushOptions{RemoteName: "origin"}); err != nil {
		t.Fatalf("push seed: %v", err)
	}

	// Determine the default branch name from the working clone's HEAD.
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	return bare, head.Name().Short()
}

// advanceOrigin clones the bare, sets path=content, commits, and pushes back.
func advanceOrigin(t *testing.T, bare, def, path, content string) {
	t.Helper()
	work := t.TempDir()
	repo, err := git.PlainClone(work, false, &git.CloneOptions{
		URL:           bare,
		ReferenceName: plumbing.NewBranchReferenceName(def),
	})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	full := filepath.Join(work, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if _, err := wt.Add(path); err != nil {
		t.Fatalf("add %s: %v", path, err)
	}
	if _, err := wt.Commit("advance", &git.CommitOptions{
		Author: &object.Signature{Name: "o", Email: "o@x"},
	}); err != nil {
		t.Fatalf("commit advance: %v", err)
	}
	if err := repo.Push(&git.PushOptions{}); err != nil {
		t.Fatalf("push advance: %v", err)
	}
}

// assertTwoParents asserts the commit at sha has exactly two parents.
func assertTwoParents(t *testing.T, e *Engine, sha string) {
	t.Helper()
	c, err := e.git.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		t.Fatalf("CommitObject %s: %v", sha, err)
	}
	if len(c.ParentHashes) != 2 {
		t.Fatalf("merge commit %s has %d parents, want 2", sha, len(c.ParentHashes))
	}
}

// assertOneParent asserts a commit is linear (single parent) — a divergent
// reconcile now REBASES rather than merging, so the tip is a normal one-parent
// commit, never a 2-parent "merge remote-tracking".
func assertOneParent(t *testing.T, e *Engine, sha string) {
	t.Helper()
	c, err := e.git.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		t.Fatalf("CommitObject %s: %v", sha, err)
	}
	if len(c.ParentHashes) != 1 {
		t.Fatalf("rebased tip %s has %d parents, want 1 (linear, no merge commit)", sha, len(c.ParentHashes))
	}
}

// mustFilesPlusLocal reads the tip files of commit and adds local.txt.
func mustFilesPlusLocal(t *testing.T, e *Engine, commit string) map[string][]byte {
	t.Helper()
	files, err := e.Files(commit)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	files["local.txt"] = []byte("L\n")
	return files
}

func TestPullFastForward(t *testing.T) {
	skipOnWindowsSync(t)
	bare, def := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil {
		t.Fatalf("import: %v", err)
	}
	// remote advances; local untouched
	advanceOrigin(t, bare, def, "added.txt", "X\n")
	sum, err := e.PullFromRemote("origin")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	root, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	files, err := e.Files(root.TipCommit)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if string(files["added.txt"]) != "X\n" {
		t.Fatalf("ff pull did not bring added.txt: %v", files)
	}
	if len(sum.Lines) == 0 {
		t.Fatalf("empty summary")
	}
	var found bool
	for _, lr := range sum.Lines {
		if lr.Line == def {
			found = true
			if lr.Status != "fast-forward" {
				t.Fatalf("status = %q, want fast-forward", lr.Status)
			}
		}
	}
	if !found {
		t.Fatalf("no LineResult for %q: %+v", def, sum.Lines)
	}
}

func TestPullDivergentCleanMerge(t *testing.T) {
	skipOnWindowsSync(t)
	bare, def := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil {
		t.Fatalf("import: %v", err)
	}
	root, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	// local edits a NEW file on the default line (via a change on it)
	ch, err := e.CreateChange(root.ID, "local")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if _, err := e.Commit(ch.ID, mustFilesPlusLocal(t, e, root.TipCommit), nil, ""); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// remote edits a DIFFERENT new file
	advanceOrigin(t, bare, def, "remote.txt", "R\n")
	sum, err := e.PullFromRemote("origin")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	root2, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	files, err := e.Files(root2.TipCommit)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if string(files["local.txt"]) != "L\n" || string(files["remote.txt"]) != "R\n" {
		t.Fatalf("clean rebase missing a side: %v", files)
	}
	// A divergent reconcile now REBASES: the tip is a linear one-parent commit
	// (local change replayed onto the remote tip), never a 2-parent merge.
	assertOneParent(t, e, root2.TipCommit)
	var ok bool
	for _, lr := range sum.Lines {
		if lr.Line == def {
			ok = true
			if lr.Status != "rebased" {
				t.Fatalf("status = %q, want rebased", lr.Status)
			}
			if lr.Conflicts != 0 {
				t.Fatalf("clean rebase reported %d conflicts", lr.Conflicts)
			}
		}
	}
	if !ok {
		t.Fatalf("no LineResult for %q: %+v", def, sum.Lines)
	}
}

func TestPullDivergentConflict(t *testing.T) {
	skipOnWindowsSync(t)
	bare, def := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil {
		t.Fatalf("import: %v", err)
	}
	root, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	// local edits readme.txt
	ch, err := e.CreateChange(root.ID, "local")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if _, err := e.Commit(ch.ID, map[string][]byte{"readme.txt": []byte("local edit\n")}, nil, ""); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	// remote edits the SAME file's same region differently
	advanceOrigin(t, bare, def, "readme.txt", "remote edit\n")
	sum, err := e.PullFromRemote("origin")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	var lr LineResult
	for _, x := range sum.Lines {
		if x.Line == def {
			lr = x
		}
	}
	if lr.Line != def {
		t.Fatalf("no LineResult for %q: %+v", def, sum.Lines)
	}
	if lr.Conflicts == 0 {
		t.Fatalf("expected conflicts on a same-region edit, got %+v", lr)
	}
	// the conflict is listed on the line's active change
	c, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	var changeID string
	if err := e.db.QueryRow(
		`SELECT id FROM change WHERE line_id=? AND status='open' ORDER BY updated_at DESC LIMIT 1`,
		c.ID).Scan(&changeID); err != nil {
		t.Fatalf("find change: %v", err)
	}
	conflicts, err := e.Conflicts(changeID)
	if err != nil {
		t.Fatalf("Conflicts: %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatalf("Conflicts(%s) listed none", changeID)
	}
}

func TestPullLocalAheadNoOp(t *testing.T) {
	skipOnWindowsSync(t)
	bare, def := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil {
		t.Fatalf("import: %v", err)
	}
	root, _ := e.LineByName(def)
	ch, _ := e.CreateChange(root.ID, "local")
	if _, err := e.Commit(ch.ID, map[string][]byte{"local_ahead.txt": []byte("L\n")}, nil, ""); err != nil {
		t.Fatalf("commit: %v", err)
	}
	before, _ := e.LineByName(def)
	sum, err := e.PullFromRemote("origin") // remote NOT advanced
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	after, _ := e.LineByName(def)
	if after.TipCommit != before.TipCommit {
		t.Fatalf("local-ahead pull rewound/moved tip: %s -> %s", before.TipCommit, after.TipCommit)
	}
	for _, lr := range sum.Lines {
		if lr.Line == def && lr.Status != "up-to-date" {
			t.Fatalf("local-ahead status = %q, want up-to-date", lr.Status)
		}
	}
}

func TestPullNoChangeNoOrphan(t *testing.T) {
	skipOnWindowsSync(t)
	bare, def := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil {
		t.Fatalf("import: %v", err)
	}
	// import does NOT create an open change on the line. Count changes before.
	root, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	var before int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM change WHERE line_id=?`, root.ID).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}
	// up-to-date pull (no remote advance)
	if _, err := e.PullFromRemote("origin"); err != nil {
		t.Fatalf("pull up-to-date: %v", err)
	}
	// remote advances; ff pull
	advanceOrigin(t, bare, def, "added.txt", "X\n")
	if _, err := e.PullFromRemote("origin"); err != nil {
		t.Fatalf("pull ff: %v", err)
	}
	var after int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM change WHERE line_id=?`, root.ID).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != before {
		t.Fatalf("pull created %d orphan change(s) on no-op/ff paths (before=%d after=%d)", after-before, before, after)
	}
	// and the ff landed
	r2, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	files, err := e.Files(r2.TipCommit)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if string(files["added.txt"]) != "X\n" {
		t.Fatalf("ff didn't land: %v", files)
	}
}

// pushNewBranchDirect clones the bare, creates a NEW branch (never imported
// into the engine e, so no local line by that name exists), commits path=
// content, and pushes it straight to the bare — for exercising
// PullFromRemoteBranch's "remote has the branch, but no open local line by
// that name" no-op path.
func pushNewBranchDirect(t *testing.T, bare, branch, path, content string) {
	t.Helper()
	work := t.TempDir()
	repo, err := git.PlainClone(work, false, &git.CloneOptions{URL: bare})
	if err != nil {
		t.Fatalf("clone bare: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Create: true,
	}); err != nil {
		t.Fatalf("checkout -b %s: %v", branch, err)
	}
	full := filepath.Join(work, path)
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if _, err := wt.Add(path); err != nil {
		t.Fatalf("add %s: %v", path, err)
	}
	if _, err := wt.Commit("new branch "+branch, &git.CommitOptions{
		Author: &object.Signature{Name: "o", Email: "o@x"},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs: []config.RefSpec{
			config.RefSpec(plumbing.NewBranchReferenceName(branch) + ":" + plumbing.NewBranchReferenceName(branch)),
		},
	}); err != nil {
		t.Fatalf("push %s: %v", branch, err)
	}
}

// TestPullFromRemoteBranchNoSuchRemoteBranch: the remote has no branch by that
// name — PullFromRemoteBranch is a silent no-op, not an error.
func TestPullFromRemoteBranchNoSuchRemoteBranch(t *testing.T) {
	skipOnWindowsSync(t)
	bare, _ := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil {
		t.Fatalf("import: %v", err)
	}
	sum, err := e.PullFromRemoteBranch("origin", "no-such-branch")
	if err != nil {
		t.Fatalf("PullFromRemoteBranch: %v", err)
	}
	if len(sum.Lines) != 0 {
		t.Fatalf("expected no-op summary, got %+v", sum)
	}
}

// TestPullFromRemoteBranchNoOpenLocalLine: the remote HAS the branch, but no
// open local line by that name exists — a silent no-op (mirrors
// PullFromRemote skipping lines with no remote counterpart, just from the
// other direction).
func TestPullFromRemoteBranchNoOpenLocalLine(t *testing.T) {
	skipOnWindowsSync(t)
	bare, _ := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil {
		t.Fatalf("import: %v", err)
	}
	pushNewBranchDirect(t, bare, "orphan", "orphan.txt", "O\n")
	sum, err := e.PullFromRemoteBranch("origin", "orphan")
	if err != nil {
		t.Fatalf("PullFromRemoteBranch: %v", err)
	}
	if len(sum.Lines) != 0 {
		t.Fatalf("expected no-op summary (no local line 'orphan'), got %+v", sum)
	}
}

// TestPullFromRemoteBranchUpToDate: local already at the remote tip — no-op,
// status "up-to-date", tip unmoved.
func TestPullFromRemoteBranchUpToDate(t *testing.T) {
	skipOnWindowsSync(t)
	bare, def := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil {
		t.Fatalf("import: %v", err)
	}
	root, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	before := root.TipCommit
	sum, err := e.PullFromRemoteBranch("origin", def)
	if err != nil {
		t.Fatalf("PullFromRemoteBranch: %v", err)
	}
	root2, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	if root2.TipCommit != before {
		t.Fatalf("up-to-date pull moved the tip: %s -> %s", before, root2.TipCommit)
	}
	if len(sum.Lines) != 1 || sum.Lines[0].Status != "up-to-date" {
		t.Fatalf("summary = %+v, want single up-to-date result", sum.Lines)
	}
}

// TestPullFromRemoteBranchFastForward: remote-only advance adopts the remote
// tip wholesale (fast-forward), scoped to just the named branch.
func TestPullFromRemoteBranchFastForward(t *testing.T) {
	skipOnWindowsSync(t)
	bare, def := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil {
		t.Fatalf("import: %v", err)
	}
	advanceOrigin(t, bare, def, "added.txt", "X\n")
	sum, err := e.PullFromRemoteBranch("origin", def)
	if err != nil {
		t.Fatalf("PullFromRemoteBranch: %v", err)
	}
	root, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	files, err := e.Files(root.TipCommit)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if string(files["added.txt"]) != "X\n" {
		t.Fatalf("ff pull did not bring added.txt: %v", files)
	}
	if len(sum.Lines) != 1 || sum.Lines[0].Status != "fast-forward" {
		t.Fatalf("summary = %+v, want single fast-forward result", sum.Lines)
	}
}

// TestPullFromRemoteBranchDivergentCleanMerge: a clean divergence rebases (as
// PullFromRemote does), scoped to the named branch only.
func TestPullFromRemoteBranchDivergentCleanMerge(t *testing.T) {
	skipOnWindowsSync(t)
	bare, def := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil {
		t.Fatalf("import: %v", err)
	}
	root, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	ch, err := e.CreateChange(root.ID, "local")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if _, err := e.Commit(ch.ID, mustFilesPlusLocal(t, e, root.TipCommit), nil, ""); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	advanceOrigin(t, bare, def, "remote.txt", "R\n")
	sum, err := e.PullFromRemoteBranch("origin", def)
	if err != nil {
		t.Fatalf("PullFromRemoteBranch: %v", err)
	}
	root2, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	files, err := e.Files(root2.TipCommit)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if string(files["local.txt"]) != "L\n" || string(files["remote.txt"]) != "R\n" {
		t.Fatalf("clean rebase missing a side: %v", files)
	}
	assertOneParent(t, e, root2.TipCommit)
	if len(sum.Lines) != 1 || sum.Lines[0].Status != "rebased" || sum.Lines[0].Conflicts != 0 {
		t.Fatalf("summary = %+v, want single clean rebased result", sum.Lines)
	}
}

// TestPullFromRemoteBranchDivergentConflict: a same-region divergence records
// a conflict on the branch's active change, and does not error.
func TestPullFromRemoteBranchDivergentConflict(t *testing.T) {
	skipOnWindowsSync(t)
	bare, def := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil {
		t.Fatalf("import: %v", err)
	}
	root, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	ch, err := e.CreateChange(root.ID, "local")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if _, err := e.Commit(ch.ID, map[string][]byte{"readme.txt": []byte("local edit\n")}, nil, ""); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	advanceOrigin(t, bare, def, "readme.txt", "remote edit\n")
	sum, err := e.PullFromRemoteBranch("origin", def)
	if err != nil {
		t.Fatalf("PullFromRemoteBranch: %v", err)
	}
	if len(sum.Lines) != 1 || sum.Lines[0].Conflicts == 0 {
		t.Fatalf("expected a single conflicted result, got %+v", sum.Lines)
	}
	c, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	var changeID string
	if err := e.db.QueryRow(
		`SELECT id FROM change WHERE line_id=? AND status='open' ORDER BY updated_at DESC LIMIT 1`,
		c.ID).Scan(&changeID); err != nil {
		t.Fatalf("find change: %v", err)
	}
	conflicts, err := e.Conflicts(changeID)
	if err != nil {
		t.Fatalf("Conflicts: %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatalf("Conflicts(%s) listed none", changeID)
	}
}

func TestPullUpToDate(t *testing.T) {
	skipOnWindowsSync(t)
	bare, def := originWithCommit(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.ImportFromRemote(bare); err != nil {
		t.Fatalf("import: %v", err)
	}
	root, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	before := root.TipCommit
	sum, err := e.PullFromRemote("origin")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	root2, err := e.LineByName(def)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	if root2.TipCommit != before {
		t.Fatalf("up-to-date pull moved the tip: %s -> %s", before, root2.TipCommit)
	}
	for _, lr := range sum.Lines {
		if lr.Line == def && lr.Status != "up-to-date" {
			t.Fatalf("status = %q, want up-to-date", lr.Status)
		}
	}
}

// originWithCommitOnMain seeds a bare repo exactly like originWithCommit, but
// forces the pushed branch name to "main" — RootLineName — so it lines up
// with the never-imported root line an Open on an empty dir bootstraps (see
// ensureRootLine: tip_commit=="" on a fresh repo). originWithCommit can't be
// reused as-is here: go-git's PlainInit defaults the first branch to "master"
// pre-first-commit, so a same-named remote branch takes a second checkout -b.
func originWithCommitOnMain(t *testing.T) string {
	t.Helper()
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	work := t.TempDir()
	repo, err := git.PlainInit(work, false)
	if err != nil {
		t.Fatalf("PlainInit work: %v", err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{bare}}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "readme.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if _, err := wt.Add("readme.txt"); err != nil {
		t.Fatalf("add readme: %v", err)
	}
	if _, err := wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "o", Email: "o@x"},
	}); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	// A commit must exist before a branch checkout can target it (checkout -b
	// on a pre-first-commit repo errors "reference not found").
	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("main"),
		Create: true,
	}); err != nil {
		t.Fatalf("checkout -b main: %v", err)
	}
	if err := repo.Push(&git.PushOptions{
		RemoteName: "origin",
		RefSpecs: []config.RefSpec{
			config.RefSpec("refs/heads/main:refs/heads/main"),
		},
	}); err != nil {
		t.Fatalf("push main: %v", err)
	}
	return bare
}

// TestPullEmptyLineAdoptsRemoteTip is #116: a brand-new local line has zero
// commits (tip_commit=="", no snapshotted open change — exactly the root
// line's state right after Open on an empty dir, before any import/commit)
// and its name matches a remote branch that already has commits. The old
// mergeBase("", r)=="" fell through the diverged default branch and tried to
// merge trees against a "" (zero-hash) local tree, erroring "object not
// found". Pull must instead degrade to a fast-forward: adopt the remote tip.
func TestPullEmptyLineAdoptsRemoteTip(t *testing.T) {
	skipOnWindowsSync(t)
	bare := originWithCommitOnMain(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	root, err := e.LineByName(RootLineName)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	if root.TipCommit != "" {
		t.Fatalf("precondition: fresh root line tip_commit = %q, want empty", root.TipCommit)
	}

	if err := e.AddRemote("origin", bare, "git"); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	sum, err := e.PullFromRemote("origin")
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	root2, err := e.LineByName(RootLineName)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	if root2.TipCommit == "" {
		t.Fatalf("empty-line pull left the tip empty")
	}
	rheads, err := e.remoteHeads("origin")
	if err != nil {
		t.Fatalf("remoteHeads: %v", err)
	}
	if root2.TipCommit != rheads[RootLineName] {
		t.Fatalf("line tip = %s, want remote tip %s", root2.TipCommit, rheads[RootLineName])
	}
	files, err := e.Files(root2.TipCommit)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if string(files["readme.txt"]) != "hello\n" {
		t.Fatalf("adopted tip missing remote content: %v", files)
	}

	var found bool
	for _, lr := range sum.Lines {
		if lr.Line == RootLineName {
			found = true
			if lr.Status != "fast-forward" {
				t.Fatalf("status = %q, want fast-forward", lr.Status)
			}
			if lr.Conflicts != 0 {
				t.Fatalf("empty-line adoption reported %d conflicts, want 0", lr.Conflicts)
			}
		}
	}
	if !found {
		t.Fatalf("no LineResult for %q: %+v", RootLineName, sum.Lines)
	}

	// No open change existed before the pull (nothing was ever snapshotted on
	// the root line) and none is created by it — the fast-forward path passes
	// "" as the change id precisely to avoid manufacturing one (see #103: a
	// change adopted as headless re-snapshots later with parent = the new tip,
	// rather than presenting the remote's own commit as local work).
	var changeCount int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM change WHERE line_id=?`, root2.ID).Scan(&changeCount); err != nil {
		t.Fatalf("count changes: %v", err)
	}
	if changeCount != 0 {
		t.Fatalf("empty-line pull created %d change row(s), want 0 (line stays headless)", changeCount)
	}
}

// TestPullEmptyLineWithHeadlessChangeStaysHeadless covers the variant where an
// open change already exists on the empty line but has never been snapshotted
// (head_commit==""). #103's rule applies: the pull must NOT give that change a
// head — only the line tip may advance — so a later folder-sync snapshot still
// parents on the (now-adopted) line tip rather than amending onto the remote
// commit's own parent.
func TestPullEmptyLineWithHeadlessChangeStaysHeadless(t *testing.T) {
	skipOnWindowsSync(t)
	bare := originWithCommitOnMain(t)
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	root, err := e.LineByName(RootLineName)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	ch, err := e.CreateChange(root.ID, "local")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if ch.HeadCommit != "" {
		t.Fatalf("precondition: fresh change head_commit = %q, want empty", ch.HeadCommit)
	}

	if err := e.AddRemote("origin", bare, "git"); err != nil {
		t.Fatalf("AddRemote: %v", err)
	}
	if _, err := e.PullFromRemote("origin"); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	root2, err := e.LineByName(RootLineName)
	if err != nil {
		t.Fatalf("LineByName: %v", err)
	}
	rheads, err := e.remoteHeads("origin")
	if err != nil {
		t.Fatalf("remoteHeads: %v", err)
	}
	if root2.TipCommit != rheads[RootLineName] {
		t.Fatalf("line tip = %s, want remote tip %s", root2.TipCommit, rheads[RootLineName])
	}
	var headAfter string
	if err := e.db.QueryRow(`SELECT head_commit FROM change WHERE id=?`, ch.ID).Scan(&headAfter); err != nil {
		t.Fatalf("query change head: %v", err)
	}
	if headAfter != "" {
		t.Fatalf("headless change head_commit = %q after pull, want it to stay empty", headAfter)
	}
}
