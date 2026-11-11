package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The git-shaped inspection commands are checked against git itself: the same
// history is built with git, cloned with cairn, and each command line is run
// through both — the outputs must be identical, byte for byte.

const parityRepoScript = `set -e
git init -q -b main .
git config user.name "Ann Author"; git config user.email ann@example.com; git config commit.gpgsign false
c(){ GIT_AUTHOR_DATE="$1" GIT_COMMITTER_DATE="$1" git commit -q -m "$2" ${3:+-m "$3"}; }
mkdir -p src/lib docs
printf 'one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n' > src/a.txt
echo 'package lib' > src/lib/lib.go; echo '# readme' > README.md
git add -A; c "2026-01-01T10:00:00+0000" "initial commit" "A body paragraph.

Second paragraph of the body."
printf 'one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven\n' > src/a.txt
printf '\000\001\002bin' > docs/blob.bin
git add -A; GIT_AUTHOR_NAME="Bob Builder" GIT_AUTHOR_EMAIL=bob@example.com c "2026-01-02T11:30:00+0100" "change two, add binary"
git tag v1.0.0
git checkout -q -b feature
echo 'func F() {}' >> src/lib/lib.go; git add -A; c "2026-01-03T09:00:00+0000" "feature: add F"
git mv README.md docs/README.md; git add -A; c "2026-01-04T09:00:00+0000" "move readme"
git checkout -q main
echo 'extra' > extra.txt; chmod +x extra.txt; git add -A; c "2026-01-05T09:00:00+0000" "add extra (exec)"
GIT_AUTHOR_DATE="2026-01-06T09:00:00+0000" GIT_COMMITTER_DATE="2026-01-06T09:00:00+0000" git merge -q --no-ff feature -m "Merge branch 'feature'"
printf 'no newline at end' > nonl.txt; git rm -q extra.txt; git add -A; c "2026-01-07T09:00:00+0000" "add nonl, remove extra"
`

// parityRepos builds the git history and its cairn clone, returning the git
// work tree and the cairn line folder.
func parityRepos(t *testing.T) (gitDir, lineDir string) {
	t.Helper()
	return parityReposWith(t, nil)
}

// parityReposWith is parityRepos with a hook that adds to the git history
// before it is cloned.
func parityReposWith(t *testing.T, extra func(gitDir string)) (gitDir, lineDir string) {
	t.Helper()
	skipOnWindows(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	gitDir = t.TempDir()
	cmd := exec.Command("bash", "-c", parityRepoScript)
	cmd.Dir = gitDir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building git repo: %v\n%s", err, out)
	}
	if extra != nil {
		extra(gitDir)
	}
	root := filepath.Join(t.TempDir(), "c")
	mustRun(t, "clone", gitDir, root)
	return gitDir, filepath.Join(root, "main")
}

// gitRun runs git and returns stdout and its exit code.
func gitRun(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("git %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

// cairnRun runs a cairn command standing in dir, returning stdout and the
// exit code main() would use.
func cairnRun(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	full := append([]string{args[0], "--repo", dir}, args[1:]...)
	out, err := captureRunResult(t, full...)
	code := 0
	if err != nil {
		code = 1
		if c, ok := exitCodeOf(err); ok {
			code = c
		}
	}
	return out, code
}

func TestE2E_InspectionMatchesGit(t *testing.T) {
	gitDir, lineDir := parityRepos(t)
	cases := []string{
		// the plain forms: git's default output
		"log",
		"log -n 2",
		"log main -n 3",
		"log feature",
		"show HEAD",
		"show v1.0.0",
		"show HEAD~4",
		"diff HEAD~1 HEAD",
		"diff v1.0.0 HEAD~3",
		// log: formats
		"log --oneline",
		"log --no-decorate",
		"log --format=%H",
		"log --pretty=format:%h -2",
		"log --pretty=short --no-decorate -2",
		"log --pretty=full --no-decorate -2",
		"log --pretty=fuller --no-decorate -2",
		"log --pretty=raw --no-decorate -2",
		"log --format=%h|%t|%p|%an|%ae|%ad|%ai|%aI|%at|%cn|%ce|%cd|%ci|%cI|%ct|%s|%f",
		"log --format=%B--%b--%n%% -3",
		"log --date=short --format=%ad",
		"log --date=iso --no-decorate -2",
		"log --date=iso-strict --format=%ad",
		"log --date=rfc --format=%ad",
		"log --date=unix --format=%ad",
		// log: diff output
		"log --no-decorate --stat --no-renames",
		"log --no-decorate -p --no-renames --full-index",
		"log --oneline --name-status --no-renames",
		"log --format=%H --name-only --no-renames",
		"log --oneline --shortstat --no-renames",
		"log --format=%h --numstat --no-renames",
		"log --oneline -n1 --stat -p --no-renames",
		"log --oneline --first-parent -p --no-renames -U1",
		// log: selection
		"log --oneline -n 2 --skip 1",
		"log --oneline -2 --reverse",
		"log --oneline --reverse",
		"log --oneline --first-parent",
		"log --oneline --merges",
		"log --oneline --no-merges",
		"log --oneline --author=Bob",
		"log --oneline --author=bob -i",
		"log --oneline --committer=Ann -3",
		"log --oneline --grep=feature",
		"log --oneline --grep=FEATURE -i --grep=binary",
		"log --oneline --since=2026-01-04 --until=2026-01-06T12:00:00Z",
		"log --oneline -S eleven",
		"log --oneline -G ^func",
		"log --oneline --all",
		"log --oneline feature",
		"log --oneline feature..main",
		"log --oneline main...feature",
		"log --oneline ^feature main",
		"log --oneline main --not feature",
		"log --oneline v1.0.0..HEAD~1",
		"log --oneline HEAD~1^2~1",
		"log --oneline -- src",
		"log --oneline -- src/lib/lib.go docs",
		// log: graph
		"log --oneline --graph",
		"log --graph --no-decorate",
		"log --graph --stat --no-renames --oneline",
		"log --graph --no-decorate -p --no-renames --full-index",
		"log --graph --oneline --all -- src",
		// show
		"show --no-decorate --no-renames --full-index HEAD~3",
		"show -s --no-decorate HEAD",
		"show --stat --oneline --no-renames HEAD~4",
		"show --name-only --format=%H HEAD",
		"show --no-decorate --name-status v1.0.0",
		"show HEAD:src/a.txt",
		"show HEAD~2:docs",
		// diff
		"diff HEAD~3 HEAD --no-renames --full-index",
		"diff HEAD~3..HEAD~1 --stat --no-renames",
		"diff feature...main --name-status --no-renames",
		"diff --stat v1.0.0 feature --no-renames",
		"diff --numstat v1.0.0 HEAD --no-renames",
		"diff --shortstat v1.0.0 HEAD --no-renames",
		"diff -R HEAD~4 HEAD~3 --full-index",
		"diff -U0 HEAD~4 HEAD~3 --full-index",
		"diff --name-only HEAD~4 HEAD -- src",
		"diff --exit-code --name-only HEAD~1 HEAD",
		"diff --quiet HEAD~1 HEAD",
		"diff --quiet HEAD HEAD",
		// plumbing
		"rev-parse HEAD",
		"rev-parse --short HEAD",
		"rev-parse --short=10 HEAD~2",
		"rev-parse --abbrev-ref HEAD",
		"rev-parse HEAD:src HEAD^{tree} v1.0.0 feature~1",
		"rev-parse feature..main",
		"rev-parse --verify -q nosuch",
		"rev-list HEAD",
		"rev-list --count HEAD",
		"rev-list --count feature..main",
		"rev-list --left-right --count feature...main",
		"rev-list -n 3 --reverse HEAD",
		"rev-list --first-parent --parents HEAD",
		"rev-list --all --topo-order",
		"rev-list HEAD -- docs",
		"shortlog -s -n HEAD",
		"shortlog -sne HEAD",
		"shortlog HEAD",
		"ls-tree HEAD",
		"ls-tree -r HEAD",
		"ls-tree -r -l HEAD",
		"ls-tree -d -r HEAD",
		"ls-tree -r -t --name-only HEAD",
		"ls-tree HEAD src/",
		"ls-tree HEAD src docs/blob.bin",
		"cat-file -t HEAD",
		"cat-file -s HEAD:src/a.txt",
		"cat-file -p HEAD^{tree}",
		"cat-file -p HEAD:src/a.txt",
		"cat-file -t HEAD:src",
		"cat-file blob HEAD:nonl.txt",
		"ls-files",
		"ls-files -s src",
		"ls-files -o --exclude-standard",
		"describe --tags",
		"describe --tags --abbrev=4 feature",
		"describe --tags v1.0.0",
		"describe --tags --long v1.0.0",
		"describe --tags --always HEAD~4",
		// grep
		"grep -n e",
		"grep -z -l e",
		"grep -z -L func",
		"grep -z -n e",
		"grep -z -n -A1 two",
		"grep -z -c t",
		"grep -c t",
		"grep -l -i TWO",
		"grep -L func",
		"grep -w one",
		"grep -v -n e -- src/a.txt",
		"grep -E f(iv|ou)r?",
		"grep -F F()",
		"grep -e one -e two",
		"grep -n lib HEAD~3",
		"grep -h -o e[a-z]*n",
		"grep -n -C1 five",
		"grep -ni TWO",
		"grep nosuchthing",
		"grep -n F( HEAD -- src/lib",
		"grep -I -l bin",
		"grep bin",
		// --stat widths and counts (--no-renames: cairn does not detect renames by default)
		"log --stat-width=30 -1",
		"log --stat=60,20,1 -1 --no-renames",
		"log --stat=20,8,2 --oneline --no-renames",
		"diff --stat=40 HEAD~3 HEAD --no-renames",
		"diff --stat=60,,1 HEAD~3 HEAD --no-renames",
		"diff --stat-count=2 HEAD~3 HEAD --no-renames",
		"diff --stat-graph-width=2 HEAD~4 HEAD --no-renames",
		"diff --stat=3 HEAD~4 HEAD --no-renames",
		"diff --stat=40,5 HEAD~4 HEAD --no-renames",
		"diff --stat=40,1,0 HEAD~4 HEAD --no-renames",
		"diff --stat-name-width=8 --stat-width=40 HEAD~4 HEAD --no-renames",
		"diff --stat=abc HEAD~4 HEAD",
		// name listings replace the stat and the patch
		"log --name-only --stat --oneline -2 --no-renames",
		"log --name-status -p --oneline -2 --no-renames",
		"show --name-only --stat HEAD",
		"diff --name-only --numstat HEAD~3 HEAD --no-renames",
		"diff --name-only --shortstat HEAD~3 HEAD --no-renames",
		"diff --name-only -p HEAD~3 HEAD --no-renames",
		"diff --name-status --name-only HEAD~3 HEAD --no-renames",
		// show -- <path> prints nothing for a commit that does not touch it
		"show HEAD -- src/a.txt",
		"show HEAD~2 -- src/a.txt",
		"show -s HEAD -- src/a.txt",
		"show HEAD -- nonl.txt",
		"show --stat HEAD~4 -- src/a.txt docs",
		// format: with diff output
		"log --pretty=format:X -p -2 --no-renames",
		"log --pretty=format:X --stat -2",
		"log --pretty=format:X --stat -p -1",
		"log --pretty=tformat:X --stat -p -2",
		"log --pretty=tformat:X -p -2 --no-renames",
		"log --pretty=format:X --name-only -2",
		"log --pretty=format:X --shortstat -2",
		"log --pretty=oneline --stat -p -1",
		"log --pretty=short --stat -p -2 --no-renames",
		"log --stat -p -2 --no-renames",
		"log --summary --oneline --no-renames",
		"diff --summary HEAD~5 HEAD --no-renames",
		"diff --stat --summary HEAD~5 HEAD --no-renames",
		"show --summary -p HEAD~3",
		"show --stat --summary HEAD~3 --no-renames",
		// rev-parse A...B prints B A ^base
		"rev-parse feature...main",
		"rev-parse main...feature",
		"rev-parse main...",
		"rev-parse ...feature",
		"rev-parse v1.0.0...feature",
		// --not flips --all too
		"rev-list --not HEAD~2 --all",
		"rev-list --all --not HEAD~2",
		"rev-list --not --all",
		"rev-list HEAD --not --all",
		"rev-list --not --not --all",
		"rev-list --all ^feature",
		"log --oneline --not feature --all",
		"rev-list --not v1.0.0 HEAD --all",
		// ls-tree pathspecs are literal
		"ls-tree HEAD src/*",
		"ls-tree -r HEAD s*",
		"ls-tree HEAD src/lib/",
		"ls-tree HEAD src/lib",
		"ls-tree -r HEAD src/lib",
		"ls-tree HEAD src//lib",
		"ls-tree HEAD src/",
		"ls-tree -r HEAD src/",
		// date formats
		"log --date=format:%Z --format=%ad",
		"log --date=format-local:%Z|%z --format=%ad",
		"log --date=format:%a|%k|%l|%u|%w|%R|%r|%P|%c|%x|%X|%C|%e|%j --format=%ad",
	}
	for _, c := range cases {
		args := strings.Fields(c)
		want, wantCode := gitRun(t, gitDir, args...)
		got, gotCode := cairnRun(t, lineDir, args...)
		if got != want {
			t.Errorf("%s:\n--- cairn\n%s--- git\n%s", c, got, want)
		}
		if (gotCode == 0) != (wantCode == 0) || (wantCode == 1 && gotCode != 1) {
			t.Errorf("%s: exit %d, git exits %d", c, gotCode, wantCode)
		}
	}
}

// Working-tree forms: the same edits made on both sides (staged in git, since
// cairn has no index — everything on disk is the working change).
func TestE2E_WorkingTreeInspectionMatchesGit(t *testing.T) {
	gitDir, lineDir := parityRepos(t)
	for _, dir := range []string{gitDir, lineDir} {
		write := func(rel, content string) {
			if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("src/a.txt", "one\nTWO\nthree\nfour\nfive  x\nsix\nseven\neight\nnine\nten\neleven\nnew tail\n")
		write("newfile.txt", "fresh\n")
		if err := os.Remove(filepath.Join(dir, "nonl.txt")); err != nil {
			t.Fatal(err)
		}
	}
	if out, code := gitRun(t, gitDir, "add", "-A"); code != 0 {
		t.Fatalf("git add: %s", out)
	}
	for _, c := range []string{
		"diff -p HEAD --full-index",
		"diff --stat HEAD",
		"diff --numstat HEAD",
		"diff --name-status HEAD",
		"diff --shortstat HEAD",
		"diff -U0 HEAD --full-index",
		"diff -R HEAD --full-index",
		"diff -w HEAD~1 --stat --no-renames",
		"diff --quiet HEAD",
		"diff --quiet HEAD -- docs",
		"diff --exit-code --name-only HEAD -- src",
		"ls-files",
		"grep -n tail",
		"grep -c fresh",
		"describe --tags --dirty",
		"status --porcelain=v2",
		"status --porcelain=v2 -- src",
		"status --porcelain=v2 -z",
	} {
		args := strings.Fields(c)
		want, wantCode := gitRun(t, gitDir, args...)
		got, gotCode := cairnRun(t, lineDir, args...)
		if got != want || gotCode != wantCode {
			t.Errorf("%s (exit %d, git %d):\n--- cairn\n%s--- git\n%s", c, gotCode, wantCode, got, want)
		}
	}
	// cairn's own no-argument form is the working change against its parent.
	got, _ := cairnRun(t, lineDir, "diff", "--name-status")
	if want := "A\tnewfile.txt\nD\tnonl.txt\nM\tsrc/a.txt\n"; got != want {
		t.Errorf("diff --name-status = %q, want %q", got, want)
	}
	if got, _ := cairnRun(t, lineDir, "ls-files", "-m"); got != "nonl.txt\nsrc/a.txt\n" {
		t.Errorf("ls-files -m = %q", got)
	}
	if got, _ := cairnRun(t, lineDir, "ls-files", "-d"); got != "nonl.txt\n" {
		t.Errorf("ls-files -d = %q", got)
	}
}

// Pathspecs are read relative to where the operator STANDS inside the line
// folder — at its top or in a subdirectory — exactly as git reads them
// relative to the cwd. (Released cairn measured them against the cwd as if it
// were the repo root, so `cairn diff -- <path>` inside a line printed nothing.)
func TestE2E_PathspecsRelativeToCwdInsideLine(t *testing.T) {
	_, lineDir := parityRepos(t)
	if err := os.WriteFile(filepath.Join(lineDir, "src", "lib", "lib.go"), []byte("package lib\nfunc F() {}\nfunc G() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(lineDir, "nonl.txt")); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(lineDir)
	sub := filepath.Join(lineDir, "src")
	type tc struct {
		dir  string
		args []string
		want string // substring; "" = must print nothing
	}
	for _, c := range []tc{
		// plain diff, from the root, the line's top, and a subdirectory
		{root, []string{"diff", "--", "src/lib/lib.go"}, "+func G() {}"},
		{lineDir, []string{"diff", "--", "src/lib/lib.go"}, "+func G() {}"},
		{lineDir, []string{"diff", "main", "--", "src/lib/lib.go"}, "+func G() {}"},
		{lineDir, []string{"diff", "src/lib/lib.go"}, "+func G() {}"},
		{sub, []string{"diff", "--", "lib/lib.go"}, "+func G() {}"},
		{sub, []string{"diff", "lib/lib.go"}, "+func G() {}"},
		{sub, []string{"diff", "--", "../nonl.txt"}, "deleted file mode 100644\n"},
		{lineDir, []string{"diff", "--", "nonl.txt"}, "deleted file mode 100644\n"},
		{lineDir, []string{"diff", "--", "docs"}, ""},
		// git-shaped diff
		{lineDir, []string{"diff", "--name-only", "--", "src/lib/lib.go"}, "src/lib/lib.go\n"},
		{sub, []string{"diff", "--name-only", "--", "lib"}, "src/lib/lib.go\n"},
		{sub, []string{"diff", "--stat", "--", "lib/lib.go"}, "src/lib/lib.go | 1 +"},
		{sub, []string{"diff", "--name-only", "--", "../docs"}, ""},
		{sub, []string{"diff", "--name-status", "HEAD", "--", "../nonl.txt"}, "D\tnonl.txt"},
		// the other pathspec-taking commands
		{sub, []string{"log", "--oneline", "--", "lib/lib.go"}, "feature: add F"},
		{sub, []string{"log", "--oneline", "--", "a.txt"}, "change two, add binary"},
		{sub, []string{"ls-files"}, "a.txt\nlib/lib.go\n"},
		{sub, []string{"ls-files", "--full-name", "lib"}, "src/lib/lib.go\n"},
		{sub, []string{"ls-files", "-m"}, "lib/lib.go\n"},
		{sub, []string{"grep", "-n", "func"}, "lib/lib.go:2:func F() {}\nlib/lib.go:3:func G() {}\n"},
		{sub, []string{"grep", "-l", "func", "HEAD", "--", "lib"}, "HEAD:lib/lib.go\n"},
		{sub, []string{"ls-tree", "--name-only", "HEAD"}, "a.txt\nlib\n"},
		{sub, []string{"ls-tree", "--name-only", "HEAD", "lib/"}, "lib/lib.go\n"},
		{sub, []string{"show", "HEAD:./lib/lib.go"}, "package lib\nfunc F() {}\n"},
		{sub, []string{"show", "HEAD:src/a.txt"}, "one\nTWO\n"},
		{sub, []string{"rev-parse", "--show-prefix"}, "src/\n"},
		{sub, []string{"rev-parse", "--show-toplevel"}, lineDir + "\n"},
		{sub, []string{"blame", "-L", "2,2", "lib/lib.go"}, "func F() {}"},
	} {
		// Both ways of standing somewhere: --repo, and the real cwd.
		for _, chdir := range []bool{false, true} {
			var got string
			if chdir {
				t.Chdir(c.dir)
				out, err := captureRunResult(t, c.args...)
				if err != nil {
					t.Errorf("in %s: %v: %v", c.dir, c.args, err)
				}
				got = out
			} else {
				got, _ = cairnRun(t, c.dir, c.args...)
			}
			if c.want == "" && got != "" {
				t.Errorf("in %s (chdir=%v): %v printed %q, want nothing", c.dir, chdir, c.args, got)
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("in %s (chdir=%v): %v = %q, want it to contain %q", c.dir, chdir, c.args, got, c.want)
			}
		}
	}
}

// format-patch output must be something `git am` applies, reproducing the
// exact tree — text, a binary file, an exec bit, a file with no final newline.
func TestE2E_FormatPatchAppliesWithGitAm(t *testing.T) {
	gitDir, lineDir := parityRepos(t)
	outDir := t.TempDir()
	names, _ := cairnRun(t, lineDir, "format-patch", "--root", "-o", outDir)
	files := strings.Fields(names)
	if len(files) != 6 { // seven commits, the merge skipped
		t.Fatalf("format-patch wrote %d patches, want 6:\n%s", len(files), names)
	}
	if filepath.Base(files[1]) != "0002-change-two-add-binary.patch" {
		t.Errorf("second patch is named %s", filepath.Base(files[1]))
	}
	am := t.TempDir()
	if out, code := gitRun(t, am, "init", "-q", "-b", "main", "."); code != 0 {
		t.Fatal(out)
	}
	args := append([]string{"-c", "user.name=x", "-c", "user.email=x@x", "am", "-q"}, files...)
	cmd := exec.Command("git", args...)
	cmd.Dir = am
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git am: %v\n%s", err, out)
	}
	got, _ := gitRun(t, am, "rev-parse", "HEAD^{tree}")
	want, _ := gitRun(t, gitDir, "rev-parse", "HEAD^{tree}")
	if got != want {
		t.Errorf("git am produced tree %s, want %s", got, want)
	}
	if subj, _ := gitRun(t, am, "log", "--format=%an|%s", "-1", "HEAD~4"); subj != "Bob Builder|change two, add binary\n" {
		t.Errorf("applied commit = %q", subj)
	}
	// A range, to stdout, numbered within the range.
	mbox, _ := cairnRun(t, lineDir, "format-patch", "--stdout", "v1.0.0..feature")
	if !strings.Contains(mbox, "Subject: [PATCH 1/2] feature: add F") ||
		!strings.Contains(mbox, "Subject: [PATCH 2/2] move readme") {
		t.Errorf("format-patch --stdout v1.0.0..feature:\n%s", mbox)
	}
	if one, _ := cairnRun(t, lineDir, "format-patch", "--stdout", "-1"); !strings.Contains(one, "Subject: [PATCH] add nonl, remove extra") {
		t.Errorf("format-patch -1:\n%s", one)
	}
}

func TestE2E_InspectionOnCairnHistory(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	main := filepath.Join(root, "main")
	write := func(rel, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(main, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(main, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("f.txt", "one\n")
	c1 := strings.TrimSpace(captureRun(t, "commit", "--repo", root, "--author", "tester", "-m", "first", "main"))
	write("f.txt", "one\ntwo\n")
	c2 := strings.TrimSpace(captureRun(t, "commit", "--repo", root, "--author", "tester", "-m", "second", "main"))
	mustRun(t, "tag", "--repo", root, "v0.1.0", "main")
	write("f.txt", "one\ntwo\nthree\n") // un-sealed work

	// HEAD and the line name are the SEALED tip; the open working change is
	// listed only by --working and by the classic view.
	if got, _ := cairnRun(t, root, "rev-parse", "HEAD", "main", "HEAD~1", "v0.1.0", "@"); got != c2+"\n"+c2+"\n"+c1+"\n"+c2+"\n"+c2+"\n" {
		t.Errorf("rev-parse = %q", got)
	}
	mustRun(t, "status", "--repo", root) // snapshots the un-sealed edit
	if got, _ := cairnRun(t, root, "log", "--classic"); !strings.Contains(got, "(working)") || !strings.Contains(got, c2[:8]+"  ") {
		t.Errorf("classic log = %q", got)
	}
	if got, _ := cairnRun(t, root, "log", "--working", "--oneline", "--stat"); !strings.Contains(got, " (working change) (working)\n f.txt | 1 +\n") ||
		!strings.Contains(got, c2[:7]+" second\n") {
		t.Errorf("log --working = %q", got)
	}
	if got, _ := cairnRun(t, root, "log"); !strings.HasPrefix(got, "commit "+c2+"\nAuthor: tester <") || strings.Contains(got, "(working)") {
		t.Errorf("plain log = %q", got)
	}
	if got, _ := cairnRun(t, root, "show", c2[:8]); !strings.HasPrefix(got, "commit "+c2+"\nAuthor: ") || !strings.Contains(got, "\n    second\n\ndiff --git a/f.txt b/f.txt\n") {
		t.Errorf("plain show = %q", got)
	}
	if got, _ := cairnRun(t, root, "show", "--classic", c2[:8]); !strings.Contains(got, "/f.txt\n@@") || strings.Contains(got, "diff --git") {
		t.Errorf("classic show = %q", got)
	}
	if got, _ := cairnRun(t, root, "diff"); !strings.HasPrefix(got, "diff --git a/f.txt b/f.txt\nindex ") || !strings.HasSuffix(got, "@@ -1,2 +1,3 @@\n one\n two\n+three\n") {
		t.Errorf("plain diff = %q", got)
	}
	got, _ := cairnRun(t, root, "log", "--format=%H %s")
	if got != c2+" second\n"+c1+" first\n" {
		t.Errorf("log --format = %q (the Change-Id trailer and the working commit must not show)", got)
	}
	if got, _ := cairnRun(t, root, "log", "--format=%B", "-1"); got != "second\n\n" {
		t.Errorf("%%B = %q", got)
	}
	if got, _ := cairnRun(t, root, "log", "--oneline", "--decorate", "-1"); got != c2[:7]+" (HEAD -> main, tag: v0.1.0) second\n" {
		t.Errorf("decorated = %q", got)
	}
	if got, _ := cairnRun(t, root, "show", "HEAD:f.txt"); got != "one\ntwo\n" {
		t.Errorf("show HEAD:f.txt = %q", got)
	}
	if got, _ := cairnRun(t, root, "rev-parse", "--abbrev-ref", "HEAD"); got != "main\n" {
		t.Errorf("abbrev-ref = %q", got)
	}
	// v0.1.0 is a lightweight tag: git's describe needs --tags for it.
	if _, code := cairnRun(t, root, "describe", "--dirty"); code == 0 {
		t.Errorf("describe without --tags used a lightweight tag")
	}
	if got, _ := cairnRun(t, root, "describe", "--tags", "--dirty"); got != "v0.1.0-dirty\n" {
		t.Errorf("describe --dirty = %q", got)
	}
	// `diff <rev>` is the working tree against that commit, as a git patch.
	got, code := cairnRun(t, root, "diff", "--exit-code", "HEAD~1")
	if code != 1 || !strings.Contains(got, "diff --git a/f.txt b/f.txt\n") || !strings.Contains(got, "@@ -1 +1,3 @@\n one\n+two\n+three\n") {
		t.Errorf("diff HEAD~1 (exit %d) = %q", code, got)
	}
	// An ambiguous abbreviation is an error that says so; too short is refused.
	if _, err := captureRunResult(t, "rev-parse", "--repo", root, "zzzz"); err == nil {
		t.Error("rev-parse of an unknown name succeeded")
	}
	// cat-file -e answers by status alone.
	if _, code := cairnRun(t, root, "cat-file", "-e", "HEAD"); code != 0 {
		t.Errorf("cat-file -e HEAD exits %d", code)
	}
	if out, code := cairnRun(t, root, "cat-file", "-e", "nosuch"); code != 1 || out != "" {
		t.Errorf("cat-file -e nosuch exits %d, prints %q", code, out)
	}
	// Colour is off unless asked for, and NO_COLOR-proof when forced.
	if got, _ := cairnRun(t, root, "log", "--oneline", "-1"); strings.Contains(got, "\x1b[") {
		t.Errorf("uncoloured log has escapes: %q", got)
	}
	if got, _ := cairnRun(t, root, "log", "--oneline", "-1", "--color"); !strings.Contains(got, "\x1b[33m") {
		t.Errorf("--color log has no escapes: %q", got)
	}
	if got, _ := cairnRun(t, root, "diff", "--color=always", "HEAD~1"); !strings.Contains(got, "\x1b[32m+two") {
		t.Errorf("--color diff = %q", got)
	}
	mustRun(t, "config", "--repo", root, "color.ui", "always")
	if got, _ := cairnRun(t, root, "grep", "two"); !strings.Contains(got, "\x1b[1;31mtwo") {
		t.Errorf("color.ui=always grep = %q", got)
	}
	if got, _ := cairnRun(t, root, "grep", "--no-color", "two"); got != "f.txt:two\n" {
		t.Errorf("--no-color grep = %q", got)
	}
}

func TestGitOptsParsing(t *testing.T) {
	g := newGitOpts("x", "n|max-count= # i l e= color? U= oneline stat")
	err := g.parse([]string{"-il", "-n5", "-3", "--max-count", "7", "-e", "a", "-eb", "--color", "-U10", "--no-oneline",
		"--repo", "/r", "pos1", "--stat", "--", "-p", "path"})
	if err != nil {
		t.Fatal(err)
	}
	if !g.on("i") || !g.on("l") || g.str("n") != "7" || !g.numSet || g.num != 3 || strings.Join(g.strs("e"), ",") != "a,b" ||
		!g.on("color") || g.str("color") != "" || g.str("U") != "10" || g.on("oneline") || !g.negated("oneline") ||
		g.repoDir() != "/r" || !g.on("stat") || strings.Join(g.pos, ",") != "pos1" || strings.Join(g.paths, ",") != "-p,path" || !g.hasDD {
		t.Errorf("parsed wrong: %+v", g)
	}
	if err := newGitOpts("x", "i").parse([]string{"--bogus"}); err == nil {
		t.Error("unknown option accepted")
	}
	if err := newGitOpts("x", "i").parse([]string{"-3"}); err == nil {
		t.Error("-<N> accepted by a command that does not take it")
	}
}

func TestRelativeDateAndPatterns(t *testing.T) {
	now := mustTime(t, "2026-06-01T12:00:00Z")
	for in, want := range map[string]string{
		"2026-06-01T11:59:30Z": "30 seconds ago", "2026-06-01T11:00:00Z": "60 minutes ago",
		"2026-05-31T12:00:00Z": "24 hours ago", "2026-05-25T12:00:00Z": "7 days ago",
		"2026-05-01T12:00:00Z": "4 weeks ago", "2026-01-01T12:00:00Z": "5 months ago",
		"2024-12-01T12:00:00Z": "1 year, 6 months ago", "2016-06-01T12:00:00Z": "10 years ago",
	} {
		if got := relativeDate(mustTime(t, in), now); got != want {
			t.Errorf("relativeDate(%s) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		`a(b)`: `a\(b\)`, `a\(b\)`: `a(b)`, `x\|y`: `x|y`, `a+`: `a\+`, `[a-z]*n`: `[a-z]*n`, `\<w\>`: `\bw\b`,
	} {
		if got := goPattern(in, false, false); got != want {
			t.Errorf("goPattern(%q) = %q, want %q", in, got, want)
		}
	}
	if d, err := parseDateArg("2 weeks ago", now); err != nil || !d.Equal(now.AddDate(0, 0, -14)) {
		t.Errorf("parseDateArg(2 weeks ago) = %v, %v", d, err)
	}
	if d, err := parseDateArg("3.days.ago", now); err != nil || !d.Equal(now.AddDate(0, 0, -3)) {
		t.Errorf("parseDateArg(3.days.ago) = %v, %v", d, err)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// An imported annotated tag stays a tag object for cat-file/rev-parse/show and
// peels for commit-ish uses, as in git.
func TestE2E_ImportedAnnotatedTagIsTagObject(t *testing.T) {
	skipOnWindows(t)
	bare := makeBareWithAnnotatedTag(t)
	dir := t.TempDir()
	mustRun(t, "clone", bare, dir)
	for _, c := range []string{
		"cat-file -t v1.0",
		"cat-file -p v1.0",
		"rev-parse v1.0",
		"rev-parse v1.0^{commit}",
		"rev-parse v1.0^{}",
		"cat-file -t v1.0^{commit}",
		"show -s --format=%H v1.0^{commit}",
	} {
		args := strings.Fields(c)
		want, err := exec.Command("git", append([]string{"-C", bare}, args...)...).Output()
		if err != nil {
			t.Fatalf("git %s: %v", c, err)
		}
		got, code := cairnRun(t, dir, args...)
		if code != 0 || got != string(want) {
			t.Errorf("%s (exit %d):\n got %q\nwant %q", c, code, got, want)
		}
	}
	// show carries the tag header.
	if out, _ := cairnRun(t, dir, "show", "-s", "v1.0"); !strings.HasPrefix(out, "tag v1.0\nTagger: ") {
		t.Errorf("show v1.0 lacks the tag header:\n%s", out)
	}
}
