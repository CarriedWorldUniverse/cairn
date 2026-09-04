package worktree

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// On a filesystem that cannot represent the executable bit (Windows), the
// working copy used to trust the bit anyway (#161): every 100755 entry was
// rewritten on each materialize, showed as modified on a clean checkout, and
// had its mode flipped to 100644 by the next commit. These tests simulate that
// filesystem on this one by forcing the probe false and stripping the bit, and
// pin git's core.fileMode=false behaviour: a tracked path keeps the mode the
// tree holds. Each has a control with the probe TRUE showing the filesystem
// stays authoritative where it can be.

func withoutExecBit(t *testing.T) {
	t.Helper()
	prev := execBitProbe
	execBitProbe = func(string) bool { return false }
	t.Cleanup(func() { execBitProbe = prev })
}

// commitWithScript seals a tree holding one executable and one regular file and
// returns the engine, the sealed commit and the tracked set/modes for scans.
func commitWithScript(t *testing.T) (*change.Engine, string, map[string]struct{}, map[string]change.EntryMode) {
	t.Helper()
	e, err := change.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	main, err := e.LineByName("main")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := e.CreateChange(main.ID, "a")
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Commit(ch.ID,
		map[string][]byte{"run.sh": []byte("#!/bin/sh\necho hi\n"), "plain.txt": []byte("text\n")},
		map[string]change.EntryMode{"run.sh": change.ModeExecutable}, "add script")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	tracked := map[string]struct{}{"run.sh": {}, "plain.txt": {}}
	return e, res.HeadCommit, tracked, map[string]change.EntryMode{"run.sh": change.ModeExecutable}
}

func TestExecBitProbeIsTrueOnThisFilesystem(t *testing.T) {
	skipOnWindows(t)
	if !execBitSupported(t.TempDir()) {
		t.Fatal("this filesystem should report the exec bit as representable")
	}
}

// A scan on a no-exec-bit filesystem keeps a tracked path's tree mode and
// records an untracked path as regular. With the bit representable, the
// filesystem decides — which is what makes the probe the discriminator.
func TestScanKeepsTreeExecModeWithoutExecBit(t *testing.T) {
	skipOnWindows(t)
	e, _, tracked, modes := commitWithScript(t)
	dir := t.TempDir()
	for _, f := range []string{"run.sh", "plain.txt", "untracked.sh"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x\n"), 0o644); err != nil { // no exec bit on disk, as Windows presents it
			t.Fatal(err)
		}
	}

	withoutExecBit(t)
	entries, _, _, _, err := CachedScan(e, dir, tracked, modes, nil, nil, time.Now().UnixNano())
	if err != nil {
		t.Fatalf("CachedScan: %v", err)
	}
	if got := entries["run.sh"].Mode; got != change.ModeExecutable {
		t.Fatalf("tracked run.sh scanned as %v; the tree's executable mode must be kept where the filesystem cannot show it", got)
	}
	if got := entries["plain.txt"].Mode; got != change.ModeRegular {
		t.Fatalf("plain.txt scanned as %v, want regular", got)
	}
	if got := entries["untracked.sh"].Mode; got != change.ModeRegular {
		t.Fatalf("untracked path scanned as %v; with no tree mode to carry it must be regular", got)
	}

	// Control: with the bit representable, a file that really lacks it IS regular.
	execBitProbe = execBitSupported
	entries, _, _, _, err = CachedScan(e, dir, tracked, modes, nil, nil, time.Now().UnixNano())
	if err != nil {
		t.Fatalf("CachedScan (control): %v", err)
	}
	if got := entries["run.sh"].Mode; got != change.ModeRegular {
		t.Fatalf("control: run.sh without the bit on an exec-capable filesystem scanned as %v, want regular", got)
	}
}

// An up-to-date executable must not be rewritten on every materialize merely
// because the filesystem cannot show its bit.
func TestMaterializeDoesNotRewriteExecFileWithoutExecBit(t *testing.T) {
	skipOnWindows(t)
	e, sha, _, _ := commitWithScript(t)
	dir := filepath.Join(t.TempDir(), "wc")
	cache := filepath.Join(t.TempDir(), "cache")
	if err := Materialize(e, cache, sha, dir); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	p := filepath.Join(dir, "run.sh")
	// Simulate Windows: the bit is gone, and pin a distinctive mtime so a
	// rewrite is unmistakable.
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatal(err)
	}

	withoutExecBit(t)
	if err := Materialize(e, cache, sha, dir); err != nil {
		t.Fatalf("Materialize (no exec bit): %v", err)
	}
	st, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(past) {
		t.Fatalf("run.sh was rewritten (mtime %v, pinned %v) on a filesystem that cannot show the exec bit — every materialize would rewrite every script", st.ModTime(), past)
	}

	// Control: where the bit is representable, a missing bit IS a difference
	// and materialize restores it.
	execBitProbe = execBitSupported
	if err := Materialize(e, cache, sha, dir); err != nil {
		t.Fatalf("Materialize (control): %v", err)
	}
	st, err = os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&0o111 == 0 {
		t.Fatal("control: on an exec-capable filesystem materialize should have restored the executable bit")
	}
}

// The data-integrity case: syncing a clean checkout on a no-exec-bit
// filesystem must not flip a script's mode in the working change, and must
// not move the head at all — nothing changed.
func TestSyncKeepsExecModeWithoutExecBit(t *testing.T) {
	r, def := seedBranch(t)
	dir := filepath.Join(r.Root(), r.st.Expressed[def].Path)
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(def, "add script"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking: %v", err)
	}
	before := headOf(t, r, def)
	if m := modeOf(t, r, before, "run.sh"); m != change.ModeExecutable {
		t.Fatalf("precondition: run.sh committed as %v, want executable", m)
	}

	// Simulate Windows presenting the same, untouched file without the bit.
	if err := os.Chmod(script, 0o644); err != nil {
		t.Fatal(err)
	}
	withoutExecBit(t)
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking (no exec bit): %v", err)
	}
	after := headOf(t, r, def)
	if m := modeOf(t, r, after, "run.sh"); m != change.ModeExecutable {
		t.Fatalf("run.sh mode became %v after a sync on a no-exec-bit filesystem — the next commit would rewrite 100755 to 100644 in shared history", m)
	}
	if after != before {
		t.Fatalf("head moved (%s -> %s) although nothing changed; an untouched checkout must not register as modified", before[:8], after[:8])
	}

	// Control: where the bit is representable, the operator really did chmod
	// the file, and the change IS recorded.
	execBitProbe = execBitSupported
	if err := r.SyncWorking(); err != nil {
		t.Fatalf("SyncWorking (control): %v", err)
	}
	if m := modeOf(t, r, headOf(t, r, def), "run.sh"); m != change.ModeRegular {
		t.Fatalf("control: a real chmod on an exec-capable filesystem should be recorded, got %v", m)
	}
}

// The #154 seed must equal a cold scan in this regime too.
func TestSeedMatchesColdScanWithoutExecBit(t *testing.T) {
	skipOnWindows(t)
	e, sha, tracked, modes := commitWithScript(t)
	dir := filepath.Join(t.TempDir(), "wc")
	cache := filepath.Join(t.TempDir(), "cache")
	withoutExecBit(t)
	seeded, err := MaterializeSeeded(e, cache, sha, dir, nil)
	if err != nil {
		t.Fatalf("MaterializeSeeded: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o644); err != nil { // as Windows would present it
		t.Fatal(err)
	}
	_, scanned, _, _, err := CachedScan(e, dir, tracked, modes, nil, nil, time.Now().UnixNano())
	if err != nil {
		t.Fatalf("CachedScan: %v", err)
	}
	if !reflect.DeepEqual(seeded, scanned) {
		t.Fatalf("seed and cold scan disagree without the exec bit\n seeded: %#v\nscanned: %#v", seeded, scanned)
	}
}

func modeOf(t *testing.T, r *Repo, commit, path string) change.EntryMode {
	t.Helper()
	meta, err := r.eng.FilesMeta(commit)
	if err != nil {
		t.Fatalf("FilesMeta %s: %v", commit[:8], err)
	}
	return meta[path].Mode
}
