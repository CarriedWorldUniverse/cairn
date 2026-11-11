//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/userconfig"
)

// P0 #1/#2: a line name becomes a folder; it must never name the metadata
// dir, the repo root, or anything outside <repo>/<folder>.
func TestSafety_BadLineNamesRefused(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "a\n")
	w.ok("main", "commit", "-m", "a")
	parent := filepath.Dir(w.root)
	before, _ := os.ReadDir(parent)
	for _, name := range []string{".cairn", "", ".", "..", ".hidden", "a..b", "x.lock", "a b", "-x", "/abs", "a/../b", "@{u}", "tr/"} {
		for _, verb := range [][]string{
			{"express", "--repo", w.root, name},
			{"branch", "--repo", w.root, name},
			{"switch", "--repo", w.root, "-c", name},
			{"checkout", "--repo", w.root, "-b", name},
		} {
			if err := run(verb); err == nil {
				t.Errorf("%v: accepted invalid line name %q", verb, name)
			}
		}
	}
	for _, n := range []string{"main", ".cairn"} {
		if _, err := os.Stat(filepath.Join(w.root, n)); err != nil {
			t.Fatalf("%s vanished: %v", n, err)
		}
	}
	if !w.exists("main", "a.txt") {
		t.Fatal("main/a.txt lost")
	}
	after, _ := os.ReadDir(parent)
	if len(after) != len(before) {
		t.Fatalf("parent dir changed: %d -> %d entries", len(before), len(after))
	}
	// The repo still works.
	w.ok("main", "status")
	// Renames/copies are held to the same rules.
	if err := run([]string{"branch", "--repo", w.root, "-m", "main", ".cairn"}); err == nil {
		t.Error("branch -m accepted .cairn")
	}
	if err := run([]string{"branch", "--repo", w.root, "-c", "main", ".."}); err == nil {
		t.Error("branch -c accepted ..")
	}
}

// P0 #3: git commits a file named with a backslash on Unix; so must cairn.
func TestSafety_BackslashFilename(t *testing.T) {
	w := newWC(t)
	w.write("main", `back\slash.txt`, "hi\n")
	w.write("main", "ok.txt", "ok\n")
	w.ok("main", "commit", "-m", "x")
	w.ok("main", "status")
	out := captureRun(t, "ls-tree", "--repo", w.dir("main"), "-r", "--name-only", "main")
	if !strings.Contains(out, `"back\\slash.txt"`) { // git quotes it too
		t.Fatalf("ls-tree lacks the file: %q", out)
	}
	// Survives express/materialize round trip.
	mustRun(t, "express", "--repo", w.root, "f2")
	if !w.exists("f2", `back\slash.txt`) {
		t.Fatal("not materialized in f2")
	}
}

// P0 #4: init into a non-empty dir would strand its files outside every line.
func TestSafety_InitRefusesNonEmptyDir(t *testing.T) {
	d := filepath.Join(t.TempDir(), "p")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"init", d})
	if err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("init into non-empty dir: err=%v", err)
	}
	if _, serr := os.Stat(filepath.Join(d, ".cairn")); serr == nil {
		t.Fatal(".cairn created despite refusal")
	}
	// An empty dir is fine.
	e := filepath.Join(t.TempDir(), "e")
	if err := os.MkdirAll(e, 0o755); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "init", e)
}

// twoChildren builds main with children a and b, each with a sealed commit.
func twoChildren(t *testing.T) *wc {
	w := newWC(t)
	w.write("main", "base.txt", "base\n")
	w.ok("main", "commit", "-m", "base")
	mustRun(t, "express", "--repo", w.root, "a")
	mustRun(t, "express", "--repo", w.root, "b")
	w.write("a", "a.txt", "a\n")
	w.ok("a", "commit", "-m", "work a")
	w.write("b", "b.txt", "b\n")
	w.ok("b", "commit", "-m", "work b")
	return w
}

// P1 #5: merging a sibling from inside a line must not silently fold it into
// main (and delete its folder).
func TestSafety_MergeSiblingRefused(t *testing.T) {
	w := twoChildren(t)
	msg := w.fails("a", "merge", "b")
	if !strings.Contains(msg, "not a child of a") {
		t.Fatalf("message = %q", msg)
	}
	if !w.exists("b", "b.txt") {
		t.Fatal("b was folded away")
	}
	if w.exists("main", "b.txt") {
		t.Fatal("b leaked into main")
	}
	// The legitimate forms still work: from main, and from the root dir.
	w.ok("main", "merge", "a")
	if !w.exists("main", "a.txt") {
		t.Fatal("a not folded into main")
	}
	mustRun(t, "fold", "--repo", w.root, "b")
	if !w.exists("main", "b.txt") {
		t.Fatal("b not folded into main")
	}
}

func TestSafety_FoldFlags(t *testing.T) {
	needGit(t)
	w := twoChildren(t)
	// --ff-only refuses when main moved after a last adopted it.
	w.write("main", "m.txt", "m\n")
	w.ok("main", "commit", "-m", "main moves")
	if msg := w.fails("main", "merge", "--ff-only", "a"); !strings.Contains(msg, "fast-forward") {
		t.Fatalf("--ff-only: %q", msg)
	}
	if !w.exists("a", "a.txt") {
		t.Fatal("refused fold removed a")
	}
	// -m names the adoption merge; --no-ff on a pure fast-forward adds a merge.
	w.ok("main", "merge", "-m", "land a please", "a")
	sha := w.head("main")
	if got := w.store("log", "-1", "--format=%s", sha); !strings.Contains(got, "land a please") {
		t.Fatalf("fold -m: subject %q", got)
	}
	w.ok("b", "commit", "-m", "adopt main") // b adopts main (now includes a)
	w.write("b", "b2.txt", "b2\n")
	w.ok("b", "commit", "-m", "work b2")
	w.ok("main", "merge", "--no-ff", "-m", "Merge b", "b")
	sha = w.head("main")
	parents := strings.Fields(w.store("rev-list", "--parents", "-n1", sha))
	if len(parents) != 3 {
		t.Fatalf("--no-ff head %s has %d parents, want 2: %v", sha, len(parents)-1, parents)
	}
	if got := strings.TrimSpace(w.store("log", "-1", "--format=%s", sha)); got != "Merge b" {
		t.Fatalf("subject %q", got)
	}
}

func TestSafety_MergeSquashAbortContinueTranslated(t *testing.T) {
	w := twoChildren(t)
	for _, flag := range []string{"--squash", "--abort", "--continue"} {
		msg := w.fails("main", "merge", flag, "a")
		if strings.Contains(msg, "flag provided but not defined") || msg == "" {
			t.Errorf("%s: raw flag error %q", flag, msg)
		}
	}
	if !w.exists("a", "a.txt") {
		t.Fatal("a vanished")
	}
}

// P1 #10: the adoption merge written when the parent advanced must not
// repeat the child's subject.
func TestSafety_AdoptionMergeHasOwnMessage(t *testing.T) {
	needGit(t)
	w := twoChildren(t)
	w.write("main", "m.txt", "m\n")
	w.ok("main", "commit", "-m", "main moves")
	w.write("a", "a2.txt", "a2\n")
	w.ok("a", "commit", "-m", "second a")
	sha := w.head("a")
	if got := strings.TrimSpace(w.store("log", "-1", "--format=%s", sha)); got != "adopt main into a" {
		t.Fatalf("adoption subject = %q", got)
	}
	if got := strings.TrimSpace(w.store("log", "-2", "--format=%s", sha)); !strings.Contains(got, "second a") {
		t.Fatalf("stamped commit lost: %q", got)
	}
	// amend/reword still see the operator's message, not the adoption's.
	w.write("a", "a3.txt", "a3\n")
	w.ok("a", "commit", "--amend", "--no-edit")
	if got := w.store("log", "--format=%s", "-3", w.head("a")); !strings.Contains(got, "second a") {
		t.Fatalf("amend --no-edit lost the message: %q", got)
	}
}

// P1 #6: a relative local remote URL must be stored absolute, or a later
// fetch from inside the clone resolves it against the wrong directory.
func TestSafety_CloneRelativeURLStoredAbsolute(t *testing.T) {
	needGit(t)
	w := newWC(t)
	w.write("main", "f.txt", "x\n")
	w.ok("main", "commit", "-m", "one")
	base := t.TempDir()
	bare := filepath.Join(base, "repo.git")
	gitIn(t, base, "init", "-q", "--bare", "repo.git")
	mustRun(t, "remote", "add", "--repo", w.root, "origin", bare)
	mustRun(t, "push", "--repo", w.root, "origin")

	t.Chdir(base)
	mustRun(t, "clone", "-q", "./repo.git", "copy")
	out := captureRun(t, "remote", "get-url", "--repo", filepath.Join(base, "copy"), "origin")
	if got := strings.TrimSpace(out); got != bare {
		t.Fatalf("stored url %q, want absolute %q", got, bare)
	}
	// Fetch from inside copy/ (a different cwd than at clone time).
	t.Chdir(filepath.Join(base, "copy", "main"))
	mustRun(t, "fetch", "origin")
	// remote add / set-url canonicalize too.
	t.Chdir(base)
	mustRun(t, "remote", "add", "--repo", filepath.Join(base, "copy"), "rel", "repo.git")
	out = captureRun(t, "remote", "get-url", "--repo", filepath.Join(base, "copy"), "rel")
	if got := strings.TrimSpace(out); got != bare {
		t.Fatalf("remote add stored %q, want %q", got, bare)
	}
}

// P1 #7: commit with nothing to record fails as git does; --allow-empty seals.
func TestSafety_CommitNothingRefused(t *testing.T) {
	needGit(t)
	w := newWC(t)
	w.write("main", "a.txt", "a\n")
	w.ok("main", "commit", "-m", "one")
	before := w.head("main")
	msg := w.fails("main", "commit", "-m", "empty")
	if !strings.Contains(msg, "nothing to commit") {
		t.Fatalf("message %q", msg)
	}
	if w.head("main") != before {
		t.Fatal("an empty commit was sealed")
	}
	// git agrees on the verdict.
	g := t.TempDir()
	gitIn(t, g, "init", "-q")
	if err := os.WriteFile(filepath.Join(g, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, g, "add", ".")
	gitIn(t, g, "commit", "-q", "-m", "one")
	if _, err := gitTry(g, "commit", "-m", "empty"); err == nil {
		t.Fatal("git itself accepted an empty commit?")
	}
	// --allow-empty works, and the next real commit works too.
	w.ok("main", "commit", "--allow-empty", "-m", "empty on purpose")
	if w.head("main") == before {
		t.Fatal("--allow-empty sealed nothing")
	}
	w.write("main", "b.txt", "b\n")
	w.ok("main", "commit", "-m", "real")
	// A child whose parent advanced still adopts via a bare commit.
	mustRun(t, "express", "--repo", w.root, "kid")
	w.write("main", "m.txt", "m\n")
	w.ok("main", "commit", "-m", "main moves")
	w.ok("kid", "commit", "-m", "adopt")
	if !w.exists("kid", "m.txt") {
		t.Fatal("kid did not adopt main")
	}
}

// P1 #9: `push origin <tag>` publishes that tag (and only it).
func TestSafety_PushTagByName(t *testing.T) {
	needGit(t)
	w := newWC(t)
	w.write("main", "f.txt", "x\n")
	w.ok("main", "commit", "-m", "one")
	base := t.TempDir()
	bare := filepath.Join(base, "r.git")
	gitIn(t, base, "init", "-q", "--bare", "r.git")
	mustRun(t, "remote", "add", "--repo", w.root, "origin", bare)
	mustRun(t, "push", "--repo", w.root, "origin")
	mustRun(t, "tag", "--repo", w.root, "v1.0")
	mustRun(t, "tag", "--repo", w.root, "-a", "-m", "second", "v2.0")
	w.ok("main", "push", "origin", "v1.0")
	if got := strings.TrimSpace(gitIn(t, base, "--git-dir", bare, "tag", "-l")); got != "v1.0" {
		t.Fatalf("remote tags after pushing v1.0 = %q", got)
	}
	w.ok("main", "push", "origin", "tag", "v2.0")
	if got := strings.Fields(gitIn(t, base, "--git-dir", bare, "tag", "-l")); len(got) != 2 {
		t.Fatalf("remote tags = %v", got)
	}
}

// P1 #17: --date takes git's forms (±HHMM, "YYYY-MM-DD HH:MM:SS +0000",
// RFC 2822, "@<unix> <tz>"); the instant matches what git records.
func TestSafety_CommitDateForms(t *testing.T) {
	needGit(t)
	t.Setenv("TZ", "UTC")
	dates := []string{
		"2021-05-05T01:02:03+0000",
		"2021-05-05T01:02:03+05:30",
		"2021-05-05T01:02:03Z",
		"2021-05-05 01:02:03 +0000",
		"2021-05-05 01:02:03 -0700",
		"2021-05-05 01:02:03",
		"Wed, 05 May 2021 01:02:03 +0200",
		"Wed May 5 01:02:03 2021 +0000",
		"@1620176523 +0000",
		"1620176523 -0500",
	}
	g := t.TempDir()
	gitIn(t, g, "init", "-q")
	w := newWC(t)
	for i, d := range dates {
		if err := os.WriteFile(filepath.Join(g, "f.txt"), []byte(d), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, g, "add", ".")
		if _, err := gitTry(g, "commit", "-q", "-m", d, "--date", d); err != nil {
			t.Logf("git rejects %q; skipping", d)
			continue
		}
		want := strings.TrimSpace(gitIn(t, g, "log", "-1", "--format=%at %ad", "--date=raw"))
		w.write("main", "f.txt", d)
		w.ok("main", "commit", "-m", d, "--date", d)
		got := strings.TrimSpace(w.store("log", "-1", "--format=%at %ad", "--date=raw", w.head("main")))
		if got != want {
			t.Errorf("#%d %q: cairn %q, git %q", i, d, got, want)
		}
	}
}

// P1 #18: `cd repo/feat && cairn bisect run -- <cmd>` finds the repo root.
func TestSafety_BisectRunFromInsideLineFolder(t *testing.T) {
	skipOnWindows(t)
	root := filepath.Join(t.TempDir(), "repo")
	mustRun(t, "init", root)
	mustRun(t, "express", "--repo", root, "feat")
	var shas []string
	for i := 1; i <= 4; i++ {
		val := "ok\n"
		if i >= 3 {
			val = "bad\n"
		}
		if err := os.WriteFile(filepath.Join(root, "feat", "flag.txt"), []byte(val), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "feat", "n.txt"), []byte(strings.Repeat("x", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		shas = append(shas, commitShaCLI(t, root, "feat"))
	}
	mustRun(t, "bisect", "start", "--repo", root, "--good", shas[0], "--bad", shas[3], "feat")
	t.Chdir(filepath.Join(root, "feat"))
	out, _, err := runOutErr(t, "bisect", "run", "--", "sh", "-c", "grep -q ok flag.txt")
	if err != nil {
		t.Fatalf("bisect run from inside the folder: %v", err)
	}
	if got := strings.TrimSpace(out); got != shas[2] {
		t.Fatalf("first bad = %s, want %s", got, shas[2])
	}
	mustRun(t, "bisect", "reset")
}

// P1 #19: revert -m N works on an adoption merge (decided by parent count).
func TestSafety_RevertMainlineOnAdoptionMerge(t *testing.T) {
	w := twoChildren(t)
	w.write("main", "m.txt", "m\n")
	w.ok("main", "commit", "-m", "main moves")
	w.write("a", "a2.txt", "a2\n")
	w.ok("a", "commit", "-m", "second a") // top is now the adoption merge
	w.ok("a", "revert", "-m", "1", "HEAD")
	if w.exists("a", "m.txt") {
		t.Fatal("revert -m 1 of the adoption merge should drop what the merge brought in (main's m.txt)")
	}
	if !w.exists("a", "a2.txt") {
		t.Fatal("a2.txt lost")
	}
}

// P1 #21: git's global options ahead of the subcommand.
func TestSafety_GlobalOptions(t *testing.T) {
	w := newWC(t)
	w.write("main", "f.txt", "x\n")
	w.ok("main", "commit", "-m", "one")
	w.write("main", "dirty.txt", "d\n")
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)

	out, err := captureRunResult(t, "-C", w.dir("main"), "status", "-s")
	if err != nil || !strings.Contains(out, "dirty.txt") {
		t.Fatalf("-C: out=%q err=%v", out, err)
	}
	t.Chdir(elsewhere)
	out, err = captureRunResult(t, "--git-dir="+filepath.Join(w.root, ".cairn"), "status", "-s")
	if err != nil || !strings.Contains(out, "dirty.txt") {
		t.Fatalf("--git-dir: out=%q err=%v", out, err)
	}
	t.Chdir(elsewhere)
	if _, err = captureRunResult(t, "--git-dir=/nonexistent/.git", "status"); err == nil || !strings.Contains(err.Error(), "--git-dir") {
		t.Fatalf("--git-dir to a non-repo: err=%v", err)
	}
	// -c: transient override, not persisted.
	t.Chdir(w.root)
	out, err = captureRunResult(t, "-c", "user.name=Transient Tom", "config", "--get", "user.name")
	if err != nil || strings.TrimSpace(out) != "Transient Tom" {
		t.Fatalf("-c: out=%q err=%v", out, err)
	}
	userconfigClear(t) // a real process would simply have exited
	out, _ = captureRunResult(t, "config", "--get", "user.name")
	if strings.Contains(out, "Transient") {
		t.Fatalf("-c override was persisted: %q", out)
	}
	// An env GIT_DIR is ignored.
	t.Setenv("GIT_DIR", "/nonexistent")
	if _, err = captureRunResult(t, "status", "-s"); err != nil {
		t.Fatalf("GIT_DIR in env broke status: %v", err)
	}
}

// userconfigClear drops transient -c overrides a test installed.
func userconfigClear(t *testing.T) {
	t.Helper()
	userconfig.ClearOverrides()
}

// P1 #22: an unknown subcommand is a one-line error, not the usage wall.
func TestSafety_UnknownSubcommandOneLine(t *testing.T) {
	out, err := captureRunResult(t, "frobnicate")
	if err == nil || err.Error() != "'frobnicate' is not a cairn command. See 'cairn help'." {
		t.Fatalf("err = %v", err)
	}
	if out != "" {
		t.Fatalf("usage printed to stdout: %q", out[:min(len(out), 80)])
	}
}

func TestSafety_FatalErrorsExit128(t *testing.T) {
	w := newWC(t)
	for _, c := range []struct {
		err  error
		want bool
	}{
		{run([]string{"rev-parse", "--repo", w.root, "nosuchrev"}), true},
		{run([]string{"rm", "--repo", w.dir("main"), "nope.txt"}), true},
		{run([]string{"status", "--repo", t.TempDir()}), true},
		{errConflicts, false},
		{os.ErrNotExist, false},
	} {
		if c.err == nil {
			t.Fatal("expected an error")
		}
		if got := isFatal(c.err); got != c.want {
			t.Errorf("isFatal(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// P2: commit --fixup/--squash name the target as git does.
func TestSafety_CommitFixupSquash(t *testing.T) {
	needGit(t)
	w := newWC(t)
	w.write("main", "a.txt", "a\n")
	w.ok("main", "commit", "-m", "add a")
	target := w.head("main")
	w.write("main", "a.txt", "a2\n")
	w.ok("main", "commit", "--fixup="+target)
	if got := strings.TrimSpace(w.store("log", "-1", "--format=%s", w.head("main"))); got != "fixup! add a" {
		t.Fatalf("fixup subject %q", got)
	}
	w.write("main", "a.txt", "a3\n")
	w.ok("main", "commit", "--squash", target)
	if got := strings.TrimSpace(w.store("log", "-1", "--format=%s", w.head("main"))); got != "squash! add a" {
		t.Fatalf("squash subject %q", got)
	}
	w.write("main", "a.txt", "a4\n")
	w.fails("main", "commit", "--fixup=nosuchrev")
}

// P2: switch -/checkout -/--detach and the git-only verbs get a one-liner.
func TestSafety_GitVerbTranslations(t *testing.T) {
	for _, argv := range [][]string{
		{"switch", "-"}, {"checkout", "-"}, {"switch", "--detach", "HEAD"}, {"checkout", "--detach"},
		{"notes", "add"}, {"bundle", "create"}, {"submodule", "update"}, {"sparse-checkout", "set"},
		{"maintenance", "run"}, {"range-diff", "a", "b"}, {"verify-commit", "HEAD"}, {"verify-tag", "v1"},
		{"update-ref", "refs/heads/x", "HEAD"},
	} {
		out, err := captureRunResult(t, argv...)
		if err == nil || out != "" || strings.Contains(err.Error(), "flag provided") || strings.Count(err.Error(), "\n") > 0 {
			t.Errorf("%v: out=%q err=%v", argv, out, err)
		}
	}
}
