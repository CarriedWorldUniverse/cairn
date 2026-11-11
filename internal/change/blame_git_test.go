package change

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
)

// Blame is cairn's own (blame.go), so — like the graph queries — it is held
// to git's answers on generated histories: lines added, removed and rewritten
// commit after commit, side branches merged back, the file renamed (once
// untouched, once edited in the same commit).

// blameHistory builds such a history with git plumbing and returns the repo
// directory, the tip, and the file's final path.
//
// The file is two regions around a fixed separator. The trunk only ever
// edits the first and a side branch only the second, so a merge commit's
// content is known without running a merge: trunk's first region, the side
// branch's second.
func blameHistory(t *testing.T, rng *rand.Rand, commits int, shaped bool) (dir, tip, path string) {
	t.Helper()
	dir = t.TempDir()
	gitOut(t, dir, nil, "init", "-q")
	next := 0
	fresh := func() string {
		next++
		if shaped {
			// Code-shaped text: blank lines, braces and indentation repeat,
			// which is where two correct diffs can disagree about WHICH
			// identical line is the new one.
			switch rng.Intn(6) {
			case 0:
				return ""
			case 1:
				return "}"
			case 2:
				return fmt.Sprintf("func f%d() {", next)
			default:
				return fmt.Sprintf("\tstatement(%d)", next)
			}
		}
		return fmt.Sprintf("line %d", next)
	}
	mutate := func(region []string) []string {
		out := append([]string(nil), region...)
		for e := 1 + rng.Intn(3); e > 0; e-- {
			switch op := rng.Intn(3); {
			case op == 0 || len(out) == 0: // insert a run
				i := rng.Intn(len(out) + 1)
				var run []string
				for n := 1 + rng.Intn(4); n > 0; n-- {
					run = append(run, fresh())
				}
				out = append(out[:i], append(run, out[i:]...)...)
			case op == 1: // delete a run
				i := rng.Intn(len(out))
				n := min(1+rng.Intn(3), len(out)-i)
				out = append(out[:i], out[i+n:]...)
			default: // rewrite a line
				out[rng.Intn(len(out))] = fresh()
			}
		}
		return out
	}
	sep := []string{"==== separator 1", "==== separator 2", "==== separator 3"}
	path = "old/dir/f.txt"
	n := 0
	commit := func(a, b []string, parents ...string) string {
		t.Helper()
		n++
		content := strings.Join(append(append(append([]string(nil), a...), sep...), b...), "\n") + "\n"
		work := filepath.Join(dir, "content")
		if err := os.WriteFile(work, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		blob := gitOut(t, dir, nil, "hash-object", "-w", work)
		// A second file that changes every commit, so most commits differ
		// from their parent somewhere OTHER than the blamed file too.
		if err := os.WriteFile(work, []byte(fmt.Sprintf("other %d\n", n)), 0o644); err != nil {
			t.Fatal(err)
		}
		other := gitOut(t, dir, nil, "hash-object", "-w", work)
		index := filepath.Join(dir, "index.tmp")
		_ = os.Remove(index)
		env := []string{"GIT_INDEX_FILE=" + index}
		gitOut(t, dir, env, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
		gitOut(t, dir, env, "update-index", "--add", "--cacheinfo", "100644,"+other+",other/o.txt")
		tree := gitOut(t, dir, env, "write-tree")
		args := []string{"commit-tree", tree, "-m", fmt.Sprintf("c%d", n)}
		for _, p := range parents {
			args = append(args, "-p", p)
		}
		stamp := fmt.Sprintf("%d +0000", 1700000000+n*60)
		return gitOut(t, dir, []string{
			"GIT_AUTHOR_NAME=a" + fmt.Sprint(n%3), "GIT_AUTHOR_EMAIL=a@t", "GIT_AUTHOR_DATE=" + stamp,
			"GIT_COMMITTER_NAME=c", "GIT_COMMITTER_EMAIL=c@t", "GIT_COMMITTER_DATE=" + stamp,
		}, args...)
	}

	var a, b []string
	for i := 0; i < 12; i++ {
		a, b = append(a, fresh()), append(b, fresh())
	}
	tip = commit(a, b)
	var side string // the open side branch's tip, "" when none
	var sideB []string
	for i := 1; i < commits; i++ {
		switch {
		case i == commits/3: // a pure rename: same content, new path
			path = "mid/f.txt"
			tip = commit(a, b, tip)
		case i == 2*commits/3: // a rename with an edit in the same commit
			path = "dir/f.txt"
			a = mutate(a)
			tip = commit(a, b, tip)
		case side == "" && rng.Intn(5) == 0: // fork
			sideB = mutate(b)
			side = commit(a, sideB, tip)
		case side != "" && rng.Intn(3) == 0: // side branch moves on
			sideB = mutate(sideB)
			// The side branch still has trunk's first region as of the fork;
			// its own commits only ever show its second.
			side = commit(a, sideB, side)
		case side != "" && rng.Intn(3) == 0: // merge it back
			b = sideB
			tip = commit(a, b, tip, side)
			side = ""
		default:
			a = mutate(a)
			tip = commit(a, b, tip)
		}
	}
	gitOut(t, dir, nil, "update-ref", "refs/heads/main", tip)
	return dir, tip, path
}

// gitBlame is `git blame --porcelain`: the commit each line is attributed to.
func gitBlame(t *testing.T, dir, rev, path string) []string {
	t.Helper()
	var out []string
	for _, l := range strings.Split(gitOut(t, dir, nil, "blame", "--porcelain", rev, "--", path), "\n") {
		f := strings.Fields(l)
		if len(f) >= 3 && len(f[0]) == 40 && !strings.HasPrefix(l, "\t") && strings.Trim(f[0], "0123456789abcdef") == "" {
			out = append(out, f[0])
		}
	}
	return out
}

// checkBlameAgainstGit requires Blame to attribute every line as git does.
//
// With ambiguous set, a line whose text occurs more than once in the file may
// differ, within a small budget. Such a history has commits for which two
// DIFFERENT edit scripts are equally short — both delete one of two identical
// "}" lines, say — and cairn's Myers and git's xdiff need not pick the same
// one; neither answer is wrong. What may never differ is a line that is the
// only one of its kind.
func checkBlameAgainstGit(t *testing.T, e *Engine, dir, tip, path string, ambiguous bool) {
	t.Helper()
	want := gitBlame(t, dir, tip, path)
	got, err := e.Blame(tip, path)
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("Blame returned %d lines, git blame %d", len(got), len(want))
	}
	// Raw, not through gitOut: that trims, and a file may start with a tab
	// or end in blank lines.
	show := exec.Command("git", "show", tip+":"+path)
	show.Dir = dir
	raw, err := show.Output()
	if err != nil {
		t.Fatalf("git show: %v", err)
	}
	content := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	occurs := map[string]int{}
	for _, l := range content {
		occurs[l]++
	}
	differ := 0
	for i := range got {
		if got[i].Text != content[i] {
			t.Fatalf("line %d text = %q, the file has %q", i+1, got[i].Text, content[i])
		}
		if got[i].Commit == want[i] {
			continue
		}
		if ambiguous && occurs[content[i]] > 1 {
			differ++
			continue
		}
		t.Errorf("line %d %q: blamed on %.8s, git blames %.8s", i+1, got[i].Text, got[i].Commit, want[i])
	}
	if differ*20 > len(got) {
		t.Errorf("%d of %d lines blamed differently from git — more than repeated lines explain", differ, len(got))
	}
	// Author and date come from the blamed commit itself.
	for i := range got {
		if a := gitOut(t, dir, nil, "show", "-s", "--format=%an", got[i].Commit); got[i].Author != a {
			t.Fatalf("line %d: author %q, commit %.8s says %q", i+1, got[i].Author, got[i].Commit, a)
		}
		if i > 5 {
			break
		}
	}
}

func TestBlameAgreesWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	for _, shaped := range []bool{false, true} {
		for seed := int64(1); seed <= 6; seed++ {
			name := fmt.Sprintf("unique-lines/seed%d", seed)
			if shaped {
				name = fmt.Sprintf("code-shaped/seed%d", seed)
			}
			t.Run(name, func(t *testing.T) {
				rng := rand.New(rand.NewSource(seed))
				dir, tip, path := blameHistory(t, rng, 60, shaped)
				repo, err := git.PlainOpen(dir)
				if err != nil {
					t.Fatal(err)
				}
				checkBlameAgainstGit(t, &Engine{git: repo}, dir, tip, path, shaped)
			})
		}
	}
}

// The same check through a real engine: objects imported into a pack and
// read by the fast object reader, the commit graph kept on disk.
func TestBlameAgreesWithGitOnAnImportedRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	dir, tip, path := blameHistory(t, rand.New(rand.NewSource(42)), 80, false)
	store := t.TempDir()
	e, err := Open(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.ImportFromRemote(dir); err != nil {
		t.Fatalf("import: %v", err)
	}
	checkBlameAgainstGit(t, e, dir, tip, path, false)
	_ = e.Close()

	// And again from a cold start, off the persisted commit graph.
	e2, err := Open(store)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e2.Close() }()
	checkBlameAgainstGit(t, e2, dir, tip, path, false)
}

func BenchmarkBlame(b *testing.B) {
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("git CLI not available")
	}
	t := &testing.T{}
	dir, tip, path := blameHistory(t, rand.New(rand.NewSource(9)), 300, false)
	store := b.TempDir()
	e, err := Open(store)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	if _, err := e.ImportFromRemote(dir); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Blame(tip, path); err != nil {
			b.Fatal(err)
		}
	}
}
