package change

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

// DiffCommits no longer flattens both trees; it walks them together and skips
// every subtree they share (diffTreeRefs). The promise is that the result is
// what the full comparison gave. This checks it on generated pairs of trees:
// files added, removed, edited, turned executable or into symlinks, files
// that become directories and directories that become files, gitlinks.
func TestDiffTreeRefsMatchesFullComparison(t *testing.T) {
	e := newTestEngine(t)
	rng := rand.New(rand.NewSource(11))
	dirs := []string{"", "a/", "a/b/", "a/b/c/", "d/", "d/e/", "z/y/x/w/"}
	modes := []EntryMode{ModeRegular, ModeRegular, ModeRegular, ModeExecutable, ModeSymlink}
	blob := func() TreeEntry {
		ent := blobEntry(t, e, fmt.Sprintf("content %d\nsecond line\n", rng.Intn(40)))
		ent.Mode = modes[rng.Intn(len(modes))]
		return ent
	}
	gen := func() map[string]TreeEntry {
		m := map[string]TreeEntry{}
		for n := 5 + rng.Intn(25); n > 0; n-- {
			m[dirs[rng.Intn(len(dirs))]+fmt.Sprintf("f%d", rng.Intn(12))] = blob()
		}
		return m
	}
	// clean drops any path that has another path beneath it, so a map is a
	// valid tree (a name is a file or a directory, not both).
	clean := func(m map[string]TreeEntry) map[string]TreeEntry {
		out := map[string]TreeEntry{}
		for p, ent := range m {
			under := false
			for q := range m {
				if len(q) > len(p) && q[:len(p)+1] == p+"/" {
					under = true
					break
				}
			}
			if !under {
				out[p] = ent
			}
		}
		return out
	}
	mutate := func(m map[string]TreeEntry) map[string]TreeEntry {
		out := map[string]TreeEntry{}
		for p, ent := range m {
			out[p] = ent
		}
		var paths []string
		for p := range out {
			paths = append(paths, p)
		}
		for k := rng.Intn(6); k > 0 && len(paths) > 0; k-- {
			p := paths[rng.Intn(len(paths))]
			switch rng.Intn(6) {
			case 0:
				delete(out, p)
			case 1:
				out[p] = blob()
			case 2: // mode only
				if ent := out[p]; ent.Mode != ModeGitlink { // a gitlink has no blob to become a file with
					ent.Mode = modes[rng.Intn(len(modes))]
					out[p] = ent
				}
			case 3: // a file becomes a directory
				delete(out, p)
				out[p+"/inner"] = blob()
			case 4: // a gitlink appears
				out[p] = TreeEntry{SHA: "1234567890123456789012345678901234567890", Mode: ModeGitlink}
			case 5:
				out[dirs[rng.Intn(len(dirs))]+fmt.Sprintf("new%d", rng.Intn(100))] = blob()
			}
		}
		return clean(out)
	}
	for iter := 0; iter < 300; iter++ {
		a := clean(gen())
		var b map[string]TreeEntry
		switch iter % 5 {
		case 0:
			b = clean(gen()) // unrelated
		case 1:
			b = a // identical
		default:
			b = mutate(a)
		}
		ta, err := e.writeTreeRefs(a)
		if err != nil {
			t.Fatal(err)
		}
		tb, err := e.writeTreeRefs(b)
		if err != nil {
			t.Fatal(err)
		}
		for _, pair := range [][2]string{{ta.String(), tb.String()}, {"", tb.String()}, {ta.String(), ""}} {
			fullA, err := e.readTreeRefs(pair[0])
			if err != nil {
				t.Fatal(err)
			}
			fullB, err := e.readTreeRefs(pair[1])
			if err != nil {
				t.Fatal(err)
			}
			want, err := e.diffTreesMeta(fullA, fullB, "a", "b")
			if err != nil {
				t.Fatal(err)
			}
			da, db, err := e.diffTreeRefs(pair[0], pair[1])
			if err != nil {
				t.Fatal(err)
			}
			got, err := e.diffTreesMeta(da, db, "a", "b")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("iter %d: pruned diff differs from the full one\n got %+v\nwant %+v", iter, got, want)
			}
			// Everything the pruned walk returns must be a true entry of its
			// side, and nothing it omits may differ between the sides.
			for p, ent := range da {
				if fullA[p] != ent {
					t.Fatalf("iter %d: old[%q] = %+v, the tree has %+v", iter, p, ent, fullA[p])
				}
			}
			for p, ent := range db {
				if fullB[p] != ent {
					t.Fatalf("iter %d: new[%q] = %+v, the tree has %+v", iter, p, ent, fullB[p])
				}
			}
			for p, ent := range fullA {
				if _, in := da[p]; !in && fullB[p] != ent {
					t.Fatalf("iter %d: %q differs between the trees but was pruned", iter, p)
				}
			}
			for p, ent := range fullB {
				if _, in := db[p]; !in && fullA[p] != ent {
					t.Fatalf("iter %d: %q differs between the trees but was pruned", iter, p)
				}
			}
		}
		if iter%5 == 1 {
			if da, db, _ := e.diffTreeRefs(ta.String(), tb.String()); len(da)+len(db) != 0 {
				t.Fatalf("identical trees produced entries: %v %v", da, db)
			}
		}
	}
}

// commitChain writes n commits, each on top of the last, and returns their
// hashes oldest first.
func commitChain(t *testing.T, e *Engine, n int) []string {
	t.Helper()
	var out []string
	parent := []string(nil)
	for i := 0; i < n; i++ {
		tree, err := e.writeTreeRefs(map[string]TreeEntry{"f": blobEntry(t, e, fmt.Sprintf("v%d\n", i))})
		if err != nil {
			t.Fatal(err)
		}
		sha, err := e.writeCommit(tree.String(), "", fmt.Sprintf("c%d", i), parent)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, sha)
		parent = []string{sha}
	}
	return out
}

// The commit graph is kept on disk and trusted on the next run, so the file
// has to survive what files suffer: being cut short, being scribbled on,
// being something else entirely. Whatever is wrong with it, the answers must
// be the ones the object store gives, and the file must end up whole again.
func TestCommitGraphFileSurvivesDamage(t *testing.T) {
	dir := t.TempDir()
	e, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	chain := commitChain(t, e, 30)
	tip := chain[len(chain)-1]
	gens := func(e *Engine) []uint32 {
		t.Helper()
		var out []uint32
		for _, sha := range chain {
			g, err := e.generation(plumbing.NewHash(sha))
			if err != nil {
				t.Fatalf("generation: %v", err)
			}
			out = append(out, g)
		}
		return out
	}
	want := gens(e)
	for i, g := range want {
		if g != uint32(i+1) {
			t.Fatalf("generation of commit %d = %d", i, g)
		}
	}
	_ = e.Close()
	path := filepath.Join(dir, commitGraphFile)
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the graph was not persisted: %v", err)
	}

	reopen := func(name string, content []byte, wantLoaded int) {
		t.Helper()
		if content != nil {
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		e, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		g := e.lockGraph()
		loaded := len(g.nodes)
		e.cgMu.Unlock()
		if wantLoaded >= 0 && loaded != wantLoaded {
			t.Fatalf("%s: %d commits loaded from the file, want %d", name, loaded, wantLoaded)
		}
		if got := gens(e); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: generations %v, want %v", name, got, want)
		}
		ahead, behind, err := e.divergence(tip, chain[9])
		if err != nil || ahead != 20 || behind != 0 {
			t.Fatalf("%s: divergence = %d/%d, %v; want 20/0", name, ahead, behind, err)
		}
		_ = e.Close()
		// The file is whole again: a fresh engine loads all of it.
		e2, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		g2 := e2.lockGraph()
		n, rewrite := len(g2.nodes), g2.rewrite
		e2.cgMu.Unlock()
		_ = e2.Close()
		if n != len(chain) || rewrite {
			t.Fatalf("%s: after repair the file holds %d commits (damaged=%v), want %d", name, n, rewrite, len(chain))
		}
	}

	reopen("intact", nil, len(chain))
	reopen("truncated mid-record", good[:len(good)-17], len(chain)-1)
	reopen("truncated to the header", good[:len(commitGraphMagic)], 0)
	reopen("empty", []byte{}, 0)
	reopen("foreign file", []byte("this is not a commit graph at all, not even slightly\n"), 0)
	flipped := append([]byte(nil), good...)
	flipped[len(commitGraphMagic)+5*(cgFixed+20+4)+3] ^= 0x40 // inside the 6th record
	reopen("a flipped bit", flipped, 5)
	doubled := append(append([]byte(nil), good...), good[len(commitGraphMagic):]...)
	reopen("every record twice (two writers)", doubled, len(chain))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	reopen("missing", nil, 0)
}

// New commits made after the graph was saved are added to it on demand —
// including ones that REPLACE history the graph already holds, which is what
// reword, drop, squash and undo leave behind. Nothing has to be invalidated
// for that: the replaced commits keep their (still true) records and the new
// ones get their own.
func TestCommitGraphFollowsRewrittenHistory(t *testing.T) {
	dir := t.TempDir()
	e, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	chain := commitChain(t, e, 10)
	if _, _, err := e.divergence(chain[9], chain[0]); err != nil {
		t.Fatal(err)
	}
	_ = e.Close()

	e, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	// "Reword" commit 5: the same tree on the same parent with a new message,
	// then everything after it replayed on top.
	rewritten := append([]string(nil), chain[:5]...)
	parent := chain[4]
	for i := 5; i < 10; i++ {
		tree, err := e.commitTree(chain[i])
		if err != nil {
			t.Fatal(err)
		}
		sha, err := e.writeCommit(tree, "", fmt.Sprintf("reworded c%d", i), []string{parent})
		if err != nil {
			t.Fatal(err)
		}
		rewritten = append(rewritten, sha)
		parent = sha
	}
	ahead, behind, err := e.divergence(rewritten[9], chain[9])
	if err != nil || ahead != 5 || behind != 5 {
		t.Fatalf("rewritten vs original tip: %d/%d, %v; want 5/5", ahead, behind, err)
	}
	base, err := e.mergeBase(rewritten[9], chain[9])
	if err != nil || base != chain[4] {
		t.Fatalf("merge base = %.8s, %v; want %.8s", base, err, chain[4])
	}
	if anc, _ := e.isAncestor(chain[7], rewritten[9]); anc {
		t.Fatal("a replaced commit is reported as an ancestor of the rewritten tip")
	}
	if anc, _ := e.isAncestor(chain[3], rewritten[9]); !anc {
		t.Fatal("a commit below the rewrite is not an ancestor of the rewritten tip")
	}
}

func TestParseRawCommit(t *testing.T) {
	raw := "tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" +
		"parent 1111111111111111111111111111111111111111\n" +
		"parent 2222222222222222222222222222222222222222\n" +
		"author A U Thor <a@example.com> 1700000000 +0100\n" +
		"committer C O Mitter <c> weird> 1700000123 -0800\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n" +
		" \n" +
		" parent 3333333333333333333333333333333333333333\n" +
		" -----END PGP SIGNATURE-----\n" +
		"\n" +
		"subject\n\nparent 4444444444444444444444444444444444444444\ntree 5555555555555555555555555555555555555555\n"
	c, err := parseRawCommit([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if c.tree.String() != "4b825dc642cb6eb9a060e54bf8d69288fbee4904" {
		t.Errorf("tree = %s", c.tree)
	}
	if len(c.parents) != 2 || c.parents[0].String()[0] != '1' || c.parents[1].String()[0] != '2' {
		t.Errorf("parents = %v (a signature line or the message was read as a header)", c.parents)
	}
	if c.time != 1700000123 {
		t.Errorf("committer time = %d", c.time)
	}
	if _, err := parseRawCommit([]byte("parent 1111111111111111111111111111111111111111\n\nmsg\n")); err == nil {
		t.Error("a commit with no tree was accepted")
	}
	if _, err := parseRawCommit([]byte("tree nothex\n\nmsg\n")); err == nil {
		t.Error("a malformed tree header was accepted")
	}
}

func BenchmarkDivergenceDeepHistory(b *testing.B) {
	t := &testing.T{}
	dir := b.TempDir()
	e, err := Open(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = e.Close() }()
	chain := commitChain(t, e, 3000)
	side, err := e.writeCommit(mustTree(b, e, chain[10]), "", "side", []string{chain[10]})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ahead, behind, err := e.divergence(chain[len(chain)-1], side)
		if err != nil || ahead != 2989 || behind != 1 {
			b.Fatalf("divergence = %d/%d, %v", ahead, behind, err)
		}
	}
}

func mustTree(b *testing.B, e *Engine, commit string) string {
	b.Helper()
	tree, err := e.commitTree(commit)
	if err != nil {
		b.Fatal(err)
	}
	return tree
}
