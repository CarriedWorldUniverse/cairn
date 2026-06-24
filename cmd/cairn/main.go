// Command cairn is the working-copy CLI: a thin dispatcher over the
// internal/worktree Repo. Each subcommand opens (or bootstraps) a repo and
// drives one Repo method — expressing branches as folders on disk, committing
// their contents, folding/abandoning lines, and inspecting the line tree.
//
// Usage:
//
//	cairn <subcommand> [flags] [args]
//
// Subcommands operating on an existing repo accept --repo (default ".") and
// --author (default $CAIRN_AUTHOR, else $USER, else "cairn").
package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/release"
	"github.com/CarriedWorldUniverse/cairn/internal/version"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// Publisher/probe seams, overridable in tests.
var newPublisher = func() release.Publisher { return release.ExecPublisher{} }
var newProbe = func() release.RegistryProbe { return release.ExecProbe{} }

// errConflicts is returned by cmdCommit and cmdPull when conflicts were
// recorded. main() maps this to os.Exit(2) so that `commit && push` is safe
// in scripts (exit 2 ≠ success, but distinct from a hard error at exit 1).
// The stderr notice is printed by the cmd function; main must NOT print it again.
var errConflicts = errors.New("completed with conflicts")

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errConflicts) {
			os.Exit(2)
		}
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "cairn:", err)
		os.Exit(1)
	}
}

const usage = `cairn — cairn working-copy CLI

usage: cairn <subcommand> [flags] [args]

subcommands:
  init [dir]                bootstrap a repo (expresses main)
  clone <url> [dir]         import a remote repo and express its default branch
  express <branch>          materialize a branch folder (--from <parent>)
  unexpress <branch>        remove a branch folder (--force to discard un-sealed work)
  commit <branch> [-m msg]  seal the working change (stamps msg, starts a fresh change)
  fold <branch>             fold a branch into its parent (--force to discard un-sealed work)
  abandon <branch>          discard a branch's line (--force to discard un-sealed work)
  status [branch]           report a branch's state — the working change vs its parent (default: root)
  diff [branch] | diff <a> <b>  show the working change vs its parent, or commit-vs-commit
  tree                          print the line tree
  ls                            list expressed branches
  resolve <branch> <path>       resolve a conflict on a branch
  remote                        list configured remotes
  remote add <name> <url>       register a remote (--cairn for a cairn remote)
  push [remote]                 publish branches + tags (default origin, --force)
  fetch [remote]                fetch a remote into tracking refs (default origin)
  pull [remote]                 fetch + reconcile each line (default origin)
  blame <path> [branch]         show per-line author/date/commit
  log [branch] [-n N]           show commit history
  show <commit>                 show a commit's metadata + diff
  undo                          revert the last operation
  oplog                         show the operation history
  config <key> [value]          get (one arg) or set (two args) a config value
  tag <name> [branch]           tag the tip of a branch (default: root branch)
  version [--target eco] [--release]  print the derived version (stdout only, CI-safe)
  version bump <level>          record explicit bump intent (major|minor|patch)
  release --target eco          cut a clean release: tag + stamp + publish (--dry-run)
  stash [-m msg]            shelve the working change; reset the folder to the sealed state
  stash pop                 restore the most recent stash
  stash list                list the stash stack
  stash drop [id]           discard a stash (default: most recent)

config keys: user.name, user.email, autosync
common flags (repo subcommands): --repo <dir> (default .), --author <name>`

// run dispatches a subcommand. args is os.Args[1:].
func run(args []string) error {
	if len(args) == 0 {
		fmt.Println(usage)
		return errors.New("no subcommand")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	case "init":
		return cmdInit(rest)
	case "clone":
		return cmdClone(rest)
	case "express":
		return cmdExpress(rest)
	case "unexpress":
		return cmdUnexpress(rest)
	case "commit":
		return cmdCommit(rest)
	case "fold":
		return cmdFold(rest)
	case "abandon":
		return cmdAbandon(rest)
	case "status":
		return cmdStatus(rest)
	case "diff":
		return cmdDiff(rest)
	case "blame":
		return cmdBlame(rest)
	case "log":
		return cmdLog(rest)
	case "show":
		return cmdShow(rest)
	case "undo":
		return cmdUndo(rest)
	case "oplog":
		return cmdOplog(rest)
	case "tree":
		return cmdTree(rest)
	case "ls":
		return cmdLs(rest)
	case "resolve":
		return cmdResolve(rest)
	case "remote":
		return cmdRemote(rest)
	case "push":
		return cmdPush(rest)
	case "fetch":
		return cmdFetch(rest)
	case "pull":
		return cmdPull(rest)
	case "config":
		return cmdConfig(rest)
	case "tag":
		return cmdTag(rest)
	case "version":
		return cmdVersion(rest)
	case "release":
		return cmdRelease(rest)
	case "stash":
		return cmdStash(rest)
	default:
		fmt.Println(usage)
		return fmt.Errorf("unknown subcommand %q", sub)
	}
}

// defaultAuthor resolves the commit author from the environment.
func defaultAuthor() string {
	if a := os.Getenv("CAIRN_AUTHOR"); a != "" {
		return a
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "cairn"
}

// openRepo opens a Repo from already-parsed flag values. It refuses to open
// (and thus silently bootstrap) a directory that has no .cairn sub-directory;
// the caller should run `cairn init` first.
func openRepo(repo, author string) (*worktree.Repo, error) {
	if fi, err := os.Stat(filepath.Join(repo, ".cairn")); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("not a cairn repo (run 'cairn init' here first)")
	}
	return worktree.Open(repo, author)
}

// openRepoSynced opens a repo and immediately snapshots every expressed folder
// into its open working change (SyncWorking), so working-copy-aware commands see
// live on-disk edits. A sync failure closes the repo and surfaces a clear error.
// Use this for read/inspect commands and pre-op safety checks; do NOT use it for
// history operations (undo/oplog) — snapshotting first would record an op that
// undo then targets.
func openRepoSynced(repo, author string) (*worktree.Repo, error) {
	r, err := openRepo(repo, author)
	if err != nil {
		return nil, err
	}
	if err := r.SyncWorking(); err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("snapshotting working copy: %w", err)
	}
	return r, nil
}

// repoFlags registers --repo and --author on fs, returning the bound vars.
func repoFlags(fs *flag.FlagSet) (repo, author *string) {
	repo = fs.String("repo", ".", "repo root directory")
	author = fs.String("author", defaultAuthor(), "commit author")
	return repo, author
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	author := fs.String("author", defaultAuthor(), "commit author")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	// Re-init guard: if .cairn already exists, silently succeed (no-op, exit 0).
	if fi, err := os.Stat(filepath.Join(dir, ".cairn")); err == nil && fi.IsDir() {
		fmt.Fprintf(os.Stderr, "cairn: already a cairn repo at %s\n", dir)
		return nil
	}
	r, err := worktree.Open(dir, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, err := r.DefaultBranch()
	if err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: initialized; edit files in %s/\n", filepath.Join(dir, branch))
	return nil
}

func cmdClone(args []string) error {
	fs := flag.NewFlagSet("clone", flag.ContinueOnError)
	author := fs.String("author", defaultAuthor(), "commit author")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn clone <url> [dir]")
	}
	url := fs.Arg(0)
	dir := ""
	if fs.NArg() > 1 {
		dir = fs.Arg(1)
	} else {
		dir = dirFromURL(url)
	}
	if dir == "" {
		return errors.New("cannot derive destination dir from url; pass it explicitly")
	}
	// Refuse to clone into a non-empty directory to avoid clobbering existing work.
	if ents, err := os.ReadDir(dir); err == nil && len(ents) > 0 {
		return fmt.Errorf("destination %s already exists and is not empty", dir)
	}
	r, err := worktree.Clone(url, dir, *author)
	if err != nil {
		return mapRemoteErr(err)
	}
	defer r.Close()
	fmt.Fprintf(os.Stderr, "cairn: cloned %s -> %s\n", url, dir)
	return nil
}

// dirFromURL derives a clone destination directory from a remote URL: the last
// path segment with any trailing ".git" stripped.
func dirFromURL(url string) string {
	trimmed := strings.TrimRight(url, "/")
	base := path.Base(trimmed)
	base = strings.TrimSuffix(base, ".git")
	if base == "." || base == "/" || base == "" {
		return ""
	}
	return base
}

func cmdExpress(args []string) error {
	fs := flag.NewFlagSet("express", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	from := fs.String("from", "", "parent branch to fork from")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	branch := fs.Arg(0)
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if err := r.Express(branch, *from); err != nil {
		return mapErr(err)
	}
	fmt.Printf("%s/%s\n", *repo, branch)
	return nil
}

func cmdUnexpress(args []string) error {
	fs := flag.NewFlagSet("unexpress", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	force := fs.Bool("force", false, "discard un-sealed work")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.Unexpress(fs.Arg(0), *force))
}

func cmdCommit(args []string) error {
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	msg := fs.String("m", "", "commit message")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	branch := fs.Arg(0)
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	res, err := r.Commit(branch, *msg)
	if err != nil {
		return mapErr(err)
	}
	// The commit succeeded; surface the best-effort auto-sync outcome on BOTH
	// the conflict and the clean path (before the branching below) so a notice
	// is never dropped when there are conflicts.
	switch note := r.LastSyncNote(); {
	case note == "synced":
		fmt.Fprintln(os.Stderr, "cairn: auto-synced with origin")
	case strings.HasPrefix(note, "skipped:"):
		fmt.Fprintf(os.Stderr, "cairn: auto-sync skipped: %s\n", strings.TrimPrefix(note, "skipped:"))
	}
	if len(res.Conflicts) > 0 {
		paths := make([]string, 0, len(res.Conflicts))
		for _, c := range res.Conflicts {
			paths = append(paths, c.Path)
		}
		fmt.Fprintf(os.Stderr, "%d conflict(s) in: %s\n", len(res.Conflicts), strings.Join(paths, ", "))
		return errConflicts
	}
	fmt.Println(res.HeadCommit)
	return nil
}

func cmdFold(args []string) error {
	fs := flag.NewFlagSet("fold", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	force := fs.Bool("force", false, "discard un-sealed work")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.Fold(fs.Arg(0), *force))
}

func cmdAbandon(args []string) error {
	fs := flag.NewFlagSet("abandon", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	force := fs.Bool("force", false, "discard un-sealed work")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.Abandon(fs.Arg(0), *force))
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch := ""
	if fs.NArg() > 0 {
		branch = fs.Arg(0)
	} else {
		// No branch given: default to the structural root's name, not the literal
		// "main" — after a clone of a master-default repo the root is "master".
		branch, err = r.DefaultBranch()
		if err != nil {
			return mapErr(err)
		}
	}
	st, err := r.Status(branch)
	if err != nil {
		return mapErr(err)
	}
	fmt.Printf("branch:    %s\n", st.Branch)
	fmt.Printf("lineage:   %s\n", strings.Join(st.Lineage, " → "))
	fmt.Printf("ahead:     %d\n", st.Ahead)
	fmt.Printf("conflicts: %s\n", strings.Join(st.Conflicts, ", "))
	fmt.Printf("expressed: %s\n", strings.Join(st.Expressed, ", "))
	if len(st.Modified)+len(st.Added)+len(st.Deleted) > 0 {
		fmt.Println("changes:")
		for _, p := range st.Modified {
			fmt.Printf("  M %s\n", p)
		}
		for _, p := range st.Added {
			fmt.Printf("  A %s\n", p)
		}
		for _, p := range st.Deleted {
			fmt.Printf("  D %s\n", p)
		}
	}
	return nil
}

// cmdDiff prints the unified diff for working-vs-tip (default or named branch) or
// commit-vs-commit. Binary files print a "Binary files differ" line.
func cmdDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	var diffs []change.FileDiff
	switch fs.NArg() {
	case 0, 1:
		branch := fs.Arg(0)
		if branch == "" {
			branch, err = r.DefaultBranch()
			if err != nil {
				return mapErr(err)
			}
		}
		diffs, err = r.WorkingDiff(branch)
	case 2:
		diffs, err = r.DiffCommits(fs.Arg(0), fs.Arg(1))
	default:
		return errors.New("usage: cairn diff [branch] | cairn diff <commitA> <commitB>")
	}
	if err != nil {
		return mapErr(err)
	}
	for _, d := range diffs {
		if d.Binary {
			fmt.Printf("Binary files differ: %s\n", d.Path)
			continue
		}
		if d.Unified != "" {
			fmt.Print(d.Unified)
		} else {
			fmt.Printf("%s: %s\n", d.Status, d.Path)
		}
	}
	return nil
}

func cmdTree(args []string) error {
	fs := flag.NewFlagSet("tree", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	nodes, err := r.Tree()
	if err != nil {
		return mapErr(err)
	}
	for _, n := range nodes {
		fmt.Printf("%s (parent %s) ahead=%d\n", n.Line.Name, n.Parent, n.Ahead)
	}
	return nil
}

func cmdLs(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	entries := r.Ls()
	branches := make([]string, 0, len(entries))
	for branch := range entries {
		branches = append(branches, branch)
	}
	sort.Strings(branches)
	for _, branch := range branches {
		fmt.Printf("%s  %s\n", branch, entries[branch].ChangeID)
	}
	return nil
}

func cmdResolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return errors.New("usage: cairn resolve <branch> <path>")
	}
	branch := fs.Arg(0)
	path := fs.Arg(1)
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.Resolve(branch, path))
}

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
	if err := fs.Parse(args); err != nil {
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
	cairn := fs.Bool("cairn", false, "register as a cairn remote (default git)")
	if err := fs.Parse(args); err != nil {
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
	fmt.Fprintf(os.Stderr, "cairn: added remote %s  %s  (%s)\n", name, url, kind)
	return nil
}

// cmdPush publishes the change-graph's branches + tags to a remote (default
// "origin"). --force overwrites a diverged remote branch.
func cmdPush(args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	force := fs.Bool("force", false, "force-overwrite a diverged remote branch")
	if err := fs.Parse(args); err != nil {
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
	// Spec: pushing to a remote registered as kind "cairn" must warn that
	// cairn->cairn fidelity isn't implemented yet and it's pushing as git. Done
	// in the CLI layer so the engine stays I/O-free.
	rems, err := r.Remotes()
	if err != nil {
		return mapErr(err)
	}
	for _, rem := range rems {
		if rem.Name == remote && rem.Kind == "cairn" {
			fmt.Fprintf(os.Stderr, "cairn: remote %q is type cairn; cairn->cairn fidelity not yet implemented, pushing as git\n", remote)
			break
		}
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
	if err := fs.Parse(args); err != nil {
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
	if err := fs.Parse(args); err != nil {
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

// cmdConfig gets or sets a config value. With one arg it prints the value (an
// empty line when unset); with two args it stores the value.
func cmdConfig(args []string) error {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn config <key> [value]")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	key := fs.Arg(0)
	if fs.NArg() == 1 {
		value, _, err := r.GetConfig(key)
		if err != nil {
			return mapErr(err)
		}
		fmt.Println(value)
		return nil
	}
	value := fs.Arg(1)
	if err := r.SetConfig(key, value); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: set %s=%s\n", key, value)
	return nil
}

// cmdTag names the tip of a branch with a tag. Usage:
//
//	cairn tag [--repo dir] <name> [branch]
//
// branch defaults to the structural root.
func cmdTag(args []string) error {
	fs := flag.NewFlagSet("tag", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn tag <name> [branch]")
	}
	name := fs.Arg(0)
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch := ""
	if fs.NArg() >= 2 {
		branch = fs.Arg(1)
	} else {
		branch, err = r.DefaultBranch()
		if err != nil {
			return mapErr(err)
		}
	}
	if err := r.Tag(name, branch); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: tagged %s -> %s\n", branch, name)
	return nil
}

// cmdVersion prints the derived version for the default branch, rendered for
// the requested ecosystem (default: plain semver). Stdout carries the version
// string ONLY so callers can do $(cairn version).
func cmdVersion(args []string) error {
	if len(args) > 0 && args[0] == "bump" {
		return cmdVersionBump(args[1:])
	}
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	target := fs.String("target", "", "render for ecosystem: npm|nuget|pypi|oci|go")
	releaseForm := fs.Bool("release", false, "print the clean release version that `cairn release` would tag")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, err := r.DefaultBranch()
	if err != nil {
		return mapErr(err)
	}
	cfg, err := version.LoadConfig(r.Root())
	if err != nil {
		return mapErr(err)
	}
	in, err := r.DeriveInput(branch, cfg)
	if err != nil {
		return mapErr(err)
	}
	var v version.Canonical
	if *releaseForm {
		v, err = version.ReleaseVersion(in)
	} else {
		v, err = version.Derive(in)
	}
	if err != nil {
		return mapErr(err)
	}
	out, err := version.Render(v, *target)
	if err != nil {
		return mapErr(err)
	}
	fmt.Println(out)
	return nil
}

// cmdVersionBump records explicit bump intent (major|minor|patch) for the next
// release. The level is positional and must appear before any flags.
func cmdVersionBump(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: cairn version bump major|minor|patch")
	}
	level := args[0]
	switch level {
	case "major", "minor", "patch":
	default:
		return errors.New("usage: cairn version bump major|minor|patch [--repo DIR]")
	}
	fs := flag.NewFlagSet("version bump", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if err := r.SetPendingBump(level); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: next release bump set to %s\n", level)
	return nil
}

// cmdRelease cuts a clean release version (e.g. v1.0.1) for the default branch
// and the requested ecosystem: it derives the next release version, stamps the
// manifest, tags, and publishes atomically (with --dry-run showing the plan).
func cmdRelease(args []string) error {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	target := fs.String("target", "", "ecosystem: npm|nuget|pypi|oci")
	dryRun := fs.Bool("dry-run", false, "show the plan without tagging or publishing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *target == "" {
		return errors.New("usage: cairn release --target npm|nuget|pypi|oci [--dry-run]")
	}
	switch *target {
	case "npm", "nuget", "pypi", "oci":
	default:
		return errors.New("usage: cairn release --target npm|nuget|pypi|oci [--dry-run]")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, err := r.DefaultBranch()
	if err != nil {
		return mapErr(err)
	}
	cfg, err := version.LoadConfig(r.Root())
	if err != nil {
		return mapErr(err)
	}
	in, err := r.DeriveInput(branch, cfg)
	if err != nil {
		return mapErr(err)
	}
	rel, err := version.ReleaseVersion(in)
	if err != nil {
		return mapErr(err)
	}
	rendered, err := version.Render(rel, *target)
	if err != nil {
		return mapErr(err)
	}
	port, err := r.ReleasePort(branch, *target)
	if err != nil {
		return mapErr(err)
	}
	opts := release.Options{
		Eco:     *target,
		Version: rendered,
		Core:    rel,
		TagName: cfg.TagPrefix + rel.String(),
		Dir:     filepath.Join(*repo, branch),
	}
	if *dryRun {
		plan, err := release.Plan(opts, port, newProbe())
		if err != nil {
			return mapErr(err)
		}
		fmt.Println(plan)
		return nil
	}
	if err := release.Release(opts, port, newPublisher(), newProbe()); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: released %s (%s) tagged %s\n", rendered, *target, opts.TagName)
	if *target == "npm" || *target == "pypi" || *target == "nuget" {
		fmt.Fprintf(os.Stderr, "cairn: manifest stamped but not committed — run `cairn commit %s` before the next release or a pull\n", branch)
	}
	return nil
}

// cmdUndo reverts the most recent operation, restoring every expressed branch's
// folder to the prior tip. The Phase-1 limitation (lines created by the undone
// op are not deleted) is surfaced as a note on stderr.
func cmdUndo(args []string) error {
	fs := flag.NewFlagSet("undo", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if err := r.Undo(); err != nil {
		return mapErr(err)
	}
	fmt.Fprintln(os.Stderr, "cairn: reverted the last operation (line tips restored; lines created by it are not removed)")
	return nil
}

// cmdOplog prints the operation log in chronological order (newest last,
// matching log-style reading). Each line: <op-id> <op-type> <actor> [detail].
func cmdOplog(args []string) error {
	fs := flag.NewFlagSet("oplog", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	ops, err := r.OperationLog()
	if err != nil {
		return mapErr(err)
	}
	for _, op := range ops {
		detail := op.Detail
		if detail != "" {
			detail = "  " + detail
		}
		fmt.Printf("%s  %-8s  %s%s\n", op.ID, op.OpType, op.Actor, detail)
	}
	return nil
}

// cmdBlame prints per-line provenance for a file at the tip of a branch,
// mapping each line back to its cairn change-id.
// Usage: cairn blame [--repo dir] <path> [branch]
func cmdBlame(args []string) error {
	fs := flag.NewFlagSet("blame", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn blame <path> [branch]")
	}
	path := fs.Arg(0)
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch := ""
	if fs.NArg() > 1 {
		branch = fs.Arg(1)
	} else if branch, err = r.DefaultBranch(); err != nil {
		return mapErr(err)
	}
	lines, err := r.Blame(branch, path)
	if err != nil {
		return mapErr(err)
	}
	for _, ln := range lines {
		id := ln.Commit
		if len(id) > 8 {
			id = id[:8]
		}
		if working, _ := r.IsWorkingCommit(ln.Commit); working {
			id = "(working)"
		}
		fmt.Printf("%-10s %-14s %s  %s\n", id, ln.Author, ln.When.Format("2006-01-02"), strings.TrimRight(ln.Text, "\n"))
	}
	return nil
}

// cmdLog prints the commit history of a branch, newest first.
// Usage: cairn log [branch] [-n N]
func cmdLog(args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	n := fs.Int("n", 20, "max commits to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch := ""
	var berr error
	if fs.NArg() > 0 {
		branch = fs.Arg(0)
	} else {
		branch, berr = r.DefaultBranch()
		if berr != nil {
			return mapErr(berr)
		}
	}
	commits, err := r.Log(branch, *n)
	if err != nil {
		return mapErr(err)
	}
	for _, c := range commits {
		short := c.SHA
		if len(short) > 8 {
			short = short[:8]
		}
		subject := c.Subject
		if c.Working {
			// The head of an open (unsealed) change is the live working commit. Its
			// description is the "(working)" placeholder until the change is named;
			// surface the marker once (avoid a doubled "(working) (working)").
			if subject == "" || subject == "(working)" {
				subject = "(working)"
			} else {
				subject = "(working) " + subject
			}
		}
		fmt.Printf("%s  %s  %s  %s\n", short, c.When.Format("2006-01-02"), c.AuthorName, subject)
	}
	return nil
}

// cmdShow prints a commit's metadata and the diff against its first parent.
// Usage: cairn show <commit>
func cmdShow(args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cairn show <commit>")
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	ci, diffs, err := r.Show(fs.Arg(0))
	if err != nil {
		return mapErr(err)
	}
	fmt.Printf("commit %s\nAuthor: %s <%s>\nDate:   %s\n\n", ci.SHA, ci.AuthorName, ci.AuthorEmail, ci.When.Format(time.RFC3339))
	for _, line := range strings.Split(ci.Message, "\n") {
		fmt.Printf("    %s\n", line)
	}
	fmt.Println()
	for _, d := range diffs {
		if d.Binary {
			fmt.Printf("Binary files differ: %s\n", d.Path)
			continue
		}
		if d.Unified != "" {
			fmt.Print(d.Unified)
		} else {
			fmt.Printf("%s: %s\n", d.Status, d.Path)
		}
	}
	return nil
}

// mapRemoteErr translates go-git transport/network failures into actionable
// guidance. It falls through to mapErr for anything it doesn't recognize.
func mapRemoteErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired),
		errors.Is(err, transport.ErrAuthorizationFailed):
		return errors.New("authentication failed — set $CAIRN_TOKEN (a personal access token) for an HTTPS remote, or check your ssh-agent/key for an SSH remote")
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return errors.New("repository not found — check the remote URL and that you have access")
	}
	// Network-shaped failures (no typed sentinel): match by shape/substring.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return errors.New("could not reach the remote — check the URL and your network connection")
	}
	msg := err.Error()
	for _, s := range []string{"no such host", "connection refused", "i/o timeout", "network is unreachable", "dial tcp"} {
		if strings.Contains(msg, s) {
			return errors.New("could not reach the remote — check the URL and your network connection")
		}
	}
	return mapErr(err)
}

// mapErr translates change-engine sentinels into operator-facing messages.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, change.ErrHasConflict):
		return fmt.Errorf("resolve conflicts before folding: %w", err)
	case errors.Is(err, change.ErrNotFound):
		return err
	default:
		return err
	}
}

// cmdStash dispatches stash sub-commands: pop, list, drop, or push (default).
func cmdStash(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "pop":
			return cmdStashPop(args[1:])
		case "list":
			return cmdStashList(args[1:])
		case "drop":
			return cmdStashDrop(args[1:])
		}
	}
	return cmdStashPush(args)
}

// cmdStashPush shelves the working change and resets the folder to the sealed tip.
func cmdStashPush(args []string) error {
	// Strip a leading literal "push" sub-verb if present.
	if len(args) > 0 && args[0] == "push" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("stash", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	msg := fs.String("m", "", "stash message")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, err := r.DefaultBranch()
	if err != nil {
		return mapErr(err)
	}
	if err := r.Stash(branch, *msg); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: shelved working changes; folder reset to %s's sealed state\n", branch)
	return nil
}

// cmdStashPop restores the most recent stash entry onto the working branch.
func cmdStashPop(args []string) error {
	fs := flag.NewFlagSet("stash pop", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, err := r.DefaultBranch()
	if err != nil {
		return mapErr(err)
	}
	if err := r.StashPop(branch); err != nil {
		return mapErr(err)
	}
	fmt.Fprintln(os.Stderr, "cairn: restored the most recent stash")
	return nil
}

// cmdStashList prints the stash stack to stdout, newest first.
func cmdStashList(args []string) error {
	fs := flag.NewFlagSet("stash list", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	entries, err := r.StashList()
	if err != nil {
		return mapErr(err)
	}
	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "cairn: no stashes")
		return nil
	}
	for _, s := range entries {
		date := s.CreatedAt
		if t, terr := time.Parse(time.RFC3339Nano, s.CreatedAt); terr == nil {
			date = t.Format("2006-01-02")
		} else if t, terr := time.Parse(time.RFC3339, s.CreatedAt); terr == nil {
			date = t.Format("2006-01-02")
		}
		fmt.Printf("%-4d %-12s %s  %s\n", s.ID, s.Branch, date, s.Message)
	}
	return nil
}

// cmdStashDrop discards a stash entry. An optional positional id selects the
// entry (default 0 = top of stack).
func cmdStashDrop(args []string) error {
	fs := flag.NewFlagSet("stash drop", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	var id int64
	if fs.NArg() > 0 {
		var parseErr error
		id, parseErr = strconv.ParseInt(fs.Arg(0), 10, 64)
		if parseErr != nil {
			return fmt.Errorf("invalid stash id %q: %w", fs.Arg(0), parseErr)
		}
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if err := r.StashDrop(id); err != nil {
		return mapErr(err)
	}
	fmt.Fprintln(os.Stderr, "cairn: stash entry discarded")
	return nil
}
