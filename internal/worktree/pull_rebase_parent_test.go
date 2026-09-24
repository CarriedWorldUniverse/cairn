package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Pull keeps a line written against its parent's latest: after the parent
// moves, the line's own (unpublished) commits are replayed onto the new tip —
// a rebase — and a commit that conflicts stops the replay there until it is
// resolved and committed. The reported shape: branch from main, commit, main
// moves, `cairn pull`, resolve — and the branch still did not sit on main.

type rebaseFixture struct {
	t      *testing.T
	origin string
	r      *Repo
	root   string
}

func newRebaseFixture(t *testing.T) *rebaseFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	origin := looseTempDir(t)
	gitIn(t, origin, "init", "-q", "-b", "main")
	writeIn(t, origin, "a.txt", "one\ntwo\nthree\n")
	gitIn(t, origin, "add", ".")
	gitIn(t, origin, "commit", "-qm", "seed")
	gitIn(t, origin, "checkout", "-q", "--detach")
	root := filepath.Join(looseTempDir(t), "wc")
	r, err := Clone(origin, root, "tester", nil)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return &rebaseFixture{t: t, origin: origin, r: r, root: root}
}

// mainMoves commits on origin's main (checked out detached, so the fixture
// can write to it) and leaves HEAD detached again.
func (f *rebaseFixture) mainMoves(msg string, files map[string]string) {
	f.t.Helper()
	gitIn(f.t, f.origin, "checkout", "-q", "main")
	for name, body := range files {
		writeIn(f.t, f.origin, name, body)
	}
	gitIn(f.t, f.origin, "add", ".")
	gitIn(f.t, f.origin, "commit", "-qm", msg)
	gitIn(f.t, f.origin, "checkout", "-q", "--detach")
}

func (f *rebaseFixture) dir(branch string) string {
	return filepath.Join(f.r.Root(), f.r.st.Expressed[branch].Path)
}

func (f *rebaseFixture) express(branch, from string) {
	f.t.Helper()
	if err := f.r.Express(branch, from); err != nil {
		f.t.Fatalf("Express %s: %v", branch, err)
	}
}

func (f *rebaseFixture) commit(branch, msg string, files map[string]string) {
	f.t.Helper()
	for name, body := range files {
		writeIn(f.t, f.dir(branch), name, body)
	}
	if _, err := f.r.Commit(branch, msg); err != nil {
		f.t.Fatalf("Commit %s: %v", branch, err)
	}
}

func (f *rebaseFixture) pull() []string {
	f.t.Helper()
	sum, err := f.r.Pull("origin")
	if err != nil {
		f.t.Fatalf("Pull: %v", err)
	}
	var out []string
	for _, rb := range sum.Rebased {
		out = append(out, rb.Line+":"+rb.Status)
	}
	return out
}

// history is the line's sealed history, newest first, as commit subjects
// (the working commit excluded).
func (f *rebaseFixture) history(branch string) []string {
	f.t.Helper()
	log, err := f.r.Log(branch, 50)
	if err != nil {
		f.t.Fatalf("Log: %v", err)
	}
	var out []string
	for _, c := range log {
		s := strings.SplitN(c.Message, "\n", 2)[0]
		if s == "(working)" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (f *rebaseFixture) read(branch, name string) string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir(branch), name))
	if err != nil {
		f.t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// The report: a conflicting first commit stops the rebase; resolve + commit
// (no message) seals it under its own message and replays the rest; the
// operator's uncommitted file survives; history is linear on main's tip.
func TestPullRebasesOntoTheParentAndStopsAtAConflict(t *testing.T) {
	f := newRebaseFixture(t)
	f.express("feat", "main")
	f.commit("feat", "feat work", map[string]string{"a.txt": "one\ntwo\nthree-feat\n"})
	f.commit("feat", "feat more", map[string]string{"f.txt": "f\n"})
	writeIn(t, f.dir("feat"), "u.txt", "uncommitted\n")
	f.mainMoves("main moved", map[string]string{"a.txt": "one\ntwo\nthree-main\n", "n.txt": "n\n"})

	if got := f.pull(); !reflect.DeepEqual(got, []string{"feat:stopped"}) {
		t.Fatalf("pull rebases = %v, want [feat:stopped]", got)
	}
	st, err := f.r.Status("feat")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.Conflicts, []string{"a.txt"}) || !strings.Contains(st.Rebase, `"feat work"`) {
		t.Fatalf("status at the stop: conflicts=%v rebase=%q", st.Conflicts, st.Rebase)
	}
	if !strings.Contains(f.read("feat", "a.txt"), "<<<<<<<") {
		t.Fatal("the stopped commit's conflict is not on disk")
	}

	writeIn(t, f.dir("feat"), "a.txt", "one\ntwo\nthree-both\n")
	if err := f.r.Resolve("feat", "a.txt", false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	res, err := f.r.Commit("feat", "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.Rebase == nil || res.Rebase.Status != "rebased" {
		t.Fatalf("commit at the stop: rebase = %+v, want rebased", res.Rebase)
	}
	if got, want := f.history("feat"), []string{"feat more", "feat work", "main moved", "seed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %v, want %v — linear, on main's tip, messages kept", got, want)
	}
	for name, want := range map[string]string{"a.txt": "one\ntwo\nthree-both\n", "f.txt": "f\n", "n.txt": "n\n", "u.txt": "uncommitted\n"} {
		if got := f.read("feat", name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	st, err = f.r.Status("feat")
	if err != nil {
		t.Fatal(err)
	}
	if st.Ahead != 2 || st.Rebase != "" || !reflect.DeepEqual(st.Added, []string{"u.txt"}) {
		t.Fatalf("after: ahead=%d rebase=%q added=%v, want 2, none, [u.txt]", st.Ahead, st.Rebase, st.Added)
	}
}

// Two commits that each conflict stop twice; the second stop comes from the
// commit that continued the first.
func TestPullRebaseStopsAgainAtTheNextConflict(t *testing.T) {
	f := newRebaseFixture(t)
	f.express("feat", "main")
	f.commit("feat", "first", map[string]string{"a.txt": "one\ntwo\nthree-1\n"})
	f.commit("feat", "second", map[string]string{"a.txt": "one\ntwo\nthree-2\n"})
	f.mainMoves("main moved", map[string]string{"a.txt": "one\ntwo\nthree-main\n"})
	f.pull()

	writeIn(t, f.dir("feat"), "a.txt", "one\ntwo\nthree-main-1\n")
	if err := f.r.Resolve("feat", "a.txt", false); err != nil {
		t.Fatal(err)
	}
	res, err := f.r.Commit("feat", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Rebase == nil || res.Rebase.Status != "stopped" || res.Rebase.StoppedAt != "second" {
		t.Fatalf("continue: %+v, want stopped at second", res.Rebase)
	}
	writeIn(t, f.dir("feat"), "a.txt", "one\ntwo\nthree-main-2\n")
	if err := f.r.Resolve("feat", "a.txt", false); err != nil {
		t.Fatal(err)
	}
	if res, err = f.r.Commit("feat", ""); err != nil || res.Rebase == nil || res.Rebase.Status != "rebased" {
		t.Fatalf("second continue: %+v, %v", res.Rebase, err)
	}
	if got, want := f.history("feat"), []string{"second", "first", "main moved", "seed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("history = %v, want %v", got, want)
	}
}

// No conflict: the rebase is silent and complete, and uncommitted edits ride
// along. A line nested under another follows its parent's rebased tip.
func TestPullRebaseCleanAndNested(t *testing.T) {
	f := newRebaseFixture(t)
	f.express("feat", "main")
	f.commit("feat", "feat work", map[string]string{"f.txt": "f\n"})
	f.express("sub", "feat")
	f.commit("sub", "sub work", map[string]string{"s.txt": "s\n"})
	writeIn(t, f.dir("sub"), "u.txt", "uncommitted\n")
	f.mainMoves("main moved", map[string]string{"n.txt": "n\n"})

	if got := f.pull(); !reflect.DeepEqual(got, []string{"feat:rebased", "sub:rebased"}) {
		t.Fatalf("pull rebases = %v, want feat then sub rebased", got)
	}
	if got, want := f.history("sub"), []string{"sub work", "feat work", "main moved", "seed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sub history = %v, want %v", got, want)
	}
	if f.read("sub", "n.txt") != "n\n" || f.read("sub", "u.txt") != "uncommitted\n" {
		t.Fatal("sub folder lacks main's file or lost the uncommitted one")
	}
}

// Published work is not rewritten: a pushed line keeps its commits and
// adopts the parent at its next commit, as before.
func TestPullDoesNotRebaseAPushedLine(t *testing.T) {
	f := newRebaseFixture(t)
	f.express("feat", "main")
	f.commit("feat", "feat work", map[string]string{"f.txt": "f\n"})
	if err := f.r.PushBranch("origin", "feat", false); err != nil {
		t.Fatalf("push: %v", err)
	}
	f.mainMoves("main moved", map[string]string{"n.txt": "n\n"})
	got := f.pull()
	if len(got) != 1 || !strings.HasPrefix(got[0], "feat:skipped: pushed") {
		t.Fatalf("pull rebases = %v, want feat skipped as pushed", got)
	}
	if h := f.history("feat"); !reflect.DeepEqual(h, []string{"feat work", "seed"}) {
		t.Fatalf("pushed line was rewritten: %v", h)
	}
}

// Undo of the stopping pull's rebase puts the line and its rebase state back.
func TestUndoAfterARebaseStop(t *testing.T) {
	f := newRebaseFixture(t)
	f.express("feat", "main")
	f.commit("feat", "feat work", map[string]string{"a.txt": "one\ntwo\nthree-feat\n"})
	f.mainMoves("main moved", map[string]string{"a.txt": "one\ntwo\nthree-main\n"})
	f.pull()
	if err := f.r.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	st, err := f.r.Status("feat")
	if err != nil {
		t.Fatal(err)
	}
	if st.Rebase != "" {
		t.Fatalf("rebase still in progress after undo: %q", st.Rebase)
	}
	if got := f.history("feat"); !reflect.DeepEqual(got, []string{"feat work", "seed"}) {
		t.Fatalf("history after undo = %v, want the pre-rebase line", got)
	}
	if got := f.read("feat", "a.txt"); got != "one\ntwo\nthree-feat\n" {
		t.Fatalf("a.txt after undo = %q, want the line's own content", got)
	}
}

// Only uncommitted work conflicts: nothing to stop at — the conflict lands on
// the working change like any other, and resolving it needs no commit.
func TestPullRebaseWithOnlyUncommittedConflict(t *testing.T) {
	f := newRebaseFixture(t)
	f.express("feat", "main")
	writeIn(t, f.dir("feat"), "a.txt", "one\ntwo\nthree-edit\n")
	if _, err := f.r.Status("feat"); err != nil {
		t.Fatal(err)
	}
	f.mainMoves("main moved", map[string]string{"a.txt": "one\ntwo\nthree-main\n"})
	if got := f.pull(); !reflect.DeepEqual(got, []string{"feat:rebased"}) {
		t.Fatalf("pull rebases = %v, want [feat:rebased]", got)
	}
	st, err := f.r.Status("feat")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.Conflicts, []string{"a.txt"}) || st.Rebase != "" {
		t.Fatalf("conflicts=%v rebase=%q, want the working change's conflict and no rebase", st.Conflicts, st.Rebase)
	}
}
