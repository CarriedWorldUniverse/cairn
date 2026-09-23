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
	// No --from: fork from the line whose folder you are standing in, the way
	// `git checkout -b` forks from the current branch — the reflex the
	// translation table promises. At the repo root (or outside it) the
	// structural root remains the default, as before.
	if *from == "" {
		if here, ok := branchFromDir(r, ".", r.Root()); ok && here != branch {
			*from = here
		}
	}
	if *from != "" {
		fmt.Fprintf(os.Stderr, "cairn: expressing %s from %s …\n", branch, *from)
	} else {
		fmt.Fprintf(os.Stderr, "cairn: expressing %s …\n", branch)
	}
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
	infer := fs.Bool("infer", false, "re-derive every line's parent from topology (as a fresh clone would)")
	dryRun := fs.Bool("dry-run", false, "with --infer: print what would change, change nothing")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if *infer {
		r, err := openRepo(*repo, *author)
		if err != nil {
			return mapErr(err)
		}
		defer r.Close()
		changes, err := r.ReparentInfer(*dryRun)
		if err != nil {
			return mapErr(err)
		}
		verb := "reparented"
		if *dryRun {
			verb = "would reparent"
		}
		for _, c := range changes {
			fmt.Printf("%s %s: %s → %s\n", verb, c.Line, c.OldParent, c.NewParent)
		}
		if len(changes) == 0 {
			fmt.Fprintln(os.Stderr, "cairn: every line is already under its inferred parent")
		} else {
			fmt.Fprintf(os.Stderr, "cairn: %s %d line(s)\n", verb, len(changes))
		}
		return nil
	}
	if fs.NArg() < 2 {
		return errors.New("usage: cairn reparent <branch> <new-parent>  |  cairn reparent --infer [--dry-run]")
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
	if st.Remote != "" {
		fmt.Printf("remote:    %s\n", st.Remote)
	}
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
	flat := fs.Bool("flat", false, "one line per line with the parent's id, for scripts")
	fetch := fs.Bool("fetch", false, "refresh the remote-tracking refs first (fetch + prune), so the remote state is current")
	gone := fs.Bool("gone", false, "only lines whose branch no longer exists on the remote")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if *fetch {
		remote, err := r.TreeRemote()
		if err != nil {
			return mapErr(err)
		}
		if remote == "" {
			return errors.New("--fetch: this repo has no remote")
		}
		if err := r.FetchPruned(remote); err != nil {
			return mapRemoteErr(err)
		}
	}
	nodes, err := r.Tree()
	if err != nil {
		return mapErr(err)
	}
	if *gone {
		var kept []worktree.TreeNode
		for _, n := range nodes {
			if n.Remote != nil && n.Remote.Kind == "gone" {
				kept = append(kept, n)
			}
		}
		if *flat {
			for _, n := range kept {
				fmt.Printf("%s (parent %s) ahead=%d remote=gone\n", n.Line.Name, n.Parent, n.Ahead)
			}
			return nil
		}
		for _, n := range kept {
			fmt.Printf("%s  ahead=%d  [gone]\n", n.Line.Name, n.Ahead)
		}
		if len(kept) == 0 {
			fmt.Fprintln(os.Stderr, "cairn: no line's branch is gone from the remote")
		}
		return nil
	}
	if *flat {
		for _, n := range nodes {
			if n.Remote != nil {
				fmt.Printf("%s (parent %s) ahead=%d remote=%s\n", n.Line.Name, n.Parent, n.Ahead, n.Remote.Kind)
			} else {
				fmt.Printf("%s (parent %s) ahead=%d\n", n.Line.Name, n.Parent, n.Ahead)
			}
		}
		return nil
	}
	fmt.Print(renderTree(nodes))
	if len(nodes) > 0 && nodes[0].RemoteName != "" && !*fetch {
		fmt.Fprintf(os.Stderr, "cairn: [state] is against %s as of the last fetch or push; cairn tree --fetch refreshes it\n", nodes[0].RemoteName)
	}
	return nil
}

// renderTree draws the line tree with names and connectors, children sorted
// by name under their parent:
//
//	main  ahead=0
//	├─ develop  ahead=2
//	│  └─ feature  ahead=1
//	└─ hotfix  ahead=1
//
// LineNode.Parent is the parent's id (a line's parent may share its name
// with nothing, but ids are what the catalogue links), so the tree is built
// by id and printed by name. A node whose parent is not in the list (it
// should not happen; abandoned lines are already filtered) is shown at the
// top level rather than dropped.
func renderTree(nodes []worktree.TreeNode) string {
	byID := map[string]worktree.TreeNode{}
	for _, n := range nodes {
		byID[n.Line.ID] = n
	}
	children := map[string][]worktree.TreeNode{}
	var roots []worktree.TreeNode
	for _, n := range nodes {
		if _, ok := byID[n.Parent]; n.Parent == "" || !ok {
			roots = append(roots, n)
			continue
		}
		children[n.Parent] = append(children[n.Parent], n)
	}
	byName := func(a []worktree.TreeNode) {
		sort.Slice(a, func(i, j int) bool { return a[i].Line.Name < a[j].Line.Name })
	}
	byName(roots)
	for _, kids := range children {
		byName(kids)
	}
	var b strings.Builder
	label := func(n worktree.TreeNode) string {
		if n.Remote == nil {
			return fmt.Sprintf("%s  ahead=%d", n.Line.Name, n.Ahead)
		}
		return fmt.Sprintf("%s  ahead=%d  [%s]", n.Line.Name, n.Ahead, n.Remote)
	}
	var walk func(n worktree.TreeNode, prefix string)
	walk = func(n worktree.TreeNode, prefix string) {
		kids := children[n.Line.ID]
		for i, k := range kids {
			last := i == len(kids)-1
			branch, next := "├─ ", "│  "
			if last {
				branch, next = "└─ ", "   "
			}
			fmt.Fprintf(&b, "%s%s%s\n", prefix, branch, label(k))
			walk(k, prefix+next)
		}
	}
	for _, r := range roots {
		fmt.Fprintf(&b, "%s\n", label(r))
		walk(r, "")
	}
	return b.String()
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
