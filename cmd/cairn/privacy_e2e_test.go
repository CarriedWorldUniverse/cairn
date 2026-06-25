package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// secretBytes is a distinctive marker so a leak test can scan every blob in a
// remote for the raw secret content regardless of which path/ref carried it.
var secretBytes = []byte("SUPER_SECRET_VALUE_DO_NOT_LEAK_42\n")

// emptyBareRepo creates an empty bare git repo and returns its path (a push URL).
func emptyBareRepo(t *testing.T) string {
	t.Helper()
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}
	return bare
}

// assertNoSecretInRemote opens a bare remote and fails if ANY blob in its object
// store contains the secret bytes — the strongest leak check (path-independent,
// ref-independent: it scans everything the push transmitted).
func assertNoSecretInRemote(t *testing.T, bare string) {
	t.Helper()
	repo, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	iter, err := repo.BlobObjects()
	if err != nil {
		t.Fatalf("blob objects: %v", err)
	}
	leaked := false
	_ = iter.ForEach(func(b *object.Blob) error {
		r, err := b.Reader()
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(r)
		_ = r.Close()
		if bytes.Contains(data, secretBytes) {
			leaked = true
			t.Errorf("LEAK: blob %s in the remote contains the secret", b.Hash)
		}
		return nil
	})
	if !leaked {
		t.Logf("no secret bytes in any remote blob ✓")
	}
}

// remoteHasSecret reports whether any blob in the bare remote contains the secret
// (used to confirm a non-private push DID ship it, and disclose restores it).
func remoteHasSecret(t *testing.T, bare string) bool {
	t.Helper()
	repo, err := git.PlainOpen(bare)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	iter, err := repo.BlobObjects()
	if err != nil {
		t.Fatalf("blob objects: %v", err)
	}
	found := false
	_ = iter.ForEach(func(b *object.Blob) error {
		r, err := b.Reader()
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(r)
		_ = r.Close()
		if bytes.Contains(data, secretBytes) {
			found = true
		}
		return nil
	})
	return found
}

// seedRepoWithSecret inits a cairn repo at root with a public app file and a
// withheld secrets/prod.env, marks secrets/ private, and seals a commit.
func seedRepoWithSecret(t *testing.T, root, mode string) {
	t.Helper()
	mustRun(t, "init", root)
	main := filepath.Join(root, "main")
	if err := os.WriteFile(filepath.Join(main, "app.go"), []byte("package main // public\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(main, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main, "secrets", "prod.env"), secretBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "--author", "dev", "main", "-m", "app plus secret")
	if mode == "shape-only" {
		mustRun(t, "private", "--repo", root, "--shape-only", "secrets")
	} else {
		mustRun(t, "private", "--repo", root, "secrets")
	}
}

// TestE2E_PrivacyGitRemoteNoLeak: pushing to a plain git remote must transmit no
// secret bytes (omit mode), while the public file still ships.
func TestE2E_PrivacyGitRemoteNoLeak(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	seedRepoWithSecret(t, root, "omit")
	bare := emptyBareRepo(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "push", "--repo", root, "origin")

	assertNoSecretInRemote(t, bare)
	// Sanity: the public content DID ship (so the push wasn't trivially empty).
	repo, _ := git.PlainOpen(bare)
	iter, _ := repo.BlobObjects()
	sawPublic := false
	_ = iter.ForEach(func(b *object.Blob) error {
		r, _ := b.Reader()
		data, _ := io.ReadAll(r)
		_ = r.Close()
		if bytes.Contains(data, []byte("public")) {
			sawPublic = true
		}
		return nil
	})
	if !sawPublic {
		t.Error("public content did not ship — push may be broken")
	}
}

// TestE2E_PrivacyCairnRemoteNoLeak: the harder case — a cairn remote receives
// refs/cairn/* (working snapshots + meta). None may carry the secret.
func TestE2E_PrivacyCairnRemoteNoLeak(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	seedRepoWithSecret(t, root, "omit")
	bare := emptyBareRepo(t)
	mustRun(t, "remote", "add", "--repo", root, "--cairn", "origin", bare)
	mustRun(t, "push", "--repo", root, "origin")
	assertNoSecretInRemote(t, bare)
}

// TestE2E_PrivacyShapeOnlyNoLeak: shape-only keeps the path but never the bytes.
func TestE2E_PrivacyShapeOnlyNoLeak(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	seedRepoWithSecret(t, root, "shape-only")
	bare := emptyBareRepo(t)
	mustRun(t, "remote", "add", "--repo", root, "--cairn", "origin", bare)
	mustRun(t, "push", "--repo", root, "origin")
	assertNoSecretInRemote(t, bare)
}

// TestE2E_DiscloseRestores: after disclose, a re-push DOES ship the real content.
func TestE2E_DiscloseRestores(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	seedRepoWithSecret(t, root, "omit")
	bare := emptyBareRepo(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "push", "--repo", root, "origin")
	if remoteHasSecret(t, bare) {
		t.Fatal("secret leaked on the withheld push")
	}
	mustRun(t, "disclose", "--repo", root, "secrets")
	mustRun(t, "push", "--repo", root, "origin")
	if !remoteHasSecret(t, bare) {
		t.Error("after disclose, the secret should ship on re-push")
	}
}

// TestE2E_PrivacyMultiLineAndTag guards the tag + multi-line redaction vectors:
// the secret lives on main AND a feature line AND under a tag; a cairn-remote push
// must redact every one of those surfaces (refs/heads/main, refs/heads/feature,
// refs/tags/*, refs/cairn/change/*, refs/cairn/meta).
func TestE2E_PrivacyMultiLineAndTag(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	seedRepoWithSecret(t, root, "omit") // main has app.go + secrets/prod.env, secrets withheld
	// A feature line forked off main inherits the secret in its history.
	mustRun(t, "express", "--repo", root, "--from", "main", "feature")
	if err := os.WriteFile(filepath.Join(root, "feature", "feat.go"), []byte("package feat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "--author", "dev", "feature", "-m", "feature work (still carries secret)")
	// A tag on main's tip points straight at a commit whose tree holds the secret.
	mustRun(t, "tag", "--repo", root, "v1", "main")

	bare := emptyBareRepo(t)
	mustRun(t, "remote", "add", "--repo", root, "--cairn", "origin", bare)
	mustRun(t, "push", "--repo", root, "origin")
	assertNoSecretInRemote(t, bare)
}

// TestE2E_NoPrivateFlagsBytePath: with no flags, a push is unaffected (the public
// repo gets the secret because nothing was withheld) — guards the fast path.
func TestE2E_NoPrivateFlagsShipsEverything(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	mustRun(t, "init", root)
	main := filepath.Join(root, "main")
	if err := os.WriteFile(filepath.Join(main, "data.txt"), secretBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", root, "--author", "dev", "main", "-m", "no flags")
	bare := emptyBareRepo(t)
	mustRun(t, "remote", "add", "--repo", root, "origin", bare)
	mustRun(t, "push", "--repo", root, "origin")
	if !remoteHasSecret(t, bare) {
		t.Error("with no privacy flags, all content should ship unchanged")
	}
}
