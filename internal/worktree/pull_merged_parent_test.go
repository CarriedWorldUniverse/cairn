package worktree

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// A PR branch that merged its parent in upstream: after `cairn pull`, the
// files the merge brought in must land at their committed content and must
// NOT show as local changes. Reported on Windows: the files main changed
// showed M after the pull. It runs on the Windows runner on purpose — every
// other pull test skips there — so its temp dirs tolerate a late unlink.

// looseTempDir is t.TempDir without failing the test when Windows still holds
// a handle at cleanup time.
func looseTempDir(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("", "cairn-pull-merged-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=o", "-c", "user.email=o@x", "-c", "core.autocrlf=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeIn(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPullOfAPRThatMergedMainShowsNoPhantomChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	origin := looseTempDir(t)
	gitIn(t, origin, "init", "-q", "-b", "main")
	writeIn(t, origin, "shared.txt", "one\ntwo\nthree\n")
	writeIn(t, origin, "mine.txt", "local\n")
	gitIn(t, origin, "add", ".")
	gitIn(t, origin, "commit", "-qm", "seed")
	gitIn(t, origin, "checkout", "-qb", "pr")
	writeIn(t, origin, "pr.txt", "pr\n")
	gitIn(t, origin, "add", ".")
	gitIn(t, origin, "commit", "-qm", "pr1")
	gitIn(t, origin, "checkout", "-q", "main")

	root := filepath.Join(looseTempDir(t), "wc")
	r, err := Clone(origin, root, "tester", nil)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.Express("pr", ""); err != nil {
		t.Fatalf("Express: %v", err)
	}
	dir := filepath.Join(r.Root(), r.st.Expressed["pr"].Path)
	// A genuine local edit, snapshotted, as a status run would.
	writeIn(t, dir, "mine.txt", "edited here\n")
	if _, err := r.Status("pr"); err != nil {
		t.Fatalf("Status: %v", err)
	}

	// Upstream: main changes a file and adds one; the PR merges main twice,
	// the second time with a PR edit to the same file (the PR's side wins).
	writeIn(t, origin, "shared.txt", "one\nTWO\nthree\n")
	writeIn(t, origin, "added.txt", "added on main\n")
	gitIn(t, origin, "add", ".")
	gitIn(t, origin, "commit", "-qm", "main change")
	gitIn(t, origin, "checkout", "-q", "pr")
	gitIn(t, origin, "merge", "-q", "--no-edit", "main")
	gitIn(t, origin, "checkout", "-q", "main")
	writeIn(t, origin, "shared.txt", "one\nTWO\nTHREE\n")
	gitIn(t, origin, "commit", "-qam", "main change 2")
	gitIn(t, origin, "checkout", "-q", "pr")
	gitIn(t, origin, "merge", "-q", "--no-edit", "main")
	writeIn(t, origin, "added.txt", "added on main, then the PR's own\n")
	gitIn(t, origin, "commit", "-qam", "pr override")
	gitIn(t, origin, "checkout", "-q", "main")

	if _, err := r.Pull("origin"); err != nil {
		t.Fatalf("Pull: %v", err)
	}

	for _, name := range []string{"shared.txt", "added.txt", "pr.txt"} {
		want := gitShow(t, origin, "pr:"+name)
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s on disk = %q, want the committed %q", name, got, want)
		}
	}
	check := func(when string, rr *Repo) {
		t.Helper()
		st, err := rr.Status("pr")
		if err != nil {
			t.Fatalf("%s Status: %v", when, err)
		}
		if len(st.Added) != 0 || len(st.Deleted) != 0 || !reflect.DeepEqual(st.Modified, []string{"mine.txt"}) {
			t.Fatalf("%s: status A=%v M=%v D=%v, want only M [mine.txt] — the pulled files show as local changes",
				when, st.Added, st.Modified, st.Deleted)
		}
	}
	check("after pull", r)
	// A fresh process, as the operator's next command would be.
	_ = r.Close()
	r2, err := Open(root, "tester")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = r2.Close() }()
	check("reopened", r2)
}

func gitShow(t *testing.T, dir, spec string) []byte {
	t.Helper()
	cmd := exec.Command("git", "show", spec)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git show %s: %v", spec, err)
	}
	return out
}
