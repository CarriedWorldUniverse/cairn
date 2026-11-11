package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/userconfig"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	author := fs.String("author", defaultAuthor(), "commit author")
	initial := fs.String("initial-branch", "", "name the root line (default: init.defaultBranch, else main)")
	fs.StringVar(initial, "b", "", "name the root line (default: init.defaultBranch, else main)")
	bare := fs.Bool("bare", false, "refused: a cairn repo is always a set of line folders")
	quiet := fs.Bool("quiet", false, "no output")
	fs.BoolVar(quiet, "q", false, "no output")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if *bare {
		return errors.New("a cairn repo has no bare form — its lines are folders; to host a shared repo, run cairn-server (or use a bare git repo as a remote)")
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	// Re-init guard: if .cairn already exists, silently succeed (no-op, exit 0).
	if fi, err := os.Stat(filepath.Join(dir, ".cairn")); err == nil && fi.IsDir() {
		if !*quiet {
			fmt.Fprintf(os.Stderr, "cairn: already a cairn repo at %s\n", dir)
		}
		return nil
	}
	if *initial == "" {
		*initial = userconfig.Get("init.defaultbranch")
	}
	if *initial != "" {
		if err := change.ValidRefName(*initial); err != nil {
			return fmt.Errorf("invalid initial branch name %q: %w", *initial, err)
		}
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
	if *initial != "" && *initial != branch {
		if err := r.RenameLine(branch, *initial, false); err != nil {
			return mapErr(err)
		}
		branch = *initial
	}
	if !*quiet {
		fmt.Fprintf(os.Stderr, "cairn: initialized; edit files in %s/\n", filepath.Join(dir, worktree.FolderName(branch)))
	}
	return nil
}

func cmdClone(args []string) error {
	fs := flag.NewFlagSet("clone", flag.ContinueOnError)
	author := fs.String("author", defaultAuthor(), "commit author")
	branchOpt := fs.String("branch", "", "also express this remote line")
	fs.StringVar(branchOpt, "b", "", "also express this remote line")
	originName := fs.String("origin", "origin", "name the remote")
	fs.StringVar(originName, "o", "origin", "name the remote")
	quiet := fs.Bool("quiet", false, "no progress output")
	fs.BoolVar(quiet, "q", false, "no progress output")
	depth := fs.String("depth", "", "refused: cairn imports full history")
	fs.Bool("single-branch", false, "refused: cairn imports every line")
	fs.Bool("bare", false, "refused: a cairn repo is always a set of line folders")
	fs.Bool("mirror", false, "refused")
	fs.Bool("no-checkout", false, "refused")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	for _, n := range []string{"single-branch", "bare", "mirror", "no-checkout"} {
		if fs.Lookup(n).Value.String() == "true" {
			return fmt.Errorf("--%s is not supported: cairn clone imports the full history and every line so the line tree, change-ids and conflicts reconstruct exactly", n)
		}
	}
	if *depth != "" {
		return errors.New("--depth is not supported: cairn clone imports the full history so the line tree, change-ids and conflicts reconstruct exactly")
	}
	if *originName == "" {
		return errors.New("--origin needs a name")
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn clone [-b <line>] [-o <name>] [-q] <url> [dir]")
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
	// A destination we create ourselves is removed again if the clone fails
	// or -b names a line the remote does not have.
	_, statErr := os.Stat(dir)
	created := statErr != nil
	var progress io.Writer = os.Stderr
	if *quiet {
		progress = io.Discard
	} else {
		fmt.Fprintf(os.Stderr, "cairn: cloning %s into %s …\n", redactURL(url), dir)
	}
	r, err := worktree.Clone(url, dir, *author, progress)
	if err != nil {
		if created {
			_ = os.RemoveAll(dir)
		}
		return mapRemoteErr(err)
	}
	defer r.Close()
	if *originName != "origin" {
		if err := r.RenameRemote("origin", *originName); err != nil {
			return mapErr(err)
		}
	}
	if *branchOpt != "" {
		def, _ := r.DefaultBranch()
		if *branchOpt != def {
			if !lineExists(r, *branchOpt) {
				if created {
					_ = os.RemoveAll(dir)
				}
				return fmt.Errorf("remote branch %s not found in upstream %s", *branchOpt, *originName)
			}
			if err := r.Express(*branchOpt, ""); err != nil {
				return mapErr(err)
			}
			if !*quiet {
				fmt.Fprintf(os.Stderr, "cairn: expressed %s at %s\n", *branchOpt, filepath.Join(dir, worktree.FolderName(*branchOpt)))
			}
		}
	}
	if !*quiet {
		fmt.Fprintf(os.Stderr, "cairn: cloned %s -> %s\n", redactURL(url), dir)
	}
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
	// Snapshot first, like every other mutating verb: Resolve re-materializes
	// the folder from the store, so edits to the line's OTHER conflicted files
	// that no command had snapshotted yet would be overwritten with their
	// marked versions.
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.Resolve(branch, path, *force))
}

// printRebase reports one line's rebase onto its parent on stderr: a stop
// says what to do next; a clean rebase is one line; skips give the reason.
func printRebase(rb change.ParentRebase) {
	switch {
	case rb.Status == "rebased":
		fmt.Fprintf(os.Stderr, "cairn: %s: rebased onto its parent\n", rb.Line)
	case rb.Status == "stopped":
		fmt.Fprintf(os.Stderr, "cairn: %s: rebase onto its parent stopped at %q — %d conflict(s); edit them out, 'cairn resolve %s <path>', then 'cairn commit' (keeps the message) to replay the %d commit(s) after it\n",
			rb.Line, firstLine(rb.StoppedAt), rb.Conflicts, rb.Line, rb.Remaining)
	case strings.HasPrefix(rb.Status, "skipped: pushed"):
		// Published lines are expected to be skipped; not worth a line each.
	default:
		fmt.Fprintf(os.Stderr, "cairn: %s: not rebased onto its parent — %s\n", rb.Line, strings.TrimPrefix(rb.Status, "skipped: "))
	}
}

// firstLine is a commit message's subject.
func firstLine(msg string) string {
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		return msg[:i]
	}
	return msg
}
