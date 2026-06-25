package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// TestCloneCairnRemoteFidelity is the core round-trip test: push a cairn repo
// (with a real line tree + an open conflict) to a bare repo registered as a
// cairn remote, clone it into a fresh directory, and assert that the clone B
// reconstructs the exact change-graph — line tree (b under a under root), open
// conflict on a, and matching change-ids — rather than the lossy flat
// git projection.
func TestCloneCairnRemoteFidelity(t *testing.T) {
	skipOnWindows(t)

	// ── Repo A: build a real line tree ──────────────────────────────────────
	repoA := filepath.Join(t.TempDir(), "A")
	mustRun(t, "init", repoA)

	// Seed the root (main) with a baseline commit.
	if err := os.WriteFile(filepath.Join(repoA, "main", "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", repoA, "main")

	// Express line a off main, then line b off a.
	mustRun(t, "express", "--repo", repoA, "--from", "main", "a")
	mustRun(t, "express", "--repo", repoA, "--from", "a", "b")

	// Commit distinct content on each line so each has a real sealed change.
	if err := os.WriteFile(filepath.Join(repoA, "a", "a.txt"), []byte("on-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", repoA, "a")

	if err := os.WriteFile(filepath.Join(repoA, "b", "b.txt"), []byte("on-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", repoA, "b")

	// ── Force an OPEN conflict on line a ────────────────────────────────────
	// main advances with a conflicting edit to f.txt …
	if err := os.WriteFile(filepath.Join(repoA, "main", "f.txt"), []byte("main-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", repoA, "main")

	// … then a edits the same file differently — commit a now merge-forwards
	// against main and conflicts.
	if err := os.WriteFile(filepath.Join(repoA, "a", "f.txt"), []byte("a-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"commit", "--repo", repoA, "a"}); err != nil && !errors.Is(err, errConflicts) {
		t.Fatalf("commit a: unexpected error %v (want nil or errConflicts)", err)
	}

	// Capture change-ids from A via `cairn ls` (format: "<branch>  <change-id>\n").
	lsOutA := mustRunOut(t, "ls", "--repo", repoA)
	changeIDsA := parseChangeIDs(lsOutA)

	// ── Push to a cairn-kind bare remote ────────────────────────────────────
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}

	mustRun(t, "remote", "add", "--repo", repoA, "--cairn", "origin", bare)
	mustRun(t, "push", "--repo", repoA, "origin")

	// ── Clone into repo B ────────────────────────────────────────────────────
	repoB := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", bare, repoB)

	// ── Assert: line tree is the real tree (b under a under main) ───────────
	// `cairn tree` output format: "<name> (parent <line-id>) ahead=<n>"
	// Parent is the line ID (UUID), not the name. Parse into a name→parentID map.
	treeOut := mustRunOut(t, "tree", "--repo", repoB)
	nameParent := parseTreeParents(treeOut)

	// main is the root — parent field is empty.
	mainParent, hasMain := nameParent["main"]
	if !hasMain {
		t.Fatalf("tree on B: missing 'main' line; got:\n%s", treeOut)
	}
	if mainParent != "" {
		t.Errorf("tree on B: 'main' should be root (empty parent), got parent=%q", mainParent)
	}

	// a must exist and must NOT share the same parent as b (non-flat structure).
	aParent, hasA := nameParent["a"]
	if !hasA {
		t.Fatalf("tree on B: missing 'a' line; got:\n%s", treeOut)
	}
	bParent, hasB := nameParent["b"]
	if !hasB {
		t.Fatalf("tree on B: missing 'b' line; got:\n%s", treeOut)
	}

	// In the fidelity (non-flat) tree: a's parent is main's line ID, b's parent
	// is a's line ID. The key invariant is b.parent != a.parent — proving nesting.
	if aParent == bParent {
		t.Errorf("tree on B: a and b share the same parent (flat projection); want nested tree.\na.parent=%q b.parent=%q\ntree:\n%s", aParent, bParent, treeOut)
	}
	// Also: a's parent must not be empty (a is not root).
	if aParent == "" {
		t.Errorf("tree on B: 'a' has no parent (should be child of main)")
	}

	// ── Assert: open conflict on line a ─────────────────────────────────────
	// After clone only the default (root) branch is expressed. Express a so
	// status can inspect its working change.
	mustRun(t, "express", "--repo", repoB, "a")
	statusA := mustRunOut(t, "status", "--repo", repoB, "a")
	// The conflict row for f.txt must have round-tripped: status must report it.
	if !strings.Contains(statusA, "conflicts: f.txt") {
		t.Errorf("status a on B: expected 'conflicts: f.txt' (open conflict round-trip); got:\n%s", statusA)
	}

	// ── Assert: change-ids match ─────────────────────────────────────────────
	// Express b so it appears in ls on B (only expressed branches show in ls).
	mustRun(t, "express", "--repo", repoB, "b")
	lsOutB := mustRunOut(t, "ls", "--repo", repoB)
	changeIDsB := parseChangeIDs(lsOutB)
	for branch, idA := range changeIDsA {
		idB, ok := changeIDsB[branch]
		if !ok {
			t.Errorf("branch %q present in A (ls) but missing from B (ls)", branch)
			continue
		}
		if idA != idB {
			t.Errorf("branch %q: change-id mismatch after fidelity round-trip: A=%q B=%q", branch, idA, idB)
		}
	}
}

// TestCloneGitRemoteFlat is the contrast test: the same repo pushed via a plain
// git remote (no --cairn flag) produces the lossy flat projection on clone —
// a and b are both children of root rather than the real nested tree.
func TestCloneGitRemoteFlat(t *testing.T) {
	skipOnWindows(t)

	// ── Repo A: same line tree as the fidelity test ──────────────────────────
	repoA := filepath.Join(t.TempDir(), "A")
	mustRun(t, "init", repoA)

	if err := os.WriteFile(filepath.Join(repoA, "main", "f.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", repoA, "main")

	mustRun(t, "express", "--repo", repoA, "--from", "main", "a")
	mustRun(t, "express", "--repo", repoA, "--from", "a", "b")

	if err := os.WriteFile(filepath.Join(repoA, "a", "a.txt"), []byte("on-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", repoA, "a")

	if err := os.WriteFile(filepath.Join(repoA, "b", "b.txt"), []byte("on-b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", repoA, "b")

	// ── Push via a plain git remote (no --cairn) ─────────────────────────────
	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}

	// Register as a plain git remote (default kind — no --cairn flag).
	mustRun(t, "remote", "add", "--repo", repoA, "origin", bare)
	mustRun(t, "push", "--repo", repoA, "origin")

	// ── Clone into repo B ─────────────────────────────────────────────────────
	repoB := filepath.Join(t.TempDir(), "B")
	mustRun(t, "clone", bare, repoB)

	// ── Assert: flat projection (a and b both children of root) ──────────────
	// `cairn tree` output format: "<name> (parent <line-id>) ahead=<n>"
	// In the flat projection all non-root lines share the same parent (root's ID).
	treeOut := mustRunOut(t, "tree", "--repo", repoB)
	nameParent := parseTreeParents(treeOut)
	t.Logf("tree on B (git remote, flat):\n%s", treeOut)

	aParent, hasA := nameParent["a"]
	bParent, hasB := nameParent["b"]
	if !hasA || !hasB {
		t.Fatalf("flat clone missing a or b line; tree:\n%s", treeOut)
	}
	// The key invariant for flat projection: a and b share the same parent
	// (both are children of the root). If b.parent == a.line-id that would imply
	// fidelity reconstruction — which should NOT happen via a plain git remote.
	if aParent != bParent {
		t.Errorf("git-remote clone: expected flat projection (a.parent == b.parent), got a.parent=%q b.parent=%q\ntree:\n%s", aParent, bParent, treeOut)
	}
}

// TestNormalGitCloneOfCairnRepo verifies that a plain go-git PlainClone of a
// bare repo that received a cairn push still works correctly: the meta ref is
// inert (just another ref), and the standard heads are intact.
func TestNormalGitCloneOfCairnRepo(t *testing.T) {
	skipOnWindows(t)

	// ── Repo A: minimal setup, push via cairn remote ─────────────────────────
	repoA := filepath.Join(t.TempDir(), "A")
	mustRun(t, "init", repoA)

	if err := os.WriteFile(filepath.Join(repoA, "main", "readme.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", repoA, "main")

	bare := t.TempDir()
	if _, err := git.PlainInit(bare, true); err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}

	mustRun(t, "remote", "add", "--repo", repoA, "--cairn", "origin", bare)
	mustRun(t, "push", "--repo", repoA, "origin")

	// Determine the cairn root branch name (the branch cairn init created).
	rootBranch := soleExpressedDir(t, repoA)

	// ── go-git PlainClone must succeed ───────────────────────────────────────
	// Specify ReferenceName so go-git doesn't have to resolve a symbolic HEAD
	// (bare repos initialised via PlainInit default to refs/heads/master which
	// may differ from the pushed cairn branch — the meta ref is inert to git).
	dest := t.TempDir()
	cloned, err := git.PlainClone(dest, false, &git.CloneOptions{
		URL:           bare,
		ReferenceName: plumbing.ReferenceName("refs/heads/" + rootBranch),
	})
	if err != nil {
		t.Fatalf("go-git PlainClone of cairn-pushed bare repo: %v", err)
	}

	// HEAD must resolve to a sane ref.
	head, err := cloned.Head()
	if err != nil {
		t.Fatalf("cloned.Head(): %v", err)
	}
	if head.Hash().IsZero() {
		t.Errorf("cloned HEAD hash is zero")
	}

	// The cloned working tree must contain readme.txt from the pushed commit.
	if _, statErr := os.Stat(filepath.Join(dest, "readme.txt")); statErr != nil {
		t.Errorf("readme.txt missing from go-git clone of cairn-pushed repo: %v", statErr)
	}
}

// parseChangeIDs parses the output of `cairn ls` (format per line:
// "<branch>  <change-id>") and returns a map of branch→change-id.
func parseChangeIDs(lsOut string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(lsOut), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			m[fields[0]] = fields[1]
		}
	}
	return m
}

// parseTreeParents parses the output of `cairn tree` (format per line:
// "<name> (parent <line-id>) ahead=<n>") and returns a map of name→parentID.
// The root line has an empty parentID ("").
func parseTreeParents(treeOut string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(treeOut), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "<name> (parent <parentID>) ahead=<n>"
		// Extract the name (first field) and the value inside "(parent ...)".
		nameEnd := strings.Index(line, " (parent ")
		if nameEnd < 0 {
			continue
		}
		name := line[:nameEnd]
		rest := line[nameEnd+len(" (parent "):]
		parenEnd := strings.Index(rest, ")")
		if parenEnd < 0 {
			continue
		}
		parentID := rest[:parenEnd]
		m[name] = parentID
	}
	return m
}
