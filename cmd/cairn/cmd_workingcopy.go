package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	author := fs.String("author", defaultAuthor(), "commit author")
	if err := parseArgs(fs, args); err != nil {
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
	if err := parseArgs(fs, args); err != nil {
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
	fmt.Fprintf(os.Stderr, "cairn: cloning %s into %s …\n", redactURL(url), dir)
	r, err := worktree.Clone(url, dir, *author, os.Stderr)
	if err != nil {
		return mapRemoteErr(err)
	}
	defer r.Close()
	fmt.Fprintf(os.Stderr, "cairn: cloned %s -> %s\n", redactURL(url), dir)
	return nil
}

// redactURL hides any embedded credential in a URL before it is printed, so a
// token passed in a clone/remote URL never lands in terminal output, logs or CI.
// The host/path stay visible; the userinfo becomes "***".
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil // drop the credential from the displayed URL (host/path stay)
	return u.String()
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
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	branch := fs.Arg(0)
	r, started, err := openRepoSyncedVerbose(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	defer reportElapsed("express", started)
	fmt.Fprintf(os.Stderr, "cairn: expressing %s …\n", branch)
	if err := r.Express(branch, *from); err != nil {
		return mapErr(err)
	}
	fmt.Printf("%s/%s\n", *repo, worktree.FolderName(branch))
	return nil
}

func cmdUnexpress(args []string) error {
	fs := flag.NewFlagSet("unexpress", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	force := fs.Bool("force", false, "discard un-sealed work")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	r, started, err := openRepoSyncedVerbose(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	defer reportElapsed("unexpress", started)
	return mapErr(r.Unexpress(fs.Arg(0), *force))
}

func cmdCommit(args []string) error {
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	msg := fs.String("m", "", "commit message")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	var branch string
	if fs.NArg() > 0 {
		branch = fs.Arg(0)
	} else if b, ok := r.CWDBranch(); ok {
		branch = b // run from inside a branch folder → commit that branch
	} else {
		return errors.New("branch required (or run from inside a branch folder)")
	}
	ensureIdentity(r)
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
	// Surface unreadable-untracked skips structurally on STDOUT (#130) — not
	// just the scan's own capped stderr warnf lines, which are lost under
	// redirection or a GUI wrapper. Exit code stays 0 either way (below);
	// like git, cairn tolerates unreadable untracked content rather than
	// failing the commit over it.
	printSkippedUnreadable(res.SkippedUnreadable)
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
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	r, started, err := openRepoSyncedVerbose(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	defer reportElapsed("fold", started)
	return mapErr(r.Fold(fs.Arg(0), *force))
}

func cmdReparent(args []string) error {
	fs := flag.NewFlagSet("reparent", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return errors.New("usage: cairn reparent <branch> <new-parent>")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if err := r.Reparent(fs.Arg(0), fs.Arg(1)); err != nil {
		return mapErr(err)
	}
	fmt.Printf("reparented %s onto %s\n", fs.Arg(0), fs.Arg(1))
	return nil
}

func cmdAbandon(args []string) error {
	fs := flag.NewFlagSet("abandon", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	force := fs.Bool("force", false, "discard un-sealed work")
	if err := parseArgs(fs, args); err != nil {
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
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
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
	// Surface unreadable-untracked skips structurally on STDOUT (#130); see
	// the identical block in cmdCommit.
	printSkippedUnreadable(st.SkippedUnreadable)
	return nil
}

// printSkippedUnreadable prints the #130 structural notice for
// unreadable-untracked paths a worktree scan had to skip, so the fact is
// visible on STDOUT — not just via the scan's own capped stderr warnf lines
// (see worktree.skipTracker), which are easy to lose under redirection or a
// GUI wrapper. Prints nothing when paths is empty. Never changes a command's
// exit code: like git, cairn tolerates unreadable untracked content rather
// than failing commit/status over it — a strict "fail on skip" mode is a
// deliberately separate, opt-in feature, out of scope for #130.
func printSkippedUnreadable(paths []string) {
	if len(paths) == 0 {
		return
	}
	fmt.Printf("skipped %d unreadable untracked path(s) — not included in this commit:\n", len(paths))
	// showMax is intentionally a SEPARATE cap from worktree's
	// maxIndividualSkipWarnings: this one bounds the one-shot stdout summary
	// printed here from the already-complete structural list (paths); that one
	// bounds noisy per-scan stderr chatter emitted DURING the walk itself. No
	// reason for the two to move together.
	const showMax = 10
	shown := paths
	if len(shown) > showMax {
		shown = shown[:showMax]
	}
	for _, p := range shown {
		fmt.Printf("  %s\n", worktree.DisplayPath(p))
	}
	if extra := len(paths) - len(shown); extra > 0 {
		fmt.Printf("  … and %d more\n", extra)
	}
}

func cmdTree(args []string) error {
	fs := flag.NewFlagSet("tree", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
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
	if err := parseArgs(fs, args); err != nil {
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
	force := fs.Bool("force", false, "accept the content even if it still contains conflict markers")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return errors.New("usage: cairn resolve [--force] <branch> <path>")
	}
	branch := fs.Arg(0)
	path := fs.Arg(1)
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.Resolve(branch, path, *force))
}
