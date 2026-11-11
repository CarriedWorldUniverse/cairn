package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Round-3 parity fixes that need their own history (a colliding object name,
// unicode subjects, rename similarity) or their own process state (stdin, a
// subdirectory): each is compared byte for byte with real git.

// gitIn runs a shell script in dir with a fixed identity and dates.
func gitScript(t *testing.T, dir, script string) {
	t.Helper()
	cmd := exec.Command("bash", "-c", "set -e\n"+script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00+0000", "GIT_COMMITTER_DATE=2026-01-01T00:00:00+0000")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// cloneOf clones a git repo with cairn, returning its main line folder.
func cloneOf(t *testing.T, gitDir string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "c")
	mustRun(t, "clone", gitDir, root)
	return filepath.Join(root, "main")
}

func requireGit(t *testing.T) {
	t.Helper()
	skipOnWindows(t)
	for _, tool := range []string{"git", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not available")
		}
	}
}

// A name prefix shared by a commit and a blob: rev-parse, cat-file and show
// refuse it as git does, while commit-ish uses (log, ^{commit}) still take
// the commit.
func TestE2E_AmbiguousAbbreviationMatchesGit(t *testing.T) {
	var prefix string
	gitDir, lineDir := parityReposWith(t, func(gitDir string) {
		head, _ := gitRun(t, gitDir, "rev-parse", "HEAD")
		prefix = head[:4]
		var content []byte
		for i := 0; ; i++ {
			c := []byte(fmt.Sprintf("amb%d\n", i))
			h := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(c))), c...))
			if strings.HasPrefix(hex.EncodeToString(h[:]), prefix) {
				content = c
				break
			}
		}
		if err := os.WriteFile(filepath.Join(gitDir, "amb.txt"), content, 0o644); err != nil {
			t.Fatal(err)
		}
		gitScript(t, gitDir, "git add amb.txt; git commit -q -m amb")
	})
	for _, c := range []string{
		"rev-parse " + prefix,
		"rev-parse --verify " + prefix,
		"cat-file -t " + prefix,
		"cat-file -e " + prefix,
		"cat-file -p " + prefix,
		"show -s --oneline " + prefix,
		"log --oneline -1 " + prefix,
		"rev-parse " + prefix + "^{commit}",
	} {
		args := strings.Fields(c)
		want, wantCode := gitRun(t, gitDir, args...)
		got, gotCode := cairnRun(t, lineDir, args...)
		if (wantCode == 0) != (gotCode == 0) {
			t.Errorf("%s: exit %d, git exits %d", c, gotCode, wantCode)
		}
		// git echoes an unresolvable rev-parse argument on stdout; cairn says
		// nothing there (its error goes to stderr).
		if wantCode != 0 && args[0] == "rev-parse" {
			continue
		}
		if got != want {
			t.Errorf("%s:\n--- cairn\n%s--- git\n%s", c, got, want)
		}
	}
}

// ls-tree from inside a subfolder: "." lists that folder's contents, names
// are literal.
func TestE2E_LsTreeFromSubdirMatchesGit(t *testing.T) {
	gitDir, lineDir := parityRepos(t)
	for _, c := range []string{
		"ls-tree HEAD .", "ls-tree HEAD ./", "ls-tree HEAD lib", "ls-tree HEAD lib/", "ls-tree HEAD",
		"ls-tree -r HEAD .", "ls-tree HEAD ../docs", "ls-tree HEAD a.t*", "ls-tree HEAD a.txt lib",
		"ls-tree --full-name HEAD .", "ls-tree --name-only HEAD .", "ls-tree -d HEAD .",
	} {
		args := strings.Fields(c)
		want, wantCode := gitRun(t, filepath.Join(gitDir, "src"), args...)
		got, gotCode := cairnRun(t, filepath.Join(lineDir, "src"), args...)
		if got != want || (gotCode == 0) != (wantCode == 0) {
			t.Errorf("in src/: %s:\n--- cairn (%d)\n%s--- git (%d)\n%s", c, gotCode, got, wantCode, want)
		}
	}
}

// With no revision and stdin not a terminal, shortlog summarises a log read
// from stdin (git's behaviour), padding of --format=fuller included.
func TestE2E_ShortlogReadsStdinLikeGit(t *testing.T) {
	gitDir, lineDir := parityRepos(t)
	for _, logFmt := range []string{"medium", "fuller"} {
		input, _ := gitRun(t, gitDir, "log", "--format="+logFmt)
		in := filepath.Join(t.TempDir(), "in.txt")
		if err := os.WriteFile(in, []byte(input), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, flags := range [][]string{{}, {"-sn"}, {"-e"}, {"-c"}, {"-cs"}} {
			cmd := exec.Command("git", append([]string{"--no-pager", "shortlog"}, flags...)...)
			cmd.Dir = gitDir
			cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
			f, _ := os.Open(in)
			cmd.Stdin = f
			want, err := cmd.Output()
			_ = f.Close()
			if err != nil {
				t.Fatal(err)
			}
			old := os.Stdin
			f, _ = os.Open(in)
			os.Stdin = f
			got, _ := cairnRun(t, lineDir, append([]string{"shortlog"}, flags...)...)
			os.Stdin = old
			_ = f.Close()
			if got != string(want) {
				t.Errorf("log %s | shortlog %v:\n--- cairn\n%s--- git\n%s", logFmt, flags, got, want)
			}
		}
	}
}

var gitSignature = regexp.MustCompile(`(?m)^-- \n[0-9.]+\n`)

// A binary patch's base85 body depends on the deflate implementation (git's
// zlib vs Go's); only its sizes are compared.
var binaryBody = regexp.MustCompile(`(?s)GIT binary patch\nliteral (\d+)\n.*?\n\nliteral (\d+)\n.*?\n\n`)

func normSignature(s string) string {
	s = binaryBody.ReplaceAllString(s, "BINARY $1 $2\n")
	s = gitSignature.ReplaceAllString(s, "-- \nVER\n")
	return strings.ReplaceAll(s, "-- \ncairn "+buildVersion+"\n", "-- \nVER\n")
}

// format-patch --stdout: create/delete/mode summary lines, abbreviated index
// lines (a binary file's stays full), --no-stat, messages separated as git
// separates them.
func TestE2E_FormatPatchMatchesGit(t *testing.T) {
	gitDir, lineDir := parityRepos(t)
	for _, c := range []string{
		"--root", "--root --no-stat", "--root --no-binary", "--root --full-index", "-2", "-1 HEAD~4",
		"-1 --subject-prefix=RFC", "v1.0.0..main",
	} {
		args := append([]string{"format-patch", "--stdout", "--no-renames"}, strings.Fields(c)...)
		want, _ := gitRun(t, gitDir, args...)
		got, code := cairnRun(t, lineDir, args...)
		if code != 0 {
			t.Errorf("%s: exit %d", c, code)
		}
		if normSignature(got) != normSignature(want) {
			t.Errorf("format-patch %s:\n--- cairn\n%s--- git\n%s", c, got, want)
		}
	}
}

// Long non-ASCII subjects and names become RFC 2047 encoded words of at most
// 76 columns, folded onto continuation lines, exactly as git writes them;
// long ASCII subjects fold at spaces under 78 columns.
func TestE2E_FormatPatchRFC2047MatchesGit(t *testing.T) {
	requireGit(t)
	gitDir := t.TempDir()
	script := `git init -q -b main .
git config user.name "Zoë Ünïcode"; git config user.email z@x
n=0
for s in "héllo wörld: a rather long subject line that contains ünïcode characters and keeps going to force a fold of the header" \
  "short é" \
  "日本語のとても長い件名がここにあります日本語のとても長い件名がここにあります日本語のとても長い件名" \
  "an ascii first part that is quite long to make it past the limit é and then some more text after it in ascii again please" \
  "ascii only subject that is really quite long and goes on and on past seventy-eight columns for sure yes indeed it does" \
  "a=b?c_d é with specials = ? _ and 100% more" \
  "exactly sixty-one characters of é plus some ascii to test edges" ; do
  n=$((n+1)); echo $n > f$n; git add f$n; git commit -q -m "$s"
done`
	gitScript(t, gitDir, script)
	lineDir := cloneOf(t, gitDir)
	for _, c := range []string{"--root", `--root --subject-prefix=PATCH_v2_longer_prefix_here_for_testing_ü`} {
		args := append([]string{"format-patch", "--stdout"}, strings.Fields(c)...)
		want, _ := gitRun(t, gitDir, args...)
		got, code := cairnRun(t, lineDir, args...)
		if code != 0 {
			t.Errorf("%s: exit %d", c, code)
		}
		if normSignature(got) != normSignature(want) {
			t.Errorf("format-patch %s:\n--- cairn\n%s--- git\n%s", c, got, want)
		}
	}
}

// --follow uses git's similarity (shared bytes in line spans over the larger
// size, 50% inclusive): a file rewritten by k of its 20 lines is followed
// across the rename exactly when git follows it.
func TestE2E_FollowRenameSimilarityMatchesGit(t *testing.T) {
	requireGit(t)
	gitDir := t.TempDir()
	gitScript(t, gitDir, `git init -q -b main .; git config user.name a; git config user.email a@x`)
	ks := []int{0, 3, 6, 7, 8, 9, 10, 11, 12, 13, 15}
	lines := func(k int, changed int) string {
		var sb strings.Builder
		for i := 0; i < 20; i++ {
			if i < changed {
				fmt.Fprintf(&sb, "CHNG %d %02d %s\n", k, i, strings.Repeat("b", 20))
			} else {
				fmt.Fprintf(&sb, "line %d %02d %s\n", k, i, strings.Repeat("a", 20))
			}
		}
		return sb.String()
	}
	for _, k := range ks {
		if err := os.WriteFile(filepath.Join(gitDir, fmt.Sprintf("o%d.txt", k)), []byte(lines(k, 0)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitScript(t, gitDir, `git add -A; git commit -q -m base`)
	for _, k := range ks {
		_ = os.Remove(filepath.Join(gitDir, fmt.Sprintf("o%d.txt", k)))
		if err := os.WriteFile(filepath.Join(gitDir, fmt.Sprintf("n%d.txt", k)), []byte(lines(k, k)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitScript(t, gitDir, `git add -A; git commit -q -m rename`)
	lineDir := cloneOf(t, gitDir)
	for _, k := range ks {
		args := []string{"log", "--oneline", "--follow", "--", fmt.Sprintf("n%d.txt", k)}
		want, _ := gitRun(t, gitDir, args...)
		got, _ := cairnRun(t, lineDir, args...)
		if got != want {
			t.Errorf("k=%d:\n--- cairn\n%s--- git\n%s", k, got, want)
		}
	}
}
