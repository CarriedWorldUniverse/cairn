package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2E_PushAutoPullRetry(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t)
	A := filepath.Join(t.TempDir(), "A")
	B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, A)
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, A)
	if err := os.WriteFile(filepath.Join(A, def, "fromA.txt"), []byte("A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", A, def)
	mustRun(t, "push", "--repo", A)
	if err := os.WriteFile(filepath.Join(B, def, "fromB.txt"), []byte("B\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", B, def)
	mustRun(t, "push", "--repo", B) // no --force: must auto-pull+retry and SUCCEED
	if _, err := os.Stat(filepath.Join(B, def, "fromA.txt")); err != nil {
		t.Fatalf("auto-pull didn't bring A's work: %v", err)
	}
	C := filepath.Join(t.TempDir(), "C")
	mustRun(t, "clone", origin, C)
	if _, err := os.Stat(filepath.Join(C, def, "fromB.txt")); err != nil {
		t.Fatalf("B's push didn't land on remote: %v", err)
	}
}

func TestE2E_PushAutoPullConflictStops(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t) // seeds readme.txt
	A := filepath.Join(t.TempDir(), "A")
	B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, A)
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, A)

	// A edits readme.txt and publishes.
	if err := os.WriteFile(filepath.Join(A, def, "readme.txt"), []byte("A-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", A, def)
	mustRun(t, "push", "--repo", A)

	// B edits the SAME readme.txt region and commits — pushing now diverges and
	// the auto-pull merge conflicts, so push must stop and ask to resolve.
	if err := os.WriteFile(filepath.Join(B, def, "readme.txt"), []byte("B-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", B, def)
	err := run([]string{"push", "--repo", B})
	if err == nil {
		t.Fatalf("push over a conflicting divergence should error, got nil")
	}
	if !strings.Contains(err.Error(), "resolve, then push") {
		t.Fatalf("conflict-stop error %q should say 'resolve, then push'", err.Error())
	}

	// readme.txt now has conflict markers on disk; write the resolution, resolve,
	// then push must succeed (the merge is now a remote descendant).
	if err := os.WriteFile(filepath.Join(B, def, "readme.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "resolve", "--repo", B, def, "readme.txt") // resolve <branch> <path>
	mustRun(t, "push", "--repo", B)

	C := filepath.Join(t.TempDir(), "C")
	mustRun(t, "clone", origin, C)
	got, err := os.ReadFile(filepath.Join(C, def, "readme.txt"))
	if err != nil {
		t.Fatalf("read C readme.txt: %v", err)
	}
	if string(got) != "resolved\n" {
		t.Fatalf("remote readme.txt = %q, want resolved", got)
	}
}

func TestE2E_PushForceBypassesRetry(t *testing.T) {
	skipOnWindows(t)
	origin := makeSeededBareRepo(t)
	A := filepath.Join(t.TempDir(), "A")
	B := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", origin, A)
	mustRun(t, "clone", origin, B)
	def := soleExpressedDir(t, A)
	// A and B edit the SAME file differently
	os.WriteFile(filepath.Join(A, def, "readme.txt"), []byte("A-version\n"), 0o644)
	mustRun(t, "commit", "--repo", A, def)
	mustRun(t, "push", "--repo", A)
	os.WriteFile(filepath.Join(B, def, "readme.txt"), []byte("B-version\n"), 0o644)
	mustRun(t, "commit", "--repo", B, def)
	// force push goes straight through, no pull/conflict
	mustRun(t, "push", "--repo", B, "--force")
	// remote now has B's version (force won)
	C := filepath.Join(t.TempDir(), "C")
	mustRun(t, "clone", origin, C)
	got, _ := os.ReadFile(filepath.Join(C, def, "readme.txt"))
	if string(got) != "B-version\n" {
		t.Fatalf("force push didn't win: %q", got)
	}
}
