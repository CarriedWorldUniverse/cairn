package main

import (
	"context"
	"fmt"
	"os"

	"github.com/CarriedWorldUniverse/cairn/internal/repo"
)

// Embargo recipient ACL ops, invoked by a server operator. These exist as
// subcommands (not gRPC) because cairn's services are protoc-generated from an
// external proto package with no local toolchain to add an RPC. The recipient
// gate is per-repo, all-or-nothing: a granted agent's clone/fetch is served the
// embargo bare (real content); everyone else gets the frozen public bare.
//
//	cairn-server embargo-grant      <repo-id> <agent-id> [granted-by]
//	cairn-server embargo-revoke     <repo-id> <agent-id>
//	cairn-server embargo-recipients <repo-id>

// runEmbargoGrant authorizes agentID to fetch repoID's embargoed content.
func runEmbargoGrant(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cairn-server embargo-grant <repo-id> <agent-id> [granted-by]")
		return 2
	}
	repoID, agentID := args[0], args[1]
	grantedBy := "ops"
	if len(args) >= 3 && args[2] != "" {
		grantedBy = args[2]
	}
	return withCore(func(core *repo.Service) int {
		if err := core.GrantEmbargoRecipient(context.Background(), repoID, agentID, grantedBy); err != nil {
			fmt.Fprintln(os.Stderr, "cairn embargo-grant:", err)
			return 1
		}
		fmt.Printf("granted embargo access to %s on %s\n", agentID, repoID)
		return 0
	})
}

// runEmbargoRevoke removes agentID's grant. A recipient who already cloned keeps
// that copy — revocation only stops FUTURE fetches.
func runEmbargoRevoke(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cairn-server embargo-revoke <repo-id> <agent-id>")
		return 2
	}
	repoID, agentID := args[0], args[1]
	return withCore(func(core *repo.Service) int {
		if err := core.RevokeEmbargoRecipient(context.Background(), repoID, agentID); err != nil {
			fmt.Fprintln(os.Stderr, "cairn embargo-revoke:", err)
			return 1
		}
		fmt.Printf("revoked embargo access from %s on %s\n", agentID, repoID)
		return 0
	})
}

// runEmbargoRecipients lists the agents authorized to fetch repoID's embargo.
func runEmbargoRecipients(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: cairn-server embargo-recipients <repo-id>")
		return 2
	}
	repoID := args[0]
	return withCore(func(core *repo.Service) int {
		ids, err := core.ListEmbargoRecipients(context.Background(), repoID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cairn embargo-recipients:", err)
			return 1
		}
		for _, id := range ids {
			fmt.Println(id)
		}
		return 0
	})
}

// runGC reclaims dangling objects on a repo's bare(s) and reaps a fully-disclosed
// embargo bare. gc is the one object-rewriting op, so it is an explicit operator/
// cron step (off the push hot path). `--now` forces immediate prune for a known
// quiet window; without it, git's default grace expiry protects in-flight pushes.
//
//	cairn-server gc <repo-id> [--now]
func runGC(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: cairn-server gc <repo-id> [--now]")
		return 2
	}
	repoID := args[0]
	pruneNow := false
	for _, a := range args[1:] {
		if a == "--now" {
			pruneNow = true
		}
	}
	return withCore(func(core *repo.Service) int {
		reaped, err := core.GCRepo(context.Background(), repoID, pruneNow)
		if err != nil {
			fmt.Fprintln(os.Stderr, "cairn gc:", err)
			return 1
		}
		if reaped {
			fmt.Printf("gc %s: done; reaped fully-disclosed embargo bare\n", repoID)
		} else {
			fmt.Printf("gc %s: done\n", repoID)
		}
		return 0
	})
}

// withCore opens the repo core from the server's env config, runs fn, and closes
// it — the shared boilerplate for the ops subcommands.
func withCore(fn func(*repo.Service) int) int {
	dbPath := env("CAIRN_DB", "/var/lib/nexus/cairn.db")
	repoRoot := env("CAIRN_REPO_ROOT", "/var/lib/nexus/repos")
	core, err := repo.Open(dbPath, repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cairn: open core:", err)
		return 1
	}
	defer core.Close()
	return fn(core)
}
