package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	cairnv1 "github.com/CarriedWorldUniverse/cwb-proto/gen/go/cwb/cairn/v1"
)

// cmdRemote lists remotes (no args) or adds one (remote add <name> <url>
// [--cairn]). The --cairn flag records the remote's kind as "cairn"; otherwise
// it defaults to "git".
func cmdRemote(args []string) error {
	// "remote add ..." is a sub-form; dispatch before flag parsing so the
	// add-specific flags (--cairn) don't collide with the list form.
	if len(args) > 0 && args[0] == "add" {
		return cmdRemoteAdd(args[1:])
	}
	fs := flag.NewFlagSet("remote", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	rems, err := r.Remotes()
	if err != nil {
		return mapErr(err)
	}
	for _, rem := range rems {
		fmt.Printf("%s  %s  (%s)\n", rem.Name, rem.URL, rem.Kind)
	}
	return nil
}

func cmdRemoteAdd(args []string) error {
	fs := flag.NewFlagSet("remote add", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	cairn := fs.Bool("cairn", false, "register as a cairn remote — enables full-fidelity push (line tree + change-ids + open conflicts); default is plain git projection")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return errors.New("usage: cairn remote add <name> <url> [--cairn]")
	}
	name := fs.Arg(0)
	url := fs.Arg(1)
	kind := "git"
	if *cairn {
		kind = "cairn"
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if err := r.AddRemote(name, url, kind); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: added remote %s  %s  (%s)\n", name, redactURL(url), kind)
	return nil
}

// cmdPush publishes the change-graph's branches + tags to a remote (default
// "origin"). --force overwrites a diverged remote branch. --reconcile (single-
// line push only) pulls + retries just that one line on divergence instead of
// surfacing the guided "diverged" error; it is rejected together with --all
// (which has its own all-lines auto-reconcile) or --force (which never pulls).
// A line with an open conflict is refused on a push to a plain git remote
// (conflict markers would otherwise ship as plain file content) unless
// --force is passed; a --cairn remote is exempt (conflicts-as-data travels
// with it by design).
func cmdPush(args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	force := fs.Bool("force", false, "force-overwrite a diverged remote branch")
	all := fs.Bool("all", false, "push all lines, not just the current one")
	reconcile := fs.Bool("reconcile", false, "single-line push: pull+retry just this line on a diverged remote")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if *reconcile && *all {
		return errors.New("--reconcile applies to a single-line push (not --all)")
	}
	if *reconcile && *force {
		return errors.New("--reconcile and --force are contradictory")
	}
	// push [remote] [branch]: 2 args → only <branch> to <remote>; otherwise the
	// branch defaults to the line you're standing in (like git pushes the current
	// branch), so a push from inside a feature folder publishes only that line and
	// never touches main. --all forces the legacy all-lines push.
	remote := "origin"
	branch := ""
	switch {
	case fs.NArg() >= 2:
		remote, branch = fs.Arg(0), fs.Arg(1)
	case fs.NArg() == 1:
		remote = fs.Arg(0)
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if branch == "" && !*all {
		if b, ok := r.CWDBranch(); ok {
			branch = b // inside a branch folder → push just that line
		}
	}
	if branch != "" {
		if *reconcile {
			// Opt-in single-line reconcile: pulls + retries just this line on
			// divergence (scoped, unlike Push's all-lines auto-reconcile).
			if err := r.PushBranchReconcile(remote, branch); err != nil {
				return mapRemoteErr(err)
			}
			fmt.Printf("pushed %s -> %s\n", branch, remote)
			return nil
		}
		// Single-line push: no auto-pull-retry (a diverged branch surfaces a
		// guided "diverged" error naming --reconcile/--pull/--force).
		if err := r.PushBranch(remote, branch, *force); err != nil {
			return mapRemoteErr(err)
		}
		fmt.Printf("pushed %s -> %s\n", branch, remote)
		return nil
	}
	if *reconcile {
		// No single line resolved: --all was NOT passed (that conflict is
		// already rejected above) and cwd isn't inside a branch folder, so
		// there's nothing for --reconcile to scope to. Distinct from the
		// --reconcile+--all message above: the operator never typed --all
		// here, so telling them "not --all" would be confusing.
		return errors.New("--reconcile needs a single line to push — pass a branch, or run it from inside an expressed branch folder")
	}
	// r.Push auto-reconciles a diverged remote (pull + 3-way merge, then retry
	// once) so "push just works". A successful auto-retry is intentionally silent
	// for v1: detecting whether the retry happened would need engine I/O the CLI
	// layer deliberately avoids. A merge that conflicts surfaces as a non-nil
	// "resolve, then push" error mapped to stderr below.
	if err := r.Push(remote, *force); err != nil {
		return mapRemoteErr(err)
	}
	fmt.Printf("pushed -> %s\n", remote)
	return nil
}

// cmdFetch fetches a remote (default "origin") into tracking refs without
// touching local lines.
func cmdFetch(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	remote := "origin"
	if fs.NArg() > 0 {
		remote = fs.Arg(0)
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if err := r.Fetch(remote); err != nil {
		return mapRemoteErr(err)
	}
	fmt.Printf("fetched <- %s\n", remote)
	return nil
}

// cmdPull fetches a remote (default "origin") and reconciles each local line
// against its remote branch, re-materializing expressed folders. Each line's
// outcome is printed; conflicts are reported but non-fatal (exit 0) so the
// operator can resolve them and push.
func cmdPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	remote := "origin"
	if fs.NArg() > 0 {
		remote = fs.Arg(0)
	}
	// pull re-materializes every expressed folder, so it MUST run the sync
	// prelude first (#182): opening without it meant the folders were rewritten
	// from their line tips with un-sealed edits never snapshotted — reverted,
	// and untracked new files deleted by the materialize sweep. Repo.Pull also
	// syncs defensively; this keeps the CLI's timing and progress consistent
	// with the other slow verbs.
	r, pullStarted, err := openRepoSyncedVerbose(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	defer reportElapsed("pull", pullStarted)
	sum, err := r.Pull(remote)
	if err != nil {
		return mapRemoteErr(err)
	}
	anyConflicts := false
	for _, lr := range sum.Lines {
		if lr.Conflicts > 0 {
			anyConflicts = true
			fmt.Printf("%s: %s (%d conflicts)\n", lr.Line, lr.Status, lr.Conflicts)
		} else {
			fmt.Printf("%s: %s\n", lr.Line, lr.Status)
		}
	}
	if anyConflicts {
		fmt.Fprintln(os.Stderr, "cairn: resolve the conflicts above, then push")
		return errConflicts
	}
	return nil
}

// printPull prints a pull in the one-line list/view format.
func printPull(p *cairnv1.Pull) {
	fmt.Printf("%s\t%s\t%s -> %s\t%s\t%s\n", p.GetId(), p.GetState(), p.GetSource(), p.GetTarget(), p.GetTitle(), p.GetLedgerIssueKey())
}
