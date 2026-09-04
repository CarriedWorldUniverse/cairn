package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// cmdUndo reverts the most recent operation, restoring every expressed branch's
// folder to the prior tip. The Phase-1 limitation (lines created by the undone
// op are not deleted) is surfaced as a note on stderr.
func cmdUndo(args []string) error {
	fs := flag.NewFlagSet("undo", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
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
	if err := parseArgs(fs, args); err != nil {
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
	if err := parseArgs(fs, args); err != nil {
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
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
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
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cairn show <commit>")
	}
	r, err := openRepo(*repo, *author)
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

// reportEdit prints the result of a history-editing operation (reword/squash/
// drop) to stdout/stderr: the new line tip on stdout on success, or a conflict
// notice on stderr (exit 2) when the rebase produced conflicts.
func reportEdit(res change.CommitResult, verb string) error {
	if len(res.Conflicts) > 0 {
		paths := make([]string, 0, len(res.Conflicts))
		for _, c := range res.Conflicts {
			paths = append(paths, c.Path)
		}
		fmt.Fprintf(os.Stderr, "%s: %d rebase conflict(s) in: %s\n", verb, len(res.Conflicts), strings.Join(paths, ", "))
		return errConflicts
	}
	fmt.Println(res.HeadCommit)
	return nil
}

// cmdReword changes the message of a sealed commit.
// Usage: cairn reword [--repo dir] <commit> <message>
func cmdReword(args []string) error {
	fs := flag.NewFlagSet("reword", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return errors.New("usage: cairn reword <commit> <message>")
	}
	commit := fs.Arg(0)
	message := fs.Arg(1)
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	res, err := r.Reword(commit, message)
	if err != nil {
		return mapErr(err)
	}
	return reportEdit(res, "reword")
}

// cmdSquash folds a sealed commit into its parent.
// Usage: cairn squash [--repo dir] <commit>
func cmdSquash(args []string) error {
	fs := flag.NewFlagSet("squash", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn squash <commit>")
	}
	commit := fs.Arg(0)
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	res, err := r.Squash(commit)
	if err != nil {
		return mapErr(err)
	}
	return reportEdit(res, "squash")
}

// cmdDrop removes a sealed commit from its line.
// Usage: cairn drop [--repo dir] <commit>
func cmdDrop(args []string) error {
	fs := flag.NewFlagSet("drop", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn drop <commit>")
	}
	commit := fs.Arg(0)
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	res, err := r.Drop(commit)
	if err != nil {
		return mapErr(err)
	}
	return reportEdit(res, "drop")
}

// cmdReauthor bulk-rewrites commit author/email across the whole repo (every line,
// root included). Match the OLD identity by name and/or email glob; supply the NEW
// name and/or email. At least one match filter is required (a guard against
// retagging the entire history by accident), as is at least one replacement.
//
//	cairn reauthor --old-email '*@users.noreply.cairn' --name Jacinta --email jacinta@darksoft.co.nz
//	cairn reauthor --old-name cairn --name Jacinta --email me@x.io --dry-run
func cmdReauthor(args []string) error {
	fs := flag.NewFlagSet("reauthor", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	oldName := fs.String("old-name", "", "match commits whose author/committer name matches this glob")
	oldEmail := fs.String("old-email", "", "match commits whose author/committer email matches this glob")
	newName := fs.String("name", "", "new author/committer name (unchanged if empty)")
	newEmail := fs.String("email", "", "new author/committer email (unchanged if empty)")
	dryRun := fs.Bool("dry-run", false, "report what would change without rewriting")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if *oldName == "" && *oldEmail == "" {
		return errors.New("reauthor: specify at least one of --old-name / --old-email " +
			"(refusing to match every commit); e.g. --old-email '*@users.noreply.cairn'")
	}
	if *newName == "" && *newEmail == "" {
		return errors.New("reauthor: specify at least one of --name / --email to set")
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	res, err := r.Reauthor(change.ReauthorSpec{
		OldName:  *oldName,
		OldEmail: *oldEmail,
		NewName:  *newName,
		NewEmail: *newEmail,
		DryRun:   *dryRun,
	})
	if err != nil {
		return mapErr(err)
	}
	verb := "rewrote"
	if *dryRun {
		verb = "would rewrite"
	}
	fmt.Printf("reauthor: %s %d commit(s) matching the old identity (%d total rebuilt, incl. descendants)\n",
		verb, res.Matched, res.Rewritten)
	return nil
}

// cmdCherryPick applies the delta of a given commit onto a branch as a new
// sealed commit (original message, fresh change-id), rebasing the open working
// change on top. Conflicts are returned as data (exit 2) so the operator can
// resolve them and continue.
//
// Usage: cairn cherry-pick [--repo dir] <commit> [branch]
// branch defaults to the structural root when omitted.
func cmdCherryPick(args []string) error {
	fs := flag.NewFlagSet("cherry-pick", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn cherry-pick <commit> [branch]")
	}
	commit := fs.Arg(0)
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	var branch string
	if fs.NArg() > 1 {
		branch = fs.Arg(1)
	} else if branch, err = r.DefaultBranch(); err != nil {
		return mapErr(err)
	}
	res, err := r.CherryPick(branch, commit)
	if err != nil {
		return mapErr(err)
	}
	if len(res.Conflicts) > 0 {
		paths := make([]string, 0, len(res.Conflicts))
		for _, c := range res.Conflicts {
			paths = append(paths, c.Path)
		}
		fmt.Fprintf(os.Stderr, "cherry-pick: %d conflict(s) in: %s — resolve, then commit\n", len(res.Conflicts), strings.Join(paths, ", "))
		return errConflicts
	}
	fmt.Println(res.HeadCommit)
	fmt.Fprintln(os.Stderr, "cairn: cherry-picked")
	return nil
}

// shortSha returns the first 8 characters of a sha (or the full string if shorter).
func shortSha(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
