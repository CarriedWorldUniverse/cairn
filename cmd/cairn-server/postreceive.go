package main

import (
	"context"
	"fmt"
	"os"

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

	n, err := core.RelocateEmbargoRefs(context.Background(), repoID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cairn post-receive: relocate embargo:", err)
		return 1
	}
	if n > 0 {
		fmt.Fprintf(os.Stderr, "cairn: segregated %d embargo ref(s) into the private store\n", n)
	}
	return 0
}
