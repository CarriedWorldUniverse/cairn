package change

import (
	"os/exec"
	"testing"
)

// ValidRefName must accept exactly what `git check-ref-format --branch` does
// for the cases that matter, so a name cairn takes is one git takes.
func TestValidRefNameMatchesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	for _, n := range []string{
		"main", "feat/x", "a-b_c.d", "v1.2.3", "feat/a/b", "x@y",
		"", "-x", "a b", "a..b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b",
		"/a", "a/", "a//b", "a.", "a.lock", "a/.b", "a/b.lock", "@", "a@{b", ".hidden", "a\x7f", "a\tb",
	} {
		want := exec.Command("git", "check-ref-format", "--branch", n).Run() == nil
		if n == "" || n == "@" {
			want = false
		}
		if got := ValidRefName(n) == nil; got != want {
			t.Errorf("ValidRefName(%q) = %v, git says %v", n, got, want)
		}
	}
}

func TestRefOpsUndo(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	tip := seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})

	// annotated tag, then force-move, each reversible
	if err := e.CreateTag("v1", tip, "first release", true, false, "tester"); err != nil {
		t.Fatalf("CreateTag: %v", err)
	}
	if err := e.CreateTag("v1", tip, "x", true, false, "tester"); err == nil {
		t.Fatal("re-tag without force succeeded")
	}
	tags, _ := e.ListTagInfos()
	if len(tags) != 1 || !tags[0].Annotated || tags[0].Message != "first release\n" || tags[0].Commit != tip {
		t.Fatalf("tags = %+v", tags)
	}
	if err := e.CreateTag("v1", tip, "", false, true, "tester"); err != nil {
		t.Fatalf("force move: %v", err)
	}
	if tags, _ = e.ListTagInfos(); tags[0].Annotated {
		t.Fatal("force-moved tag is still annotated")
	}
	if err := e.Undo(); err != nil {
		t.Fatal(err)
	}
	if tags, _ = e.ListTagInfos(); !tags[0].Annotated {
		t.Fatal("undo of the move did not restore the annotated tag")
	}
	if _, err := e.RemoveTag("v1"); err != nil {
		t.Fatal(err)
	}
	if err := e.Undo(); err != nil {
		t.Fatal(err)
	}
	if tags, _ = e.ListTagInfos(); len(tags) != 1 || !tags[0].Annotated {
		t.Fatalf("undo of the delete: %+v", tags)
	}
	// A fresh creation undoes to nothing, ref included.
	e2 := newTestEngine(t)
	m2, _ := e2.LineByName("main")
	tip2 := seedLineTip(t, e2, m2.ID, map[string][]byte{"a.txt": []byte("a\n")})
	if err := e2.CreateTag("v1", tip2, "m", true, false, "tester"); err != nil {
		t.Fatal(err)
	}
	if err := e2.Undo(); err != nil {
		t.Fatal(err)
	}
	e = e2
	if tags, _ = e.ListTagInfos(); len(tags) != 0 {
		t.Fatalf("tags after undoing creation: %+v", tags)
	}
	if _, err := e.git.Reference("refs/tags/v1", false); err == nil {
		t.Fatal("tag ref survived undo")
	}

	// line rename keeps children attached; undo reverses it
	e = newTestEngine(t)
	main, _ = e.LineByName("main")
	seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	child, err := e.CreateLine("child", main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.RenameLine("main", "trunk", false); err != nil {
		t.Fatalf("RenameLine: %v", err)
	}
	c2, _ := e.LineByID(child.ID)
	root, _ := e.RootLine()
	if root.Name != "trunk" || c2.ParentLine != root.ID {
		t.Fatalf("root %q, child parent %q (want %q)", root.Name, c2.ParentLine, root.ID)
	}
	if err := e.RenameLine("child", "trunk", false); err == nil {
		t.Fatal("renamed onto an existing line without force")
	}
	if err := e.Undo(); err != nil {
		t.Fatal(err)
	}
	if root, _ = e.RootLine(); root.Name != "main" {
		t.Fatalf("undo left root named %q", root.Name)
	}

	// -M replaces an existing line, and undo brings it back
	other, err := e.CreateLine("other", main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.RenameLine("child", "other", true); err != nil {
		t.Fatalf("force rename: %v", err)
	}
	if l, _ := e.LineByName("other"); l.ID != child.ID {
		t.Fatal("forced rename did not take the name")
	}
	if err := e.Undo(); err != nil {
		t.Fatal(err)
	}
	if l, err := e.LineByName("other"); err != nil || l.ID != other.ID || l.Status != "open" {
		t.Fatalf("undo did not restore the replaced line: %+v %v", l, err)
	}
	if l, err := e.LineByName("child"); err != nil || l.ID != child.ID {
		t.Fatalf("undo did not restore child: %+v %v", l, err)
	}
}

// An undo carries the inverse of what it did, so undoing it redoes tag,
// rename, copy and remote edits.
func TestRefOpsUndoOfUndo(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	tip := seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	if err := e.CreateTag("v1", tip, "m", true, false, "t"); err != nil {
		t.Fatal(err)
	}
	e.Undo()
	e.Undo()
	if tags, _ := e.ListTagInfos(); len(tags) != 1 || !tags[0].Annotated {
		t.Fatalf("tag create -> undo -> undo: %+v", tags)
	}
	if _, err := e.RemoveTag("v1"); err != nil {
		t.Fatal(err)
	}
	e.Undo()
	e.Undo()
	if tags, _ := e.ListTagInfos(); len(tags) != 0 {
		t.Fatalf("tag delete -> undo -> undo: %+v", tags)
	}
	if err := e.RenameLine("main", "trunk", false); err != nil {
		t.Fatal(err)
	}
	e.Undo()
	e.Undo()
	if r, _ := e.RootLine(); r.Name != "trunk" {
		t.Fatalf("rename redo: root %q", r.Name)
	}
	if err := e.CopyLine("trunk", "dup"); err != nil {
		t.Fatal(err)
	}
	e.Undo()
	if _, err := e.LineByName("dup"); err == nil {
		t.Fatal("copy undo left the line")
	}
	e.Undo()
	if _, err := e.LineByName("dup"); err != nil {
		t.Fatalf("copy redo: %v", err)
	}
}
