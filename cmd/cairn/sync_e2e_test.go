package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestE2E_CollaborationLoop(t *testing.T) {
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
	mustRun(t, "pull", "--repo", B)
	if _, err := os.Stat(filepath.Join(B, def, "fromA.txt")); err != nil {
		t.Fatalf("pull didn't bring A's work into B: %v", err)
	}
	if _, err := os.Stat(filepath.Join(B, def, "fromB.txt")); err != nil {
		t.Fatalf("B's own work lost: %v", err)
	}
	mustRun(t, "push", "--repo", B)
}
