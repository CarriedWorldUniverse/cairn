//go:build linux

package worktree

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

func freeBytes(t *testing.T, path string) int64 {
	t.Helper()
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		t.Fatalf("statfs: %v", err)
	}
	return int64(s.Bavail) * int64(s.Bsize)
}

func TestMaterializeSharesBlocksWhenReflinkSupported(t *testing.T) {
	base := t.TempDir()
	cacheDir := filepath.Join(base, "cache")
	if !reflinkSupported(base) {
		t.Skip("filesystem does not support reflinks")
	}
	eng, err := change.Open(filepath.Join(base, "eng"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")
	const size = 8 << 20 // 8 MiB
	big := make([]byte, size)
	for i := range big {
		big[i] = byte(i*7 + 1)
	}
	r, err := eng.Commit(ch.ID, map[string][]byte{"big.bin": big}, nil, "")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	a := filepath.Join(base, "A")
	b := filepath.Join(base, "B")
	if err := Materialize(eng, cacheDir, r.HeadCommit, a); err != nil {
		t.Fatal(err)
	} // 1st: writes cache blob + reflink
	free0 := freeBytes(t, base)
	if err := Materialize(eng, cacheDir, r.HeadCommit, b); err != nil {
		t.Fatal(err)
	} // 2nd: reflink only
	free1 := freeBytes(t, base)

	used := free0 - free1 // bytes consumed by the 2nd materialize
	if used > size/2 {
		t.Fatalf("2nd materialize consumed %d bytes for an %d-byte file; reflink not sharing", used, size)
	}
}
