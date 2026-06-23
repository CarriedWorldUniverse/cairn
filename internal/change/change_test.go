package change

import (
	"errors"
	"strings"
	"testing"
)

func TestCommitAdvancesHeadStableChangeID(t *testing.T) {
	e := newTestEngine(t)
	root, _ := e.LineByName("main")
	ch, err := e.CreateChange(root.ID, "agent-a")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if !strings.HasPrefix(ch.ID, "z") {
		t.Fatalf("change_id %q, want reverse-hex (z-prefixed)", ch.ID)
	}
	r1, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("one\n")}, "")
	if err != nil {
		t.Fatalf("Commit 1: %v", err)
	}
	r2, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("two\n")}, "")
	if err != nil {
		t.Fatalf("Commit 2: %v", err)
	}
	if r1.HeadCommit == r2.HeadCommit {
		t.Fatal("head did not advance between commits")
	}
	got, _ := e.GetChange(ch.ID)
	if got.ID != ch.ID {
		t.Fatalf("change_id changed: %s != %s", got.ID, ch.ID)
	}
	if got.HeadCommit != r2.HeadCommit {
		t.Fatalf("stored head %s != last commit %s", got.HeadCommit, r2.HeadCommit)
	}
}

func TestGetChangeNotFound(t *testing.T) {
	e := newTestEngine(t)
	if _, err := e.GetChange("znotarealchangeid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetChange unknown id: got %v, want ErrNotFound", err)
	}
}

func TestNewChangeIDUnique(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id := newChangeID()
		if seen[id] {
			t.Fatalf("duplicate change_id after %d calls: %s", i, id)
		}
		seen[id] = true
		if !strings.HasPrefix(id, "z") {
			t.Fatalf("id %q missing z prefix", id)
		}
	}
}
