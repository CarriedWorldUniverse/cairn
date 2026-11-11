package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

// noIndexNote is what the index-only forms of git verbs print: there is
// nothing for them to do, and scripts must keep going.
const noIndexNote = `no staging area here — nothing is ever staged, so there is nothing to unstage; ` +
	`your edits stay in the working change ("cairn restore <path>" discards them, "cairn commit -- <path>" seals only some)`

// cmdRestore discards working-change edits to paths, restoring them from the
// line's sealed tip or from --source.
//
//	cairn restore [--source=<rev>] [--worktree] [--staged] [--branch <line>] [--] <pathspec>...
//
// git semantics: a deleted file comes back; a file the source does not have is
// removed. --staged alone is a no-op (no index). `cairn undo` reverts it.
func cmdRestore(args []string) error {
	before, pathsAfter, _ := splitDashDash(args)
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	explicit := branchFlag(fs)
	var source string
	fs.StringVar(&source, "source", "", "restore from this commit instead of the line's sealed tip")
	fs.StringVar(&source, "s", "", "restore from this commit instead of the line's sealed tip")
	var wt, staged bool
	fs.BoolVar(&wt, "worktree", false, "restore the working files (the default)")
	fs.BoolVar(&wt, "W", false, "restore the working files (the default)")
	fs.BoolVar(&staged, "staged", false, "accepted for git compatibility: cairn has no index")
	fs.BoolVar(&staged, "S", false, "accepted for git compatibility: cairn has no index")
	for _, name := range []string{"q", "quiet", "progress", "no-progress"} {
		fs.Bool(name, false, "accepted for git compatibility (no-op)")
	}
	for _, name := range []string{"ours", "theirs", "merge", "m", "p", "patch"} {
		fs.Bool(name, false, "not supported")
	}
	if err := parseGitArgs(fs, before); err != nil {
		return err
	}
	for _, name := range []string{"ours", "theirs", "merge", "m"} {
		if f := fs.Lookup(name); f.Value.String() == "true" {
			return fmt.Errorf(`restore --%s: a conflicted path is settled by editing the file and running "cairn resolve <branch> <path>"`, name)
		}
	}
	for _, name := range []string{"p", "patch"} {
		if f := fs.Lookup(name); f.Value.String() == "true" {
			return errors.New(`restore --patch: cairn has no interactive hunk selection; restore whole paths, or edit the file`)
		}
	}
	specs := append(fs.Args(), pathsAfter...)
	if staged && !wt {
		fmt.Fprintln(os.Stderr, "cairn: "+noIndexNote)
		return nil
	}
	if len(specs) == 0 {
		return errors.New("you must specify path(s) to restore (\"cairn restore .\" for the whole line; \"cairn reset --hard\" also drops open conflicts)")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, err := targetBranch(r, *explicit)
	if err != nil {
		return mapErr(err)
	}
	if specs, err = treePathspecs(r, branch, specs); err != nil {
		return err
	}
	changed, err := r.Restore(branch, source, specs)
	if err != nil {
		return mapErr(err)
	}
	from := "its sealed state"
	if source != "" {
		from = source
	}
	if len(changed) == 0 {
		fmt.Fprintf(os.Stderr, "cairn: nothing to restore — the path(s) already match %s\n", from)
		return nil
	}
	fmt.Fprintf(os.Stderr, "cairn: restored %d path(s) on %s from %s\n", len(changed), branch, from)
	return nil
}

// cmdReset is git's reset, in cairn's model.
//
//	cairn reset --hard                          discard the line's whole working change
//	cairn reset [--soft|--mixed|--hard] <rev>   move the line's sealed tip back to <rev>
//	cairn reset [<rev>] [--] <path>...          no-op: there is no index to reset
//
// --soft and --mixed are the same thing here (no index): the files on disk are
// kept, so everything after <rev> becomes the working change. --hard rewrites
// the folder to <rev>. Moving the tip is a history edit and follows the same
// rules as reword/drop; see change.ResetLine for what <rev> may be.
func cmdReset(args []string) error {
	before, pathsAfter, hasDD := splitDashDash(args)
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	explicit := branchFlag(fs)
	hard := fs.Bool("hard", false, "also rewrite the folder (discarding un-sealed edits)")
	soft := fs.Bool("soft", false, "keep the files; the difference becomes the working change")
	mixed := fs.Bool("mixed", false, "same as --soft in cairn (there is no index)")
	keep := fs.Bool("keep", false, "not supported")
	merge := fs.Bool("merge", false, "not supported")
	patch := fs.Bool("p", false, "not supported")
	for _, name := range []string{"q", "quiet", "no-refresh", "refresh"} {
		fs.Bool(name, false, "accepted for git compatibility (no-op)")
	}
	if err := parseGitArgs(fs, before); err != nil {
		return err
	}
	if *keep || *merge {
		return errors.New(`reset --keep/--merge are not supported; use --soft (keep the files) or --hard (discard them)`)
	}
	if *patch {
		return errors.New(`reset -p: cairn has no index and no interactive hunk selection`)
	}
	modes := 0
	for _, m := range []bool{*hard, *soft, *mixed} {
		if m {
			modes++
		}
	}
	if modes > 1 {
		return errors.New("reset: --soft, --mixed and --hard are mutually exclusive")
	}
	pos := fs.Args()
	if (hasDD && len(pathsAfter) > 0) || len(pos) > 1 {
		if *hard || *soft {
			return errors.New("reset: cannot do a --hard/--soft reset with paths; \"cairn restore <path>\" discards edits to a path")
		}
		fmt.Fprintln(os.Stderr, "cairn: "+noIndexNote)
		return nil
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, err := targetBranch(r, *explicit)
	if err != nil {
		return mapErr(err)
	}
	rev := ""
	if len(pos) == 1 {
		rev = pos[0]
		if _, rerr := r.ResolveRev(branch, rev); rerr != nil {
			// `git reset <path>` (unstage) written without "--".
			if !*hard && !*soft && pathLooksReal(r, branch, rev) {
				fmt.Fprintln(os.Stderr, "cairn: "+noIndexNote)
				return nil
			}
			return rerr
		}
	}
	if rev == "" && !*hard {
		// `git reset` / `git reset --soft` with no commit only touches the index.
		fmt.Fprintln(os.Stderr, "cairn: "+noIndexNote)
		return nil
	}
	info, err := r.Reset(branch, rev, *hard)
	if err != nil {
		return mapErr(amendHint(err))
	}
	switch {
	case rev == "":
		fmt.Fprintf(os.Stderr, "cairn: discarded the working change on %s (cairn undo brings it back)\n", branch)
	case info.Dropped == 0 && *hard:
		fmt.Fprintf(os.Stderr, "cairn: %s is already at %s; discarded its working change (cairn undo brings it back)\n", branch, shortSha(info.NewTip))
	case info.Dropped == 0:
		fmt.Fprintf(os.Stderr, "cairn: %s is already at %s\n", branch, shortSha(info.NewTip))
	case *hard:
		fmt.Fprintf(os.Stderr, "cairn: %s is now at %s (%d commit(s) dropped, folder rewritten; cairn undo brings them back)\n", branch, shortSha(info.NewTip), info.Dropped)
	default:
		fmt.Fprintf(os.Stderr, "cairn: %s is now at %s (%d commit(s) un-sealed — their changes are in the working change)\n", branch, shortSha(info.NewTip), info.Dropped)
	}
	if info.ForceRemote != "" {
		fmt.Fprintf(os.Stderr, "cairn: %s still has the commit(s) you dropped — the next 'cairn push %s %s' needs --force\n", info.ForceRemote, info.ForceRemote, branch)
	}
	return nil
}

// pathLooksReal reports whether arg names something in the line's folder.
func pathLooksReal(r *worktree.Repo, branch, arg string) bool {
	specs, err := treePathspecs(r, branch, []string{arg})
	if err != nil {
		return false
	}
	folder, ok := r.FolderOf(branch)
	if !ok {
		return false
	}
	_, serr := os.Stat(folder + string(os.PathSeparator) + specs[0])
	return serr == nil
}

// sequencerFlags are git's --continue/--abort/--skip/--quit, which
// cherry-pick and revert share.
type sequencerFlags struct{ cont, abort, skip, quit *bool }

func addSequencerFlags(fs *flag.FlagSet) sequencerFlags {
	return sequencerFlags{
		cont:  fs.Bool("continue", false, "seal the resolved cherry-pick/revert"),
		abort: fs.Bool("abort", false, "rewind the cherry-pick/revert"),
		skip:  fs.Bool("skip", false, "not supported (cairn keeps no sequencer state)"),
		quit:  fs.Bool("quit", false, "forget the pending message, keep the files"),
	}
}

// run handles the sequencer flags. cairn keeps no sequencer state: a pick or
// revert that hits a conflict stops with the conflict recorded on the working
// change, and what git calls --continue is simply `resolve` + `commit`. So:
//
//	--continue  seal the working change with the message the stopped verb left
//	--abort     rewind the line to before the verb (when it is still the last
//	            thing that happened to the line)
//	--quit      forget the pending message; leave the files as they are
//	--skip      refused — re-run the verb with the commits still to apply
func (s sequencerFlags) run(r *worktree.Repo, branch, verb string) (handled bool, err error) {
	switch {
	case *s.cont:
		st, serr := r.Status(branch)
		if serr != nil {
			return true, mapErr(serr)
		}
		if len(st.Conflicts) > 0 {
			return true, fmt.Errorf("%s --continue: %d conflict(s) still open in: %s — edit them out and run 'cairn resolve %s <path>' for each first",
				verb, len(st.Conflicts), strings.Join(st.Conflicts, ", "), branch)
		}
		msg := r.PendingMessage(branch)
		if msg == "" {
			if len(st.Modified)+len(st.Added)+len(st.Deleted) == 0 {
				fmt.Fprintf(os.Stderr, "cairn: no %s in progress on %s\n", verb, branch)
				return true, nil
			}
			return true, fmt.Errorf("%s --continue: no %s is waiting on %s — seal your changes with 'cairn commit -m <msg>'", verb, verb, branch)
		}
		ensureIdentity(r)
		res, cerr := r.CommitWith(branch, msg, worktree.CommitOpts{AllowEmpty: true})
		if cerr != nil {
			return true, mapErr(cerr)
		}
		fmt.Println(res.HeadCommit)
		if len(res.Conflicts) > 0 {
			return true, errConflicts
		}
		return true, nil
	case *s.abort:
		if aerr := r.AbortPick(branch, verb); aerr != nil {
			return true, mapErr(aerr)
		}
		fmt.Fprintf(os.Stderr, "cairn: %s aborted; %s is back where it was\n", verb, branch)
		return true, nil
	case *s.quit:
		r.ClearPendingMessage(branch)
		return true, nil
	case *s.skip:
		return true, fmt.Errorf(`%s --skip: cairn keeps no sequencer state — discard the stopped change with "cairn %s --abort", then re-run "cairn %s" with the commits still to apply`, verb, verb, verb)
	}
	return false, nil
}

// cmdRevert adds a new sealed commit that takes a commit back out.
//
//	cairn revert [-n|--no-commit] [-m <parent>] [--no-edit] [--branch <line>] <commit>...
//	cairn revert --continue | --abort | --quit
//
// The message is git's: `Revert "<subject>"` + `This reverts commit <sha>.`.
// A revert that does not apply cleanly is left in the working change with the
// conflict recorded (exit 2): edit it out, `cairn resolve`, then `cairn commit`
// (or `cairn revert --continue`) — the message is kept for you.
func cmdRevert(args []string) error {
	fs := flag.NewFlagSet("revert", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	explicit := branchFlag(fs)
	var noCommit bool
	fs.BoolVar(&noCommit, "n", false, "apply to the working change without sealing")
	fs.BoolVar(&noCommit, "no-commit", false, "apply to the working change without sealing")
	var mainline int
	fs.IntVar(&mainline, "m", 0, "parent number (from 1) a merge commit is reverted against")
	fs.IntVar(&mainline, "mainline", 0, "parent number (from 1) a merge commit is reverted against")
	seq := addSequencerFlags(fs)
	for _, name := range []string{"no-edit", "e", "edit", "s", "signoff", "q", "quiet"} {
		fs.Bool(name, false, "accepted for git compatibility (no-op)")
	}
	if err := parseGitArgs(fs, args); err != nil {
		return err
	}
	anySeq := *seq.cont || *seq.abort || *seq.skip || *seq.quit
	if fs.NArg() == 0 && !anySeq {
		return errUsage("cairn revert [-n] [-m <parent>] <commit>...  |  cairn revert --continue|--abort")
	}
	// --abort must see the log as the verb left it: a command-start sync would
	// append a snapshot of the half-resolved folder.
	open := openRepoSynced
	if *seq.abort {
		open = openRepo
	}
	r, err := open(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, err := targetBranch(r, *explicit)
	if err != nil {
		return mapErr(err)
	}
	if handled, serr := seq.run(r, branch, "revert"); handled {
		return serr
	}
	// git reverts the commits in the order given; a range is newest-first.
	var commits []string
	for _, arg := range fs.Args() {
		shas, rerr := r.ResolveRange(branch, arg)
		if rerr != nil {
			return mapErr(rerr)
		}
		if strings.Contains(arg, "..") {
			for i, j := 0, len(shas)-1; i < j; i, j = i+1, j-1 {
				shas[i], shas[j] = shas[j], shas[i]
			}
		}
		commits = append(commits, shas...)
	}
	ensureIdentity(r)
	for i, sha := range commits {
		res, rerr := r.Revert(branch, sha, mainline, noCommit)
		if rerr != nil {
			return mapErr(rerr)
		}
		if res.Commit != "" {
			fmt.Println(res.Commit)
		}
		if len(res.Conflicts) > 0 {
			paths := make([]string, 0, len(res.Conflicts))
			for _, c := range res.Conflicts {
				paths = append(paths, c.Path)
			}
			next := fmt.Sprintf("edit them out, 'cairn resolve %s <path>' each, then 'cairn commit' (the revert message is kept); 'cairn revert --abort' rewinds", branch)
			if res.Commit != "" {
				next = fmt.Sprintf("the revert is sealed, but it overlaps your un-sealed edits: edit the markers out and 'cairn resolve %s <path>' each", branch)
			}
			if rest := commits[i+1:]; len(rest) > 0 {
				next += fmt.Sprintf("; not yet reverted: %s", strings.Join(shortShas(rest), " "))
			}
			return reportConflicts("revert", paths, next)
		}
		if res.Commit == "" {
			fmt.Fprintf(os.Stderr, "cairn: reverted %s in the working change of %s (not sealed) — 'cairn commit' seals it with the revert message\n", shortSha(sha), branch)
		}
	}
	return nil
}

// cmdCherryPick applies the delta of one or more commits onto a branch, each
// as a new sealed commit (original message, fresh change-id), rebasing the
// open working change on top. Conflicts are returned as data (exit 2) so the
// operator can resolve them and continue.
//
//	cairn cherry-pick [-n] [-x] [--branch <line>] <commit|A..B>... [branch]
//	cairn cherry-pick --continue | --abort | --quit
//
// The trailing positional is the branch when it names an expressed line (the
// original `cherry-pick <commit> [branch]` form); otherwise the branch is the
// folder you are in, else the root line. With several commits the run stops at
// the first conflict and says which were not applied.
func cmdCherryPick(args []string) error {
	fs := flag.NewFlagSet("cherry-pick", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	explicit := branchFlag(fs)
	var noCommit bool
	fs.BoolVar(&noCommit, "n", false, "apply to the working change without sealing")
	fs.BoolVar(&noCommit, "no-commit", false, "apply to the working change without sealing")
	appendX := fs.Bool("x", false, `append "(cherry picked from commit <sha>)" to the message`)
	seq := addSequencerFlags(fs)
	for _, name := range []string{"e", "edit", "no-edit", "s", "signoff", "ff", "allow-empty", "keep-redundant-commits"} {
		fs.Bool(name, false, "accepted for git compatibility (no-op)")
	}
	if err := parseGitArgs(fs, args); err != nil {
		return err
	}
	anySeq := *seq.cont || *seq.abort || *seq.skip || *seq.quit
	if fs.NArg() < 1 && !anySeq {
		return errors.New("usage: cairn cherry-pick <commit> [branch]")
	}
	open := openRepoSynced
	if *seq.abort {
		open = openRepo
	}
	r, err := open(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	specs := fs.Args()
	branch := *explicit
	if branch == "" && len(specs) > 1 && r.IsExpressed(specs[len(specs)-1]) {
		branch, specs = specs[len(specs)-1], specs[:len(specs)-1]
	}
	if branch, err = targetBranch(r, branch); err != nil {
		return mapErr(err)
	}
	if handled, serr := seq.run(r, branch, "cherry-pick"); handled {
		return serr
	}
	var commits []string
	for _, arg := range specs {
		shas, rerr := r.ResolveRange(branch, arg)
		if rerr != nil {
			return mapErr(fmt.Errorf("worktree.CherryPick: %w", rerr))
		}
		commits = append(commits, shas...)
	}
	for i, sha := range commits {
		res, perr := r.CherryPickOpts(branch, sha, worktree.PickOpts{NoCommit: noCommit, AppendX: *appendX})
		if perr != nil {
			return mapErr(perr)
		}
		if len(res.Conflicts) > 0 {
			paths := make([]string, 0, len(res.Conflicts))
			for _, c := range res.Conflicts {
				paths = append(paths, c.Path)
			}
			msg := fmt.Sprintf("cherry-pick: %d conflict(s) in: %s — resolve, then commit", len(res.Conflicts), strings.Join(paths, ", "))
			if rest := commits[i+1:]; len(rest) > 0 {
				msg += fmt.Sprintf(" (not yet picked: %s)", strings.Join(shortShas(rest), " "))
			}
			fmt.Fprintln(os.Stderr, msg)
			return errConflicts
		}
		if noCommit {
			fmt.Fprintf(os.Stderr, "cairn: applied %s to the working change of %s (not sealed) — 'cairn commit' seals it with the picked message\n", shortSha(sha), branch)
			continue
		}
		fmt.Println(res.Commit)
	}
	if !noCommit {
		fmt.Fprintln(os.Stderr, "cairn: cherry-picked")
	}
	return nil
}

func shortShas(shas []string) []string {
	out := make([]string, len(shas))
	for i, s := range shas {
		out[i] = shortSha(s)
	}
	return out
}
