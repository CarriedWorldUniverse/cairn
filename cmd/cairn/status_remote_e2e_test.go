package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `status` reads its one line's remote state on its own (RemoteStateOf)
// rather than classifying every line as `tree` does. The two must never
// disagree: for each expressed line, in each state a line can be in, what
// status prints is what tree prints.
func TestStatusRemoteStateMatchesTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	bare := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", "--initial-branch=main", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v %s", err, out)
	}
	write := func(branch, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, branch, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustRun(t, "init", root)

	check := func(when string, lines ...string) {
		t.Helper()
		flat := mustRunOut(t, "tree", "--repo", root)
		for _, line := range lines {
			// tree: "<name>  ahead=N  [state]" — state is absent with no remote.
			var treeState string
			for _, l := range strings.Split(flat, "\n") {
				l = strings.TrimLeft(l, "│├└─ ")
				if strings.HasPrefix(l, line+"  ") {
					if i := strings.Index(l, "["); i >= 0 {
						treeState = strings.TrimSuffix(l[i+1:], "]")
					}
				}
			}
			var statusState string
			for _, l := range strings.Split(mustRunOut(t, "status", "--repo", root, line), "\n") {
				if rest, ok := strings.CutPrefix(l, "remote:"); ok {
					statusState = strings.TrimPrefix(strings.TrimSpace(rest), "origin: ")
				}
			}
			if statusState != treeState {
				t.Errorf("%s: line %s: status says %q, tree says %q\n%s", when, line, statusState, treeState, flat)
			}
		}
	}

	write("main", "a.txt", "a\n")
	mustRun(t, "commit", "--repo", root, "main")
	check("no remote", "main")

	mustRun(t, "remote", "add", "origin", bare, "--repo", root)
	check("remote added, nothing pushed", "main")
	mustRun(t, "push", "--repo", root, "origin", "main")

	mustRun(t, "express", "--repo", root, "feat")
	write("feat", "f.txt", "f\n")
	mustRun(t, "commit", "--repo", root, "feat")
	mustRun(t, "push", "--repo", root, "origin", "feat")
	mustRun(t, "express", "--repo", root, "local")
	write("local", "l.txt", "l\n")
	mustRun(t, "commit", "--repo", root, "local")
	check("pushed, pushed, unpushed", "main", "feat", "local")

	write("feat", "g.txt", "g\n")
	mustRun(t, "commit", "--repo", root, "feat")
	write("feat", "h.txt", "h\n")
	mustRun(t, "commit", "--repo", root, "feat")
	check("feat ahead 2", "main", "feat", "local")

	// Someone else pushes to main: after a fetch main is behind.
	other := t.TempDir()
	for _, args := range [][]string{
		{"clone", "-q", "--branch", "main", bare, other},
		{"-C", other, "-c", "user.name=o", "-c", "user.email=o@o", "commit", "-q", "--allow-empty", "-m", "theirs"},
		{"-C", other, "push", "-q", "origin", "HEAD:main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	mustRun(t, "fetch", "--repo", root, "origin")
	check("main behind", "main", "feat", "local")
	if st := mustRunOut(t, "status", "--repo", root, "main"); !strings.Contains(st, "remote:    origin: behind 1") {
		t.Fatalf("expected main to be behind 1:\n%s", st)
	}

	// And a local commit on top of that: diverged.
	write("main", "b.txt", "b\n")
	mustRun(t, "commit", "--repo", root, "main")
	check("main diverged", "main", "feat", "local")
	if st := mustRunOut(t, "status", "--repo", root, "main"); !strings.Contains(st, "remote:    origin: diverged +1/-1") {
		t.Fatalf("expected main to have diverged +1/-1:\n%s", st)
	}

	// The remote deletes feat: gone, once the tracking ref is pruned.
	if out, err := exec.Command("git", "--git-dir", bare, "update-ref", "-d", "refs/heads/feat").CombinedOutput(); err != nil {
		t.Fatalf("delete remote branch: %v %s", err, out)
	}
	mustRun(t, "tree", "--fetch", "--repo", root)
	check("feat gone", "main", "feat", "local")
	if st := mustRunOut(t, "status", "--repo", root, "feat"); !strings.Contains(st, "remote:    origin: gone") {
		t.Fatalf("expected feat to be gone:\n%s", st)
	}
}

// `tree` and `ls` no longer snapshot the working copy first — nothing they
// print depends on it. An edit made before them must still be there, still
// un-snapshotted work, for the next command that does look: nothing is lost
// by not looking.
func TestTreeAndLsLeaveTheWorkingCopyForTheNextCommand(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "main")
	if err := os.WriteFile(filepath.Join(root, "main", "a.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main", "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	treeOut := mustRunOut(t, "tree", "--repo", root)
	if !strings.Contains(treeOut, "main  ahead=0") {
		t.Fatalf("tree: %s", treeOut)
	}
	if ls := mustRunOut(t, "ls", "--repo", root); !strings.Contains(ls, "main") {
		t.Fatalf("ls: %s", ls)
	}
	st := mustRunOut(t, "status", "--repo", root, "main")
	if !strings.Contains(st, "M a.txt") || !strings.Contains(st, "A new.txt") {
		t.Fatalf("status after tree/ls lost the working edits:\n%s", st)
	}
	mustRun(t, "commit", "--repo", root, "main", "-m", "second")
	if log := mustRunOut(t, "log", "--repo", root, "main"); !strings.Contains(log, "second") {
		t.Fatalf("the edits made before tree/ls were not committed:\n%s", log)
	}
}
