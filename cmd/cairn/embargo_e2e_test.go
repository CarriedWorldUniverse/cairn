package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// remoteHeadMessage returns the first line of the commit message at the bare
// remote's refs/heads/<branch> tip.
func remoteHeadMessage(t *testing.T, bare, branch string) string {
	t.Helper()
	repo, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	ref, err := repo.Reference(plumbing.ReferenceName("refs/heads/"+branch), true)
	if err != nil {
		t.Fatalf("ref %s: %v", branch, err)
	}
	c, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if i := strings.IndexByte(c.Message, '\n'); i >= 0 {
		return c.Message[:i]
	}
	return c.Message
}

func commitOn(t *testing.T, root, branch, file, body, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, branch, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "--author", "dev", branch, "-m", msg)
}

// TestE2E_EmbargoFreezesPublicPush: embargoing a commit holds it (and everything
// after) out of a public (git) push; disclosing it advances the public tip.
func TestE2E_EmbargoFreezesPublicPush(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	// Three sealed commits c1→c2→c3 on main; capture c2's sha to embargo it.
	commitOn(t, root, "main", "a.txt", "1\n", "c1")
	if err := os.WriteFile(filepath.Join(root, "main", "a.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2 := strings.TrimSpace(captureRun(t, "commit", "--repo", root, "--author", "dev", "main", "-m", "c2-fix"))
	commitOn(t, root, "main", "a.txt", "3\n", "c3")

	mustRun(t, "embargo", "--repo", root, c2)
	bare := emptyBareRepo(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "push", "--repo", root, "origin", "main")

	// Public tip is frozen at c1 — c2/c3 held back.
	if msg := remoteHeadMessage(t, bare, "main"); msg != "c1" {
		t.Fatalf("after embargo, public head = %q, want c1", msg)
	}

	// Disclose c2 → next push advances the public tip to c3.
	mustRun(t, "disclose", "--repo", root, c2)
	mustRun(t, "push", "--repo", root, "origin", "main")
	if msg := remoteHeadMessage(t, bare, "main"); msg != "c3" {
		t.Fatalf("after disclose, public head = %q, want c3", msg)
	}
}

// TestE2E_EmbargoRefusesCairnRemote: gated distribution to a cairn server is the
// server tier (Slice 4b); for now an embargo push to a cairn remote is refused
// rather than leaking the embargoed content via refs/cairn/*.
// TestE2E_EmbargoCairnDualPush: pushing embargoed content to a cairn remote
// delivers a dual projection — the public refs are capped at the embargo boundary
// and carry NO cairn meta (a public clone reconstructs the frozen flat graph),
// while the REAL tips + full meta travel in the private refs/cairn/embargo/*
// namespace (a real cairn server relocates that into its gated store; here we push
// to a plain bare and verify only the client's ref layout).
func TestE2E_EmbargoCairnDualPush(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	commitOn(t, root, "main", "a.txt", "1\n", "c1")
	if err := os.WriteFile(filepath.Join(root, "main", "a.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2 := strings.TrimSpace(captureRun(t, "commit", "--repo", root, "--author", "dev", "main", "-m", "c2-fix"))
	mustRun(t, "embargo", "--repo", root, c2)

	bare := emptyBareRepo(t)
	mustRun(t, "remote", "add", "--repo", root, "--cairn", "origin", bare)
	mustRun(t, "push", "--repo", root, "origin", "main") // no longer refused

	repo, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatal(err)
	}
	if msg := remoteHeadMessage(t, bare, "main"); msg != "c1" {
		t.Fatalf("public head = %q, want c1 (capped)", msg)
	}
	if _, err := repo.Reference("refs/cairn/meta", false); err == nil {
		t.Error("public refs/cairn/meta should be absent during embargo (flat public projection)")
	}
	embHead, err := repo.Reference("refs/cairn/embargo/heads/main", false)
	if err != nil {
		t.Fatalf("refs/cairn/embargo/heads/main missing: %v", err)
	}
	c, _ := repo.CommitObject(embHead.Hash())
	if c == nil || !strings.HasPrefix(c.Message, "c2-fix") {
		t.Fatalf("embargo head should be the real uncapped tip c2-fix; got %v", c)
	}
	if _, err := repo.Reference("refs/cairn/embargo/meta", false); err != nil {
		t.Errorf("refs/cairn/embargo/meta missing: %v", err)
	}
}
