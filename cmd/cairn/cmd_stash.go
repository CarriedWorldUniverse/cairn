package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

// cmdStash dispatches stash sub-commands; a bare `stash` (or one that starts
// with a flag or a branch) is `stash push`.
//
//	stash [push] [-m msg] [branch] [-- <path>...]   shelve the working change (or only some paths)
//	stash save [msg...]                             git's older spelling of push -m
//	stash pop   [branch] [id]                       restore a stash and drop it
//	stash apply [branch] [id]                       restore a stash, keep it on the stack
//	stash list | show [-p] [id] | drop [id] | clear
//	stash branch <name> [id]                        express a new line and pop the stash onto it
//
// An id is the number `stash list` prints, or git's stash@{N} (N entries down
// from the most recent).
func cmdStash(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "pop":
			return cmdStashRestore(args[1:], true)
		case "apply":
			return cmdStashRestore(args[1:], false)
		case "list":
			return cmdStashList(args[1:])
		case "drop":
			return cmdStashDrop(args[1:])
		case "show":
			return cmdStashShow(args[1:])
		case "clear":
			return cmdStashClear(args[1:])
		case "branch":
			return cmdStashBranch(args[1:])
		case "save":
			return cmdStashSave(args[1:])
		}
	}
	return cmdStashPush(args)
}

// stashPushFlags registers the flags push and save share.
func stashPushFlags(fs *flag.FlagSet) (repo, author, msg *string) {
	repo, author = repoFlags(fs)
	msg = new(string)
	fs.StringVar(msg, "m", "", "stash message")
	fs.StringVar(msg, "message", "", "stash message")
	// No index and no untracked files in cairn: these select nothing extra.
	for _, name := range []string{"u", "include-untracked", "a", "all", "k", "keep-index", "no-keep-index", "q", "quiet", "S", "staged"} {
		fs.Bool(name, false, "accepted for git compatibility (no-op)")
	}
	fs.Bool("p", false, "not supported")
	fs.Bool("patch", false, "not supported")
	return repo, author, msg
}

func refuseStashPatch(fs *flag.FlagSet) error {
	for _, name := range []string{"p", "patch"} {
		if fs.Lookup(name).Value.String() == "true" {
			return errors.New(`stash -p: cairn has no interactive hunk selection; shelve whole paths with "cairn stash push -- <path>..."`)
		}
	}
	return nil
}

// cmdStashPush shelves the working change and resets the folder to the sealed
// tip — or, given paths, shelves only the edits under them and leaves the
// rest. The first positional selects the branch when it names an expressed
// line (default: the folder you stand in, else the structural root).
func cmdStashPush(args []string) error {
	// Strip a leading literal "push" sub-verb if present.
	if len(args) > 0 && args[0] == "push" {
		args = args[1:]
	}
	before, pathsAfter, hasDD := splitDashDash(args)
	fs := flag.NewFlagSet("stash", flag.ContinueOnError)
	repo, author, msg := stashPushFlags(fs)
	if err := parseGitArgs(fs, before); err != nil {
		return err
	}
	if err := refuseStashPatch(fs); err != nil {
		return err
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	pos := fs.Args()
	var branch string
	switch {
	case len(pos) > 0 && (r.IsExpressed(pos[0]) || (len(pos) == 1 && !hasDD && !pathLooksReal(r, defaultBranchOr(r), pos[0]))):
		// An expressed line — or a lone name that is not a path either, which
		// keeps reporting "not expressed" as it always has.
		branch, pos = pos[0], pos[1:]
	default:
		if branch, err = r.DefaultBranch(); err != nil {
			return mapErr(err)
		}
	}
	return stashPush(r, branch, *msg, append(pos, pathsAfter...))
}

func defaultBranchOr(r *worktree.Repo) string {
	b, _ := r.DefaultBranch()
	return b
}

// cmdStashSave is `git stash save [<message>...]`: every positional is part of
// the message.
func cmdStashSave(args []string) error {
	fs := flag.NewFlagSet("stash save", flag.ContinueOnError)
	repo, author, msg := stashPushFlags(fs)
	if err := parseGitArgs(fs, args); err != nil {
		return err
	}
	if err := refuseStashPatch(fs); err != nil {
		return err
	}
	if *msg == "" {
		*msg = strings.Join(fs.Args(), " ")
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
	return stashPush(r, branch, *msg, nil)
}

func stashPush(r *worktree.Repo, branch, msg string, specs []string) error {
	var err error
	if len(specs) > 0 {
		if specs, err = treePathspecs(r, branch, specs); err != nil {
			return err
		}
		err = r.StashPaths(branch, msg, specs)
	} else {
		err = r.Stash(branch, msg)
	}
	if err != nil {
		// Nothing to stash is a no-op, not a failure (matches git's "No local
		// changes to save" → exit 0), so `stash` is script-safe.
		if errors.Is(err, change.ErrNothingToStash) {
			fmt.Fprintf(os.Stderr, "cairn: no working changes to stash on %s\n", branch)
			return nil
		}
		return mapErr(err)
	}
	if len(specs) > 0 {
		fmt.Fprintf(os.Stderr, "cairn: shelved the working changes under %s; the rest of %s's working change is untouched\n", strings.Join(specs, ", "), branch)
		return nil
	}
	fmt.Fprintf(os.Stderr, "cairn: shelved working changes; folder reset to %s's sealed state\n", branch)
	return nil
}

// stashTarget classifies the [branch] [id] positionals pop/apply/branch take,
// in either order: an expressed branch name wins (so a line really named "5"
// is still reachable); anything else must be a stash id or stash@{N}.
func stashTarget(r *worktree.Repo, args []string, usage string) (branch string, stashID int64, err error) {
	for _, arg := range args {
		if !r.IsExpressed(arg) {
			if id, perr := r.ResolveStashID(arg); perr == nil {
				if stashID != 0 {
					return "", 0, errUsage(usage)
				}
				stashID = id
				continue
			} else if strings.HasPrefix(arg, "stash@{") {
				return "", 0, perr
			}
		}
		if branch != "" {
			return "", 0, errUsage(usage)
		}
		branch = arg
	}
	if branch == "" {
		if branch, err = r.DefaultBranch(); err != nil {
			return "", 0, mapErr(err)
		}
	}
	return branch, stashID, nil
}

// cmdStashRestore is `stash pop` (drop) and `stash apply` (keep). The stash is
// merged into the working change three-way, so it applies over un-sealed
// edits and onto a line that has moved since; a conflict is recorded on the
// working change (exit 2) and the stash is kept.
func cmdStashRestore(args []string, drop bool) error {
	verb := "apply"
	if drop {
		verb = "pop"
	}
	fs := flag.NewFlagSet("stash "+verb, flag.ContinueOnError)
	repo, author := repoFlags(fs)
	for _, name := range []string{"index", "q", "quiet"} {
		fs.Bool(name, false, "accepted for git compatibility (no-op)")
	}
	if err := parseGitArgs(fs, args); err != nil {
		return err
	}
	usage := "cairn stash " + verb + " [branch] [id]"
	if fs.NArg() > 2 {
		return errUsage(usage)
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, stashID, err := stashTarget(r, fs.Args(), usage)
	if err != nil {
		return err
	}
	conflicts, err := r.StashRestore(branch, stashID, drop)
	if err != nil {
		return mapErr(err)
	}
	if len(conflicts) > 0 {
		paths := make([]string, 0, len(conflicts))
		for _, c := range conflicts {
			paths = append(paths, c.Path)
		}
		return reportConflicts("stash "+verb, paths,
			fmt.Sprintf("the stash is kept; edit the markers out and 'cairn resolve %s <path>' each (then 'cairn stash drop')", branch))
	}
	what := "the most recent stash"
	if stashID != 0 {
		what = fmt.Sprintf("stash %d", stashID)
	}
	if drop {
		fmt.Fprintf(os.Stderr, "cairn: restored %s\n", what)
	} else {
		fmt.Fprintf(os.Stderr, "cairn: applied %s (kept on the stack)\n", what)
	}
	return nil
}

// cmdStashList prints the stash stack to stdout, newest first.
func cmdStashList(args []string) error {
	fs := flag.NewFlagSet("stash list", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
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
		fmt.Printf("%-4d %-12s %s  %s\n", s.ID, s.Branch, stashDate(s.CreatedAt), s.Message)
	}
	return nil
}

func stashDate(created string) string {
	if t, terr := time.Parse(time.RFC3339Nano, created); terr == nil {
		return t.Format("2006-01-02")
	} else if t, terr := time.Parse(time.RFC3339, created); terr == nil {
		return t.Format("2006-01-02")
	}
	return created
}

// oneStashID parses the optional [id] positional of show/drop.
func oneStashID(r *worktree.Repo, fs *flag.FlagSet, usage string) (int64, error) {
	switch fs.NArg() {
	case 0:
		return 0, nil
	case 1:
		return r.ResolveStashID(fs.Arg(0))
	}
	return 0, errUsage(usage)
}

// cmdStashDrop discards a stash entry (default: the most recent).
func cmdStashDrop(args []string) error {
	fs := flag.NewFlagSet("stash drop", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	fs.Bool("q", false, "accepted for git compatibility (no-op)")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	id, err := oneStashID(r, fs, "cairn stash drop [id]")
	if err != nil {
		return err
	}
	if err := r.StashDrop(id); err != nil {
		return mapErr(err)
	}
	fmt.Fprintln(os.Stderr, "cairn: stash entry discarded")
	return nil
}

// cmdStashShow prints what a stash holds: a name-status summary, or the diff
// with -p.
func cmdStashShow(args []string) error {
	fs := flag.NewFlagSet("stash show", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	var patch bool
	fs.BoolVar(&patch, "p", false, "show the diff")
	fs.BoolVar(&patch, "patch", false, "show the diff")
	for _, name := range []string{"stat", "name-status", "u", "include-untracked"} {
		fs.Bool(name, false, "accepted for git compatibility (no-op)")
	}
	if err := parseGitArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	id, err := oneStashID(r, fs, "cairn stash show [-p] [id]")
	if err != nil {
		return err
	}
	s, diffs, err := r.StashDiff(id)
	if err != nil {
		return mapErr(err)
	}
	if !patch {
		fmt.Fprintf(os.Stderr, "cairn: stash %d on %s: %s\n", s.ID, s.Branch, s.Message)
	}
	for _, d := range diffs {
		switch {
		case !patch:
			fmt.Printf("%s\t%s\n", map[change.FileStatus]string{change.Added: "A", change.Modified: "M", change.Deleted: "D"}[d.Status], d.Path)
		case d.Binary:
			fmt.Printf("Binary files differ: %s\n", d.Path)
		case d.Unified != "":
			fmt.Print(d.Unified)
		default:
			fmt.Printf("%s: %s\n", d.Status, d.Path)
		}
	}
	return nil
}

// cmdStashClear discards the whole stash stack.
func cmdStashClear(args []string) error {
	fs := flag.NewFlagSet("stash clear", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	n, err := r.StashClear()
	if err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: discarded %d stash entr%s\n", n, map[bool]string{true: "y", false: "ies"}[n == 1])
	return nil
}

// cmdStashBranch is `git stash branch <name> [id]`: express a new line off the
// line the stash was taken on, and pop the stash onto it.
func cmdStashBranch(args []string) error {
	fs := flag.NewFlagSet("stash branch", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	usage := "cairn stash branch <name> [id]"
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return errUsage(usage)
	}
	name := fs.Arg(0)
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if r.IsExpressed(name) {
		return fmt.Errorf("stash branch: %q already exists; 'cairn stash pop %s' restores onto it", name, name)
	}
	var id int64
	if fs.NArg() == 2 {
		if id, err = r.ResolveStashID(fs.Arg(1)); err != nil {
			return err
		}
	}
	s, err := r.StashGet(id)
	if err != nil {
		return mapErr(err)
	}
	if err := r.Express(name, s.Branch); err != nil {
		return mapErr(err)
	}
	conflicts, err := r.StashRestore(name, s.ID, true)
	if err != nil {
		return mapErr(err)
	}
	fmt.Printf("%s/%s\n", *repo, worktree.FolderName(name))
	if len(conflicts) > 0 {
		paths := make([]string, 0, len(conflicts))
		for _, c := range conflicts {
			paths = append(paths, c.Path)
		}
		return reportConflicts("stash branch", paths,
			fmt.Sprintf("%s moved since the stash was taken; the stash is kept — resolve on %s, then 'cairn stash drop %d'", s.Branch, name, s.ID))
	}
	fmt.Fprintf(os.Stderr, "cairn: expressed %s from %s and restored stash %d onto it\n", name, s.Branch, s.ID)
	return nil
}
