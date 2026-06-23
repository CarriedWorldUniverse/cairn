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
	if _, err := e.Commit(ch.ID, mustFilesPlusLocal(t, e, root.TipCommit)); err != nil {
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
		t.Fatalf("clean merge missing a side: %v", files)
	}
	// merged commit has 2 parents
	assertTwoParents(t, e, root2.TipCommit)
	var ok bool
	for _, lr := range sum.Lines {
		if lr.Line == def {
			ok = true
			if lr.Status != "merged" {
				t.Fatalf("status = %q, want merged", lr.Status)
			}
			if lr.Conflicts != 0 {
				t.Fatalf("clean merge reported %d conflicts", lr.Conflicts)
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
	if _, err := e.Commit(ch.ID, map[string][]byte{"readme.txt": []byte("local edit\n")}); err != nil {
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
	if _, err := e.Commit(ch.ID, map[string][]byte{"local_ahead.txt": []byte("L\n")}); err != nil {
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
