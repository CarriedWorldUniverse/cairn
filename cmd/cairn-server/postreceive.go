package main

import (
	"context"
	"fmt"
	"os"

	"github.com/CarriedWorldUniverse/cairn/internal/replica"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
)

// runPostReceive is invoked as `cairn-server post-receive <repo-id>` by the
// server-side hook, AFTER the refs are updated (so the pushed objects are in the
// bare, not a quarantine). It relocates any refs/cairn/embargo/* the push
// delivered out of the public bare into the per-repo embargo bare, so the public
// projection that git-upload-pack serves never advertises embargoed content. A
// push without embargo refs is a no-op. Post-receive cannot reject (the push has
// already landed); a failure is logged.
func runPostReceive(repoID string) int {
	dbPath := env("CAIRN_DB", "/var/lib/nexus/cairn.db")
	repoRoot := env("CAIRN_REPO_ROOT", "/var/lib/nexus/repos")
	core, err := repo.Open(dbPath, repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cairn post-receive: open core:", err)
		return 1
	}
	defer core.Close()

	ctx := context.Background()
	n, err := core.RelocateEmbargoRefs(ctx, repoID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cairn post-receive: relocate embargo:", err)
		return 1
	}
	if n > 0 {
		fmt.Fprintf(os.Stderr, "cairn: segregated %d embargo ref(s) into the private store\n", n)
	}

	// Reconcile disclosures: a branch whose embargo was lifted has re-entered the
	// public projection on this push, so retire its gated ref (renaming it to a
	// normal head in the embargo bare) and let BareForServe fall back to public.
	// Runs AFTER relocation so it never sees partially-relocated state. Idempotent;
	// a failure self-heals on the next push (post-receive cannot reject).
	d, err := core.PruneDisclosedEmbargo(ctx, repoID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cairn post-receive: prune disclosed embargo:", err)
		return 0 // the refs already landed; don't fail the push — retry next time
	}
	if d > 0 {
		fmt.Fprintf(os.Stderr, "cairn: retired %d disclosed embargo branch(es)\n", d)
	}

	// Replication: mark the repo dirty in the spool for the server's Runner to
	// pick up. Off the push path entirely — a marking failure must not fail
	// the hook (the push already landed); log and keep the embargo exit code.
	if spoolDir := os.Getenv("CAIRN_REPLICA_SPOOL"); spoolDir != "" {
		repoPath, err := core.StoragePathForID(ctx, repoID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cairn post-receive: replica: resolve storage path:", err)
			return 0
		}
		if err := (replica.Spool{Dir: spoolDir}).Mark(repoID, repoPath); err != nil {
			fmt.Fprintln(os.Stderr, "cairn post-receive: replica: mark:", err)
		}
	}

	return 0
}
