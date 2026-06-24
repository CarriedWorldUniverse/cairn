package change

import (
	"testing"
)

// TestCommitCarriesModes verifies that Commit threads a sparse mode map through
// to the tree and FileModes reads back only the non-regular entries.
func TestCommitCarriesModes(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "t")

	files := map[string][]byte{
		"s":     []byte("#!/bin/sh\n"),
		"l":     []byte("target.txt"),
		"plain": []byte("hello\n"),
	}
	modes := map[string]EntryMode{
		"s": ModeExecutable,
		"l": ModeSymlink,
	}
	r, err := e.Commit(ch.ID, files, modes, "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got, err := e.FileModes(r.HeadCommit)
	if err != nil {
		t.Fatalf("FileModes: %v", err)
	}
	if got["s"] != ModeExecutable {
		t.Errorf("s mode = %v, want ModeExecutable", got["s"])
	}
	if got["l"] != ModeSymlink {
		t.Errorf("l mode = %v, want ModeSymlink", got["l"])
	}
	if _, ok := got["plain"]; ok {
		t.Errorf("plain should be omitted (regular), got %v", got["plain"])
	}

	// Content round-trips for all three (a symlink's content = its target).
	contents, err := e.Files(r.HeadCommit)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if string(contents["l"]) != "target.txt" {
		t.Errorf("symlink content = %q, want target.txt", contents["l"])
	}
	if string(contents["s"]) != "#!/bin/sh\n" {
		t.Errorf("exec content = %q", contents["s"])
	}
}

// TestCommitNilModesEmptyFileModes verifies the existing-caller path: a nil
// modes map yields an empty (no non-regular) FileModes result.
func TestCommitNilModesEmptyFileModes(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	main, _ := e.LineByName("main")
	ch, _ := e.CreateChange(main.ID, "t")

	r, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("a\n"), "d/b.txt": []byte("b\n")}, nil, "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	got, err := e.FileModes(r.HeadCommit)
	if err != nil {
		t.Fatalf("FileModes: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("FileModes = %v, want empty", got)
	}
}
