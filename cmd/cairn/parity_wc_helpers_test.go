//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// wc is a throwaway cairn repo for the working-copy parity tests.
type wc struct {
	t    *testing.T
	root string
}

func newWC(t *testing.T) *wc {
	t.Helper()
	// A fixed identity, and no global config leaking in from the machine.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("CAIRN_AUTHOR", "Tess Ter")
	t.Setenv("CAIRN_EMAIL", "tess@example.org")
	root := t.TempDir()
	mustRun(t, "init", root)
	return &wc{t: t, root: root}
}

// newWCWithFeature seeds main with base.txt and expresses "feat" off it — the
// non-root line the history-editing verbs (amend, reset <rev>) need.
func newWCWithFeature(t *testing.T) *wc {
	t.Helper()
	w := newWC(t)
	w.write("main", "base.txt", "base\n")
	w.ok("main", "commit", "-m", "base")
	mustRun(t, "express", "--repo", w.root, "feat")
	return w
}

func (w *wc) dir(branch string) string { return filepath.Join(w.root, branch) }

func (w *wc) write(branch, rel, content string) {
	w.t.Helper()
	full := filepath.Join(w.dir(branch), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wc) remove(branch, rel string) {
	w.t.Helper()
	if err := os.RemoveAll(filepath.Join(w.dir(branch), filepath.FromSlash(rel))); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wc) read(branch, rel string) string {
	w.t.Helper()
	b, err := os.ReadFile(filepath.Join(w.dir(branch), filepath.FromSlash(rel)))
	if err != nil {
		w.t.Fatalf("read %s/%s: %v", branch, rel, err)
	}
	return string(b)
}

func (w *wc) exists(branch, rel string) bool {
	_, err := os.Lstat(filepath.Join(w.dir(branch), filepath.FromSlash(rel)))
	return err == nil
}

// args builds `<verb> --repo <root>/<branch> <rest...>`: pointing --repo at the
// line's folder is how a command is "run from inside it", so the branch is
// inferred exactly as it is for a user standing there.
func (w *wc) args(branch, verb string, rest ...string) []string {
	if verb == "stash" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		// `stash <sub-verb> [flags]`: the sub-verb comes first, as in git.
		return append([]string{verb, rest[0], "--repo", w.dir(branch)}, rest[1:]...)
	}
	return append([]string{verb, "--repo", w.dir(branch)}, rest...)
}

// run runs a verb inside branch's folder, returning stdout, stderr and error.
func (w *wc) run(branch, verb string, rest ...string) (string, string, error) {
	w.t.Helper()
	return runOutErr(w.t, w.args(branch, verb, rest...)...)
}

// ok is run that must succeed; it returns trimmed stdout.
func (w *wc) ok(branch, verb string, rest ...string) string {
	w.t.Helper()
	out, errOut, err := w.run(branch, verb, rest...)
	if err != nil {
		w.t.Fatalf("cairn %s %v: %v\nstderr: %s", verb, rest, err, errOut)
	}
	return strings.TrimSpace(out)
}

// fails is run that must fail (not with a conflict); it returns the error text.
func (w *wc) fails(branch, verb string, rest ...string) string {
	w.t.Helper()
	_, _, err := w.run(branch, verb, rest...)
	if err == nil {
		w.t.Fatalf("cairn %s %v succeeded; want an error", verb, rest)
	}
	if errors.Is(err, errConflicts) {
		w.t.Fatalf("cairn %s %v: got a conflict exit; want a hard error", verb, rest)
	}
	return err.Error()
}

// conflicts is run that must exit 2 (errConflicts); it returns stderr.
func (w *wc) conflicts(branch, verb string, rest ...string) string {
	w.t.Helper()
	_, errOut, err := w.run(branch, verb, rest...)
	if !errors.Is(err, errConflicts) {
		w.t.Fatalf("cairn %s %v: err = %v, want errConflicts (exit 2)\nstderr: %s", verb, rest, err, errOut)
	}
	return errOut
}

// porcelain is `status --porcelain` for branch.
func (w *wc) porcelain(branch string) string {
	w.t.Helper()
	out, _, err := w.run(branch, "status", "--porcelain")
	if err != nil {
		w.t.Fatalf("status --porcelain: %v", err)
	}
	return out
}

// subjects lists the SEALED commit subjects of branch, newest first.
func (w *wc) subjects(branch string) []string {
	w.t.Helper()
	var out []string
	for _, line := range strings.Split(w.ok(branch, "log", "-n", "50"), "\n") {
		f := strings.SplitN(line, "  ", 4)
		if len(f) == 4 && f[3] != "(working)" {
			out = append(out, f[3])
		}
	}
	return out
}

// head is the sha of branch's newest sealed commit.
func (w *wc) head(branch string) string {
	w.t.Helper()
	for _, line := range strings.Split(w.ok(branch, "log", "-n", "50"), "\n") {
		f := strings.SplitN(line, "  ", 4)
		if len(f) == 4 && f[3] != "(working)" {
			return f[0]
		}
	}
	w.t.Fatalf("no sealed commit on %s", branch)
	return ""
}

// store runs real git against cairn's object store — the ground truth for
// what a commit object actually holds (author vs committer, raw message).
func (w *wc) store(args ...string) string {
	w.t.Helper()
	return gitIn(w.t, "", append([]string{"--git-dir", filepath.Join(w.root, ".cairn", "objects.git")}, args...)...)
}

// needGit skips the test when git is not installed.
func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// gitIn runs real git in dir with a fixed identity and no user config.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func gitTry(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Gita Uthor", "GIT_AUTHOR_EMAIL=gita@example.org",
		"GIT_COMMITTER_NAME=Gita Uthor", "GIT_COMMITTER_EMAIL=gita@example.org",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// newGit makes an empty real-git repo to run the same steps in.
func newGit(t *testing.T) string {
	t.Helper()
	needGit(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	return dir
}

func writeAt(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// both applies the same file edit to a cairn line folder and a git worktree.
type both struct {
	t   *testing.T
	w   *wc
	git string
	br  string
}

func (b both) write(rel, content string) {
	b.w.write(b.br, rel, content)
	writeAt(b.t, b.git, rel, content)
}

func (b both) remove(rel string) {
	b.w.remove(b.br, rel)
	if err := os.RemoveAll(filepath.Join(b.git, filepath.FromSlash(rel))); err != nil {
		b.t.Fatal(err)
	}
}

// treeOf snapshots a directory as "path[*]=content" lines (a * marks an
// executable), skipping .git / .cairn — for comparing a cairn line folder with
// a git worktree.
func treeOf(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			if fi.Name() == ".git" || fi.Name() == ".cairn" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		mark := ""
		if fi.Mode()&0o111 != 0 {
			mark = "*"
		}
		lines = append(lines, filepath.ToSlash(rel)+mark+"="+string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\x00")
}

func mustContain(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("output does not mention %q:\n%s", want, got)
		}
	}
}
