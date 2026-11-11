package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

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

// shortSha returns the first 8 characters of a sha (or the full string if shorter).
func shortSha(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
