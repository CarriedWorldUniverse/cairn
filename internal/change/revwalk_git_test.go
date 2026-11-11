package change

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
)

// The revision walk, revision syntax and tree diff are checked against git
// itself, like the graph queries in graph_git_test.go.

func gitTry(dir string, env []string, args ...string) (string, bool) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err == nil
}

func walkSHAs(t *testing.T, e *Engine, o WalkOptions) string {
	t.Helper()
	var out []string
	if err := e.WalkCommits(o, func(c *CommitMeta) error { out = append(out, c.SHA); return nil }); err != nil {
		t.Fatalf("WalkCommits: %v", err)
	}
	return strings.Join(out, "\n")
}

func TestRevWalkAgreesWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	for _, mode := range []clockMode{clockOrdered, clockEqual, clockSkewed} {
		for seed := int64(1); seed <= 3; seed++ {
			t.Run(fmt.Sprintf("%s/seed%d", mode, seed), func(t *testing.T) {
				rng := rand.New(rand.NewSource(seed))
				g := generate(t, rng, 40, mode)
				repo, err := git.PlainOpen(g.dir)
				if err != nil {
					t.Fatal(err)
				}
				e := &Engine{git: repo}
				n := len(g.commits)
				for k := 0; k < 25; k++ {
					a, b := g.commits[rng.Intn(n)], g.commits[rng.Intn(n)]

					// Everything reachable, git's default order.
					if got, want := walkSHAs(t, e, WalkOptions{Include: []string{a}}), gitOut(t, g.dir, nil, "rev-list", a); got != want {
						t.Fatalf("rev-list %.8s:\n got %s\nwant %s", a, got, want)
					}
					if got, want := walkSHAs(t, e, WalkOptions{Include: []string{a}, FirstParent: true}), gitOut(t, g.dir, nil, "rev-list", "--first-parent", a); got != want {
						t.Fatalf("rev-list --first-parent %.8s:\n got %s\nwant %s", a, got, want)
					}
					if got, want := walkSHAs(t, e, WalkOptions{Include: []string{a}, Limit: 5, Skip: 2, Reverse: true}), gitOut(t, g.dir, nil, "rev-list", "-n", "5", "--skip=2", "--reverse", a); got != want {
						t.Fatalf("rev-list -n5 --skip=2 --reverse %.8s:\n got %s\nwant %s", a, got, want)
					}
					// A range, topologically ordered: exact in git whatever the
					// clocks say (it walks by generation there too).
					rng2 := WalkOptions{Include: []string{a}, Exclude: []string{b}, Topo: true}
					if got, want := walkSHAs(t, e, rng2), gitOut(t, g.dir, nil, "rev-list", "--topo-order", a, "^"+b); got != want {
						t.Fatalf("rev-list --topo-order %.8s ^%.8s:\n got %s\nwant %s", a, b, got, want)
					}
					wantN, _ := strconv.Atoi(gitOut(t, g.dir, nil, "rev-list", "--count", "--topo-order", a, "^"+b))
					if gotN, err := e.CountCommits(WalkOptions{Include: []string{a}, Exclude: []string{b}}); err != nil || gotN != wantN {
						t.Fatalf("count %.8s ^%.8s = %d (%v), git says %d", a, b, gotN, err, wantN)
					}
					// git's date-ordered range walk is only trustworthy when
					// the clocks agree with the topology.
					if mode == clockOrdered {
						if got, want := walkSHAs(t, e, WalkOptions{Include: []string{a}, Exclude: []string{b}}), gitOut(t, g.dir, nil, "rev-list", a, "^"+b); got != want {
							t.Fatalf("rev-list %.8s ^%.8s:\n got %s\nwant %s", a, b, got, want)
						}
						rr, err := e.ParseRevisions([]string{a + "..." + b})
						if err != nil {
							t.Fatal(err)
						}
						if got, want := walkSHAs(t, e, WalkOptions{Include: rr.Include, Exclude: rr.Exclude}), gitOut(t, g.dir, nil, "rev-list", a+"..."+b); got != want {
							t.Fatalf("rev-list %.8s...%.8s:\n got %s\nwant %s", a, b, got, want)
						}
					}

					// Revision syntax.
					for _, suffix := range []string{"", "~1", "^", "^2", "~2^2", "^^", "~3", "^1~1^2", "^0", "^{commit}"} {
						rev := "c" + strconv.Itoa(rng.Intn(n)) + suffix
						want, ok := gitTry(g.dir, nil, "rev-parse", "--verify", "-q", rev+"^{commit}")
						got, err := e.ResolveCommit(rev)
						if ok != (err == nil) || (ok && got != want) {
							t.Fatalf("ResolveCommit(%s) = %q, %v; git says %q, ok=%v", rev, got, err, want, ok)
						}
					}
					short := a[:6+rng.Intn(6)]
					if got, err := e.ResolveCommit(short); err != nil || got != a {
						t.Fatalf("ResolveCommit(%s) = %q, %v; want %s", short, got, err, a)
					}
				}
			})
		}
	}
}

// contentRepo builds a git history with real file changes: random edits,
// adds, deletes and merges over a small tree.
func contentRepo(t *testing.T, rng *rand.Rand, n int) genRepo {
	t.Helper()
	dir := t.TempDir()
	gitOut(t, dir, nil, "init", "-q")
	idx := filepath.Join(dir, ".git", "test-index")
	files := []string{"a.txt", "b.txt", "dir/c.txt", "dir/sub/d.txt", "dir/e.go", "other/f.txt", "bin.dat"}
	g := genRepo{dir: dir}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		env := []string{"GIT_INDEX_FILE=" + idx}
		var parents []string
		if i > 0 {
			p := i - 1 - rng.Intn(min(i, 4))
			parents = append(parents, g.commits[p])
			if rng.Intn(5) == 0 {
				if q := rng.Intn(i); q != p {
					parents = append(parents, g.commits[q])
				}
			}
			gitOut(t, dir, env, "read-tree", parents[0])
		} else {
			gitOut(t, dir, env, "read-tree", "--empty")
		}
		for k := 0; k < 1+rng.Intn(3); k++ {
			f := files[rng.Intn(len(files))]
			if rng.Intn(6) == 0 {
				_, _ = gitTry(dir, env, "update-index", "--force-remove", f)
				continue
			}
			var sb strings.Builder
			for l := 0; l < 12; l++ {
				if rng.Intn(4) == 0 {
					fmt.Fprintf(&sb, "line %d rev %d\n", l, i)
				} else {
					fmt.Fprintf(&sb, "line %d\n", l)
				}
			}
			content := sb.String()
			if f == "bin.dat" {
				content = fmt.Sprintf("\x00\x01bin %d", i)
			} else if rng.Intn(5) == 0 {
				content = strings.TrimSuffix(content, "\n")
			}
			cmd := exec.Command("git", "hash-object", "-w", "--stdin")
			cmd.Dir = dir
			cmd.Stdin = strings.NewReader(content)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("hash-object: %v", err)
			}
			mode := "100644"
			if rng.Intn(8) == 0 {
				mode = "100755"
			}
			gitOut(t, dir, env, "update-index", "--add", "--cacheinfo", mode+","+strings.TrimSpace(string(out))+","+f)
		}
		tree := gitOut(t, dir, env, "write-tree")
		stamp := strconv.FormatInt(base.Add(time.Duration(i)*time.Minute).Unix(), 10) + " +0000"
		cenv := []string{
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_AUTHOR_DATE=" + stamp,
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_COMMITTER_DATE=" + stamp,
		}
		args := []string{"commit-tree", tree, "-m", fmt.Sprintf("c%d", i)}
		for _, p := range parents {
			args = append(args, "-p", p)
		}
		sha := gitOut(t, dir, cenv, args...)
		g.commits = append(g.commits, sha)
		gitOut(t, dir, nil, "update-ref", fmt.Sprintf("refs/heads/c%d", i), sha)
	}
	return g
}

func TestTreeDiffAndPathWalkAgreeWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	for seed := int64(1); seed <= 3; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			g := contentRepo(t, rng, 40)
			repo, err := git.PlainOpen(g.dir)
			if err != nil {
				t.Fatal(err)
			}
			e := &Engine{git: repo}
			n := len(g.commits)
			for _, paths := range [][]string{{"dir"}, {"a.txt"}, {"dir/sub/d.txt", "other"}, {"*.go"}, {"dir/c.txt"}} {
				tip := g.commits[n-1-rng.Intn(5)]
				want := gitOut(t, g.dir, nil, append([]string{"rev-list", tip, "--"}, paths...)...)
				if got := walkSHAs(t, e, WalkOptions{Include: []string{tip}, Paths: paths}); got != want {
					t.Fatalf("rev-list %.8s -- %v:\n got %s\nwant %s", tip, paths, got, want)
				}
			}
			for k := 0; k < 30; k++ {
				a, b := g.commits[rng.Intn(n)], g.commits[rng.Intn(n)]
				changes, err := e.TreeChanges(a, b, nil)
				if err != nil {
					t.Fatal(err)
				}
				var ns []string
				for _, ch := range changes {
					ns = append(ns, fmt.Sprintf("%c\t%s", ch.Status(), ch.Path))
				}
				if got, want := strings.Join(ns, "\n"), gitOut(t, g.dir, nil, "diff", "--name-status", "--no-renames", a, b); got != want {
					t.Fatalf("name-status %.8s %.8s:\n got %s\nwant %s", a, b, got, want)
				}
				sub, err := e.TreeChanges(a, b, NewPathspec([]string{"dir"}))
				if err != nil {
					t.Fatal(err)
				}
				var subNames []string
				for _, ch := range sub {
					subNames = append(subNames, ch.Path)
				}
				if got, want := strings.Join(subNames, "\n"), gitOut(t, g.dir, nil, "diff", "--name-only", "--no-renames", a, b, "--", "dir"); got != want {
					t.Fatalf("name-only -- dir %.8s %.8s:\n got %s\nwant %s", a, b, got, want)
				}
				// The rendered patch must turn a's tree into b's when git
				// applies it — text, binary, modes, missing final newlines.
				patches, err := e.CommitPatches(a, b, nil, PatchOptions{Context: 3, Binary: true}, false)
				if err != nil {
					t.Fatal(err)
				}
				var patch strings.Builder
				for _, p := range patches {
					patch.WriteString(p.Text())
				}
				idx := filepath.Join(t.TempDir(), "index")
				env := []string{"GIT_INDEX_FILE=" + idx}
				gitOut(t, g.dir, env, "read-tree", a)
				if patch.Len() > 0 {
					cmd := exec.Command("git", "apply", "--cached", "-")
					cmd.Dir = g.dir
					cmd.Env = append(os.Environ(), env...)
					cmd.Stdin = strings.NewReader(patch.String())
					if out, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("git apply %.8s..%.8s: %v\n%s\n%s", a, b, err, out, patch.String())
					}
				}
				if got, want := gitOut(t, g.dir, env, "write-tree"), gitOut(t, g.dir, nil, "rev-parse", b+"^{tree}"); got != want {
					t.Fatalf("patch %.8s..%.8s applied to tree %s, want %s", a, b, got, want)
				}
				// Stat block, byte for byte.
				stat := strings.TrimRight(FormatStat(patches, 80), "\n")
				if want := strings.TrimRight(gitRaw(t, g.dir, "diff", "--stat", "--no-renames", a, b), "\n"); stat != want {
					t.Fatalf("stat %.8s %.8s:\n got\n%s\nwant\n%s", a, b, stat, want)
				}
			}
		})
	}
}

// gitRaw is gitOut without trimming leading whitespace.
func gitRaw(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func TestRevisionSyntaxOnLines(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	c1 := seedLineTip(t, e, main.ID, map[string][]byte{"f.txt": []byte("1\n"), "d/g.txt": []byte("g\n")})
	c2 := seedLineTip(t, e, main.ID, map[string][]byte{"f.txt": []byte("2\n"), "d/g.txt": []byte("g\n")})
	if err := e.Tag("v1", c1, "t"); err != nil {
		t.Fatal(err)
	}
	exp, err := e.CreateLine("exp", main.ID)
	if err != nil {
		t.Fatal(err)
	}
	c3 := seedLineTip(t, e, exp.ID, map[string][]byte{"f.txt": []byte("3\n"), "d/g.txt": []byte("g\n")})

	for rev, want := range map[string]string{
		"HEAD": c2, "@": c2, "main": c2, "main~1": c1, "HEAD^": c1, "v1": c1, "exp": c3, "exp~1": c2,
		"exp^^": c1, c3[:8]: c3, c1: c1, "refs/heads/exp": c3, "tags/v1": c1,
	} {
		got, err := e.ResolveCommit(rev)
		if err != nil || got != want {
			t.Errorf("ResolveCommit(%q) = %q, %v; want %s", rev, got, err, want)
		}
	}
	e.SetHeadLine("exp")
	if got, _ := e.ResolveCommit("HEAD"); got != c3 {
		t.Errorf("HEAD on exp = %s, want %s", got, c3)
	}
	for _, bad := range []string{"nosuch", "main~9", "main^2", "", "deadbeefdead"} {
		if _, err := e.ResolveCommit(bad); err == nil {
			t.Errorf("ResolveCommit(%q) succeeded", bad)
		}
	}
	// HEAD is the SEALED tip: an open working commit on the line is not it.
	ch, _ := e.CreateChange(exp.ID, "w")
	if _, _, err := e.SnapshotWorking(ch.ID, map[string]TreeEntry{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.ResolveCommit("exp"); got != c3 {
		t.Errorf("exp with an open working change = %s, want sealed tip %s", got, c3)
	}
	sha, typ, err := e.ResolveObject("main:d/g.txt")
	if err != nil || typ != "blob" {
		t.Fatalf("ResolveObject(main:d/g.txt) = %s %s %v", sha, typ, err)
	}
	if data, _ := e.ReadBlob(sha); string(data) != "g\n" {
		t.Errorf("main:d/g.txt = %q", data)
	}
	if _, typ, _ := e.ResolveObject("main:d"); typ != "tree" {
		t.Errorf("main:d is a %s, want tree", typ)
	}
	if _, typ, _ := e.ResolveObject("HEAD^{tree}"); typ != "tree" {
		t.Errorf("HEAD^{tree} is a %s", typ)
	}
	rr, err := e.ParseRevisions([]string{"v1..exp"})
	if err != nil || len(rr.Include) != 1 || rr.Include[0] != c3 || len(rr.Exclude) != 1 || rr.Exclude[0] != c1 {
		t.Errorf("v1..exp = %+v, %v", rr, err)
	}
	if n, _ := e.CountCommits(WalkOptions{Include: rr.Include, Exclude: rr.Exclude}); n != 2 {
		t.Errorf("v1..exp has %d commits, want 2", n)
	}
	if tag, depth, err := e.Describe(c3, nil); err != nil || tag != "v1" || depth != 2 {
		t.Errorf("Describe = %q %d %v, want v1 2", tag, depth, err)
	}
}

// Describe is checked against `git describe --tags` on generated histories,
// under every clock: the nearest tag and the exact commit count from it.
func TestDescribeAgreesWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	for _, mode := range []clockMode{clockOrdered, clockEqual, clockSkewed} {
		for seed := int64(1); seed <= 3; seed++ {
			t.Run(fmt.Sprintf("%s/seed%d", mode, seed), func(t *testing.T) {
				rng := rand.New(rand.NewSource(seed))
				g := generate(t, rng, 40, mode)
				e := newTestEngine(t)
				repo, err := git.PlainOpen(g.dir)
				if err != nil {
					t.Fatal(err)
				}
				e.git = repo
				tagOf := map[string]string{}
				for i := 0; i < 5; i++ {
					c := g.commits[rng.Intn(len(g.commits))]
					if tagOf[c] != "" {
						continue
					}
					name := fmt.Sprintf("t%d", i)
					tagOf[c] = name
					gitOut(t, g.dir, nil, "tag", name, c)
					if err := e.Tag(name, c, "t"); err != nil {
						t.Fatal(err)
					}
				}
				for _, c := range g.commits {
					tag, depth, err := e.Describe(c, nil)
					if err != nil {
						t.Fatal(err)
					}
					// --candidates is raised so git's answer is the true
					// nearest tag; the depth must then be the exact count.
					want, ok := gitTry(g.dir, nil, "describe", "--tags", "--long", "--candidates=100", c)
					if !ok {
						if tag != "" {
							t.Fatalf("Describe(%.8s) = %s-%d, git finds no tag", c, tag, depth)
						}
						continue
					}
					if tag == "" {
						t.Fatalf("Describe(%.8s) found no tag, git says %s", c, want)
					}
					n, _ := strconv.Atoi(gitOut(t, g.dir, nil, "rev-list", "--count", tag+".."+c))
					if depth != n {
						t.Fatalf("Describe(%.8s) = %s-%d, but %s..%.8s has %d commits (git: %s)", c, tag, depth, tag, c, n, want)
					}
					wantTag := want[:strings.LastIndex(want[:strings.LastIndex(want, "-")], "-")]
					wn, _ := strconv.Atoi(gitOut(t, g.dir, nil, "rev-list", "--count", wantTag+".."+c))
					// git's own pick leans on commit dates; with honest clocks
					// the two agree, otherwise cairn's is never the farther.
					if (mode == clockOrdered && depth != wn) || depth > wn {
						t.Fatalf("Describe(%.8s) = %s-%d, git picked %s at depth %d", c, tag, depth, wantTag, wn)
					}
				}
			})
		}
	}
}

// --follow disables git's parent pruning: a TREESAME merge is hidden but
// every parent is still walked, whichever side the merge took.
func TestFollowThroughMergesAgreesWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	for _, take := range []string{"main", "side"} {
		t.Run("takes-"+take, func(t *testing.T) {
			dir := t.TempDir()
			env := []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}
			clock := 1700000000
			g := func(args ...string) {
				t.Helper()
				clock += 100
				ts := strconv.Itoa(clock) + " +0000"
				gitOut(t, dir, append(env, "GIT_AUTHOR_DATE="+ts, "GIT_COMMITTER_DATE="+ts), args...)
			}
			write := func(name, body string) {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			g("init", "-q", "-b", "main")
			write("a.txt", "base\n")
			g("add", ".")
			g("commit", "-q", "-m", "base")
			g("checkout", "-q", "-b", "side")
			write("a.txt", "side\n")
			g("commit", "-q", "-am", "side-a")
			g("checkout", "-q", "main")
			write("a.txt", "main\n")
			g("commit", "-q", "-am", "main-a")
			if take == "main" {
				g("merge", "-q", "-s", "ours", "--no-edit", "side")
			} else {
				g("merge", "-q", "-X", "theirs", "--no-edit", "side")
			}
			repo, err := git.PlainOpen(dir)
			if err != nil {
				t.Fatal(err)
			}
			e := &Engine{git: repo}
			tip := gitOut(t, dir, nil, "rev-parse", "HEAD")
			want := gitOut(t, dir, nil, "log", "--format=%H", "--follow", tip, "--", "a.txt")
			if got := walkSHAs(t, e, WalkOptions{Include: []string{tip}, Paths: []string{"a.txt"}, Follow: true}); got != want {
				t.Fatalf("follow:\n got %s\nwant %s", got, want)
			}
		})
	}
}

// A name with a space gets git's trailing tab on its ---/+++ lines.
func TestPatchSpaceNamesAgreeWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	dir := t.TempDir()
	env := []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"}
	gitOut(t, dir, env, "init", "-q", "-b", "main")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("my file.txt", "one\ntwo\n")
	write("gone away.txt", "bye\n")
	write("plain.txt", "p\n")
	gitOut(t, dir, env, "add", ".")
	gitOut(t, dir, env, "commit", "-q", "-m", "one")
	write("my file.txt", "one\nTWO\n")
	write("new one.txt", "hi\n")
	write("plain.txt", "q\n")
	if err := os.Remove(filepath.Join(dir, "gone away.txt")); err != nil {
		t.Fatal(err)
	}
	gitOut(t, dir, env, "add", "-A")
	gitOut(t, dir, env, "commit", "-q", "-m", "two")
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{git: repo}
	patches, err := e.CommitPatches(gitOut(t, dir, nil, "rev-parse", "HEAD~1"), gitOut(t, dir, nil, "rev-parse", "HEAD"), nil, PatchOptions{Context: 3, FullIndex: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	for _, p := range patches {
		got.WriteString(p.Text())
	}
	if want := gitRaw(t, dir, "diff", "--no-renames", "--full-index", "HEAD~1", "HEAD"); got.String() != want {
		t.Fatalf("patch:\n got\n%s\nwant\n%s", got.String(), want)
	}
}

// QuotePath escapes control characters exactly as git does.
func TestQuotePathAgreesWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	dir := t.TempDir()
	gitOut(t, dir, nil, "init", "-q")
	names := []string{"a\ab", "v\vv", "f\ff", "b\bb", "t\tt", "q\"q", "é"}
	var want []string
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitOut(t, dir, nil, "add", ".")
	for _, l := range strings.Split(gitOut(t, dir, nil, "ls-files"), "\n") {
		want = append(want, l)
	}
	var got []string
	for _, n := range names {
		got = append(got, QuotePath(n))
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
