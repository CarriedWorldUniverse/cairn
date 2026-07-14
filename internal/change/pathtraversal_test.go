package change

import (
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// rawTree stores an arbitrary tree object directly (as a fetched/hostile object
// would arrive), bypassing writeTree's validation — the setup a malicious remote
// would produce.
func (e *Engine) rawTree(t *testing.T, entries []object.TreeEntry) plumbing.Hash {
	t.Helper()
	tree := &object.Tree{Entries: entries}
	obj := e.git.Storer.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		t.Fatalf("encode tree: %v", err)
	}
	h, err := e.git.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store tree: %v", err)
	}
	return h
}

func TestValidTreeEntryName(t *testing.T) {
	// "..." moved from accepted to rejected (#126 follow-up, item D): Windows
	// strips trailing '.'/' ' from a component before resolving it, so "..."
	// reduces to "" there — a traversal component, even though it isn't a
	// literal ".." here.
	bad := []string{
		"", ".", "..", "a/b", "a\\b", "a\x00b",
		"...",         // strips (Windows) to ""
		".. ",         // strips (Windows) to ""
		"con",         // reserved device name
		"NUL",         // reserved device name (case-insensitive)
		"a:b",         // NTFS alternate data stream
		"PRN.txt",     // reserved device name, with extension
		"con.foo.txt", // reserved device name: base matched up to the FIRST '.'
	}
	for _, n := range bad {
		if err := validTreeEntryName(n); err == nil {
			t.Errorf("validTreeEntryName(%q) = nil, want error", n)
		}
	}
	for _, n := range []string{"a", "file.txt", "..a", "a..", ".hidden"} {
		if err := validTreeEntryName(n); err != nil {
			t.Errorf("validTreeEntryName(%q) = %v, want nil", n, err)
		}
	}
}

func TestValidTreePath(t *testing.T) {
	bad := []string{"", "/abs", "../escape", "a/../b", "a/./b", "sub/..", "a//b", "a/\x00/b"}
	for _, p := range bad {
		if err := validTreePath(p); err == nil {
			t.Errorf("validTreePath(%q) = nil, want error", p)
		}
	}
	for _, p := range []string{"a", "a/b/c", "src/mod/f.gd", "..a/b", "dir/.hidden"} {
		if err := validTreePath(p); err != nil {
			t.Errorf("validTreePath(%q) = %v, want nil", p, err)
		}
	}
}

// TestReadRejectsTraversalTree (#126) is the security regression: a hostile
// remote authors a tree whose entry name is "..", and cairn's read walk must
// refuse it rather than yield a "../escape" path that worktree.Materialize would
// write outside the branch folder. Guards the read boundary for every consumer
// (FilesMeta/readTreeRefs and the content-based readTree/Files).
func TestReadRejectsTraversalTree(t *testing.T) {
	e := newTestEngine(t)
	blob, err := e.writeBlob([]byte("pwned\n"))
	if err != nil {
		t.Fatal(err)
	}
	sub := e.rawTree(t, []object.TreeEntry{
		{Name: "pwned", Mode: filemode.Regular, Hash: blob},
	})
	// A parent entry literally named ".." — the traversal vector.
	root := e.rawTree(t, []object.TreeEntry{
		{Name: "..", Mode: filemode.Dir, Hash: sub},
	})
	commit, err := e.writeCommit(root.String(), "c-traverse", "hostile", nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.FilesMeta(commit); err == nil {
		t.Error("FilesMeta accepted a tree with a '..' entry — path traversal reachable")
	} else if !strings.Contains(err.Error(), "traversal") {
		t.Errorf("FilesMeta error should name the traversal, got: %v", err)
	}
	if _, err := e.Files(commit); err == nil {
		t.Error("Files accepted a tree with a '..' entry — path traversal reachable")
	}

	// A file entry named ".." directly at the root is likewise refused.
	root2 := e.rawTree(t, []object.TreeEntry{
		{Name: "..", Mode: filemode.Regular, Hash: blob},
	})
	commit2, err := e.writeCommit(root2.String(), "c-traverse2", "hostile", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.FilesMeta(commit2); err == nil {
		t.Error("FilesMeta accepted a root '..' file entry")
	}
}

// TestReadRejectsDuplicateNameTree (#126, item B) is the single-tree
// duplicate-name-collision regression: a hostile tree with TWO entries
// sharing the name "evil" at the same level — one a symlink, one a
// directory — is invalid, but go-git's decoder accepts it on read anyway.
// Without a read-side guard, collectTreeRefs would yield BOTH "evil" (as a
// symlink pointing outside the branch folder) and "evil/pwned" (walking into
// the directory entry), handing materialize a collision it should never see.
// FilesMeta must refuse the tree outright.
func TestReadRejectsDuplicateNameTree(t *testing.T) {
	e := newTestEngine(t)
	linkBlob, err := e.writeBlob([]byte("../../outside"))
	if err != nil {
		t.Fatal(err)
	}
	pwnedBlob, err := e.writeBlob([]byte("pwned\n"))
	if err != nil {
		t.Fatal(err)
	}
	sub := e.rawTree(t, []object.TreeEntry{
		{Name: "pwned", Mode: filemode.Regular, Hash: pwnedBlob},
	})
	// Two entries, same name "evil", different kinds — the collision.
	root := e.rawTree(t, []object.TreeEntry{
		{Name: "evil", Mode: filemode.Symlink, Hash: linkBlob},
		{Name: "evil", Mode: filemode.Dir, Hash: sub},
	})
	commit, err := e.writeCommit(root.String(), "c-dup", "hostile", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.FilesMeta(commit); err == nil {
		t.Error("FilesMeta accepted a tree with a duplicate entry name — collision reachable")
	} else if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("FilesMeta error should name the duplicate, got: %v", err)
	}
	if _, err := e.Files(commit); err == nil {
		t.Error("Files (readTree) accepted a tree with a duplicate entry name")
	}
}

// NOTE (#126, item B, scope decision): an end-to-end worktree.Materialize-
// level duplicate-name test was intentionally NOT added. Crafting the raw
// hostile tree needs e.rawTree/e.writeCommit, which are unexported symbols
// only reachable from package change's own _test.go files — but package
// change's test files cannot import internal/worktree (which imports
// change), as that is a real import cycle Go's toolchain rejects (confirmed
// via `go vet`: "import cycle not allowed in test"), and worktree's own tests
// have no access to the unexported git storer needed to build the hostile
// tree in the first place. This gap is closed by inspection instead of a
// redundant test: worktree.materialize's FIRST statement is
// `meta, err := eng.FilesMeta(commitSha); if err != nil { return ... }` —
// before even the branch folder (dir) or the blob cache dir is created — so
// TestReadRejectsDuplicateNameTree's FilesMeta rejection above is
// structurally equivalent to a Materialize rejection: an error there returns
// out of materialize before ANY disk write, tracked or not.
