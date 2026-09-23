package change

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
)

// Cairn's graph queries are checked against git itself, on generated
// histories: merges, criss-crosses, unrelated roots — and three clocks, since
// the clock is exactly what broke go-git's walks. git runs with a
// commit-graph, so its answers come from generation numbers, not timestamps:
// it is the oracle whatever the dates say.

type clockMode int

const (
	clockOrdered clockMode = iota // each commit later than the last
	clockEqual                    // every commit in the same second
	clockSkewed                   // random dates, unrelated to topology
)

func (m clockMode) String() string {
	return [...]string{"ordered", "equal", "skewed"}[m]
}

type genRepo struct {
	dir     string
	commits []string
}

func gitOut(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("git %v: %v\n%s", args, err, stderr)
	}
	return strings.TrimSpace(string(out))
}

// generate builds n commits: mostly one parent, some merges of two or three
// earlier commits, and the occasional new root.
func generate(t *testing.T, rng *rand.Rand, n int, mode clockMode) genRepo {
	t.Helper()
	dir := t.TempDir()
	gitOut(t, dir, nil, "init", "-q")
	tree := gitOut(t, dir, nil, "hash-object", "-t", "tree", "-w", "--stdin")
	g := genRepo{dir: dir}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		var at time.Time
		switch mode {
		case clockOrdered:
			at = base.Add(time.Duration(i) * time.Minute)
		case clockEqual:
			at = base
		case clockSkewed:
			at = base.Add(time.Duration(rng.Intn(48*60)-24*60) * time.Minute)
		}
		args := []string{"commit-tree", tree, "-m", fmt.Sprintf("c%d", i)}
		if i > 0 && rng.Intn(20) != 0 {
			k := 1
			if r := rng.Intn(10); r < 2 {
				k = 2
			} else if r == 2 {
				k = 3
			}
			used := map[int]bool{}
			for j := 0; j < k; j++ {
				// Favour recent commits so the history has depth.
				p := i - 1 - rng.Intn(min(i, 6))
				if rng.Intn(4) == 0 {
					p = rng.Intn(i)
				}
				if used[p] {
					continue
				}
				used[p] = true
				args = append(args, "-p", g.commits[p])
			}
		}
		stamp := strconv.FormatInt(at.Unix(), 10) + " +0000"
		env := []string{
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_AUTHOR_DATE=" + stamp,
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_COMMITTER_DATE=" + stamp,
		}
		sha := gitOut(t, dir, env, args...)
		g.commits = append(g.commits, sha)
		gitOut(t, dir, nil, "update-ref", fmt.Sprintf("refs/heads/c%d", i), sha)
	}
	gitOut(t, dir, nil, "commit-graph", "write", "--reachable")
	return g
}

// gitMergeBases is `git merge-base --all`, sorted; empty when unrelated.
func gitMergeBases(t *testing.T, dir, a, b string) []string {
	t.Helper()
	cmd := exec.Command("git", "merge-base", "--all", a, b)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil
		}
		t.Fatalf("git merge-base: %v", err)
	}
	bases := strings.Fields(string(out))
	sort.Strings(bases)
	return bases
}

func TestGraphQueriesAgreeWithGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	for _, mode := range []clockMode{clockOrdered, clockEqual, clockSkewed} {
		for seed := int64(1); seed <= 4; seed++ {
			t.Run(fmt.Sprintf("%s/seed%d", mode, seed), func(t *testing.T) {
				rng := rand.New(rand.NewSource(seed))
				g := generate(t, rng, 40, mode)
				repo, err := git.PlainOpen(g.dir)
				if err != nil {
					t.Fatal(err)
				}
				e := &Engine{git: repo}
				n := len(g.commits)
				for k := 0; k < 60; k++ {
					a, b := g.commits[rng.Intn(n)], g.commits[rng.Intn(n)]
					want := gitMergeBases(t, g.dir, a, b)

					got, err := e.mergeBases(a, b)
					if err != nil {
						t.Fatalf("mergeBases: %v", err)
					}
					sorted := append([]string(nil), got...)
					sort.Strings(sorted)
					if strings.Join(sorted, ",") != strings.Join(want, ",") {
						t.Fatalf("mergeBases(%.8s, %.8s) = %v, git says %v", a, b, sorted, want)
					}

					counts := strings.Fields(gitOut(t, g.dir, nil, "rev-list", "--left-right", "--count", a+"..."+b))
					wa, _ := strconv.Atoi(counts[0])
					wb, _ := strconv.Atoi(counts[1])
					ga, gb, err := e.divergence(a, b)
					if err != nil {
						t.Fatalf("divergence: %v", err)
					}
					if ga != wa || gb != wb {
						t.Fatalf("divergence(%.8s, %.8s) = %d/%d, git says %d/%d", a, b, ga, gb, wa, wb)
					}

					anc, err := e.isAncestor(a, b)
					if err != nil {
						t.Fatalf("isAncestor: %v", err)
					}
					wantAnc := exec.Command("git", "merge-base", "--is-ancestor", a, b)
					wantAnc.Dir = g.dir
					if isAnc := wantAnc.Run() == nil; anc != isAnc {
						t.Fatalf("isAncestor(%.8s, %.8s) = %v, git says %v", a, b, anc, isAnc)
					}

					set, err := e.ancestorSet(b)
					if err != nil {
						t.Fatalf("ancestorSet: %v", err)
					}
					if wantN, _ := strconv.Atoi(gitOut(t, g.dir, nil, "rev-list", "--count", b)); len(set) != wantN {
						t.Fatalf("ancestorSet(%.8s) has %d commits, git says %d", b, len(set), wantN)
					}
					in, err := e.mergeBaseIn(a, set)
					if err != nil {
						t.Fatalf("mergeBaseIn: %v", err)
					}
					if (in == "") != (len(want) == 0) || (in != "" && !contains(want, in)) {
						t.Fatalf("mergeBaseIn(%.8s, ancestry of %.8s) = %q, git's bases are %v", a, b, in, want)
					}
				}
			})
		}
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
