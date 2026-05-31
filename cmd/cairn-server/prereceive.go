package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/CarriedWorldUniverse/cairn/internal/protect"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
)

// runPreReceive is invoked as `cairn-server pre-receive <repo-id>` by the
// server-side hook. It reads update lines from stdin, loads the repo's
// protection rule, computes fast-forward-ness via git, and exits non-zero on
// the first rejected update (git then refuses the whole push).
func runPreReceive(repoID string) int {
	dbPath := env("CAIRN_DB", "/var/lib/nexus/cairn.db")
	repoRoot := env("CAIRN_REPO_ROOT", "/var/lib/nexus/repos")
	core, err := repo.Open(dbPath, repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cairn pre-receive: open core:", err)
		return 1
	}
	defer core.Close()

	ctx := context.Background()
	r, err := core.GetRepoByID(ctx, repoID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cairn pre-receive: repo:", err)
		return 1
	}
	var rule protect.Rule
	if err := json.Unmarshal([]byte(r.Protection), &rule); err != nil || rule.DefaultBranch == "" {
		rule.DefaultBranch = r.DefaultBranch // fall back to the catalogue column
	}

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		u, err := protect.ParseUpdateLine(sc.Text())
		if err != nil {
			fmt.Fprintln(os.Stderr, "cairn pre-receive:", err)
			return 1
		}
		u.OldIsAncestorOfNew = isAncestor(r.StoragePath, u.Old, u.New)
		if err := protect.Allow(rule, u); err != nil {
			fmt.Fprintln(os.Stderr, "cairn:", err.Error())
			return 1
		}
	}
	return 0
}

// isAncestor reports whether old is an ancestor of new in the bare repo (i.e.
// the update is a fast-forward). A zero/absent old is not an ancestor question;
// protect.Allow handles the create/delete cases before this matters.
func isAncestor(gitDir, old, new string) bool {
	const zero = "0000000000000000000000000000000000000000"
	if old == zero || new == zero {
		return false
	}
	cmd := exec.Command("git", "--git-dir", gitDir, "merge-base", "--is-ancestor", old, new)
	return cmd.Run() == nil // exit 0 => old IS an ancestor of new
}
