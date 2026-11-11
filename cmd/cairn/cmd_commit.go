package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

// gitAuthorRe matches git's `--author="Name <email>"` form. A bare name (no
// angle brackets) is cairn's own, older meaning of --author — see cmdCommit.
var gitAuthorRe = regexp.MustCompile(`^\s*(.*?)\s*<([^<>]*)>\s*$`)

// cmdCommit seals the working change of a line.
//
//	cairn commit [branch] [-m msg]...            seal the whole working change
//	cairn commit [branch] [-m msg] -- <path>...  seal ONLY those paths; every
//	                                             other edit stays in the working change
//	cairn commit [branch] --amend [-m msg]       fold the working change into the
//	                                             previous sealed commit
//
// The first positional is the branch when it names an expressed line; any
// other positional (and everything after "--") is a pathspec. The branch is
// inferred from the folder the command is run in when it is not given.
//
// --author has two meanings, told apart by shape. `--author="Name <email>"`
// is git's: the AUTHOR of this one commit, the committer and the operation's
// actor staying the configured identity. A bare `--author name` is cairn's
// original flag, shared by every repo subcommand: the identity NAME this
// invocation acts (and commits) as — unchanged, so existing callers keep
// working.
func cmdCommit(args []string) error {
	before, pathsAfter, hasDD := splitDashDash(args)
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	var msgs stringList
	fs.Var(&msgs, "m", "commit message (repeat for more paragraphs)")
	fs.Var(&msgs, "message", "commit message (repeat for more paragraphs)")
	var file string
	fs.StringVar(&file, "F", "", "read the commit message from a file (- for stdin)")
	fs.StringVar(&file, "file", "", "read the commit message from a file (- for stdin)")
	amend := fs.Bool("amend", false, "fold the working change into the previous sealed commit")
	noEdit := fs.Bool("no-edit", false, "keep the message; never open an editor")
	date := fs.String("date", "", "author date for the commit")
	resetAuthor := fs.Bool("reset-author", false, "with --amend: take authorship of the amended commit")
	allowEmptyMsg := fs.Bool("allow-empty-message", false, "accept an empty message from the editor")
	// Accepted for git compatibility; no-ops in cairn's model. There is no
	// staging area, so every commit already takes all edits (-a), and a seal
	// with no edits is already allowed (--allow-empty).
	for _, name := range []string{"a", "all", "allow-empty", "q", "quiet", "no-verify", "n", "e", "edit", "s", "signoff", "no-gpg-sign"} {
		fs.Bool(name, false, "accepted for git compatibility (no-op)")
	}
	if err := parseGitArgs(fs, before); err != nil {
		return err
	}
	// git's --author="Name <email>" vs cairn's --author <identity name>.
	var sig *change.Signature
	openAs := *author
	if m := gitAuthorRe.FindStringSubmatch(*author); m != nil {
		if m[1] == "" {
			return fmt.Errorf(`--author %q: expected "Name <email>"`, *author)
		}
		sig = &change.Signature{Name: m[1], Email: m[2]}
		openAs = defaultAuthor()
	}
	if *date != "" {
		when, err := parseGitDate(*date)
		if err != nil {
			return err
		}
		if sig == nil {
			sig = &change.Signature{}
		}
		sig.When = when
	}
	if len(msgs) > 0 && file != "" {
		return errors.New("options -m and -F cannot be used together")
	}
	r, err := openRepo(*repo, openAs)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()

	// Positionals: [branch] [path...]. An expressed line's name wins the first
	// slot; "--" settles any ambiguity with a file of the same name.
	pos := fs.Args()
	var branch string
	switch b, inFolder := r.CWDBranch(); {
	case len(pos) > 0 && r.IsExpressed(pos[0]):
		branch, pos = pos[0], pos[1:]
	case inFolder:
		branch = b
	case len(pos) == 1 && !hasDD:
		// Outside any line's folder a lone positional can only be meant as the
		// branch; let the seal report that it is not expressed.
		branch, pos = pos[0], nil
	default:
		return errors.New("branch required (or run from inside a branch folder)")
	}
	specs := append(pos, pathsAfter...)
	if len(specs) > 0 {
		if specs, err = treePathspecs(r, branch, specs); err != nil {
			return err
		}
	}
	if *amend && len(specs) > 0 {
		return errors.New("commit --amend folds the whole working change into the previous commit; it cannot be combined with paths (commit the paths first, or amend everything)")
	}

	// The message: -m / -F, else what a stopped revert or cherry-pick left,
	// else the editor on a terminal, else (scripts) the historical default.
	message, haveMsg := "", false
	switch {
	case len(msgs) > 0:
		message, haveMsg = strings.Join(msgs, "\n\n"), true
	case file != "":
		data, rerr := readFileOrStdin(file)
		if rerr != nil {
			return rerr
		}
		message, haveMsg = strings.TrimRight(string(data), " \t\r\n"), true
	}
	if !haveMsg && !*amend {
		if pending := r.PendingMessage(branch); pending != "" {
			message, haveMsg = pending, true
		}
	}
	if !haveMsg && !*noEdit && stdinIsTerminal() {
		edited, used, eerr := editCommitMessage(r, branch, *amend)
		if eerr != nil {
			return eerr
		}
		if used {
			if edited == "" && !*allowEmptyMsg {
				return errors.New("aborting commit due to empty commit message")
			}
			message, haveMsg = edited, true
		}
	}

	ensureIdentity(r)

	if *amend {
		var msgPtr *string
		if haveMsg {
			msgPtr = &message
		}
		sealed, forceRemote, aerr := r.Amend(branch, msgPtr, worktree.CommitOpts{Author: sig}, *resetAuthor)
		if aerr != nil {
			return mapErr(amendHint(aerr))
		}
		if forceRemote != "" {
			fmt.Fprintf(os.Stderr, "cairn: the commit you amended is already on %s — the next 'cairn push %s %s' needs --force\n", forceRemote, forceRemote, branch)
		}
		fmt.Println(sealed)
		return nil
	}
	if len(specs) > 0 {
		sealed, perr := r.CommitPaths(branch, message, specs, worktree.CommitOpts{Author: sig})
		if perr != nil {
			return mapErr(perr)
		}
		r.ClearPendingMessage(branch)
		fmt.Println(sealed)
		return nil
	}

	res, err := r.CommitWith(branch, message, worktree.CommitOpts{Author: sig})
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
	if res.Rebase != nil && res.Rebase.Status != "" {
		printRebase(*res.Rebase)
		if res.Rebase.Status == "stopped" {
			fmt.Println(res.HeadCommit)
			return errConflicts
		}
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

// amendHint is where `commit --amend` and `reset <rev>` pass their
// history-edit refusals through. The shared guard (change.guardRewrite) already
// names what blocks the edit and what to run instead, so there is nothing to
// add; it stays as the one seam should the CLI need to.
func amendHint(err error) error { return err }

// readFileOrStdin reads path, or stdin when path is "-".
func readFileOrStdin(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(stdinReader)
	}
	return os.ReadFile(path)
}

// parseGitDate parses the date forms `git commit --date` is commonly given:
// RFC 3339 / ISO 8601, RFC 2822, git's internal "<unix> <±hhmm>", "@<unix>",
// and plain "YYYY-MM-DD[ HH:MM[:SS]]" (local time).
func parseGitDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "@"); ok {
		s = rest
	}
	if f := strings.Fields(s); len(f) >= 1 && len(f) <= 2 {
		if secs, err := strconv.ParseInt(f[0], 10, 64); err == nil && len(f[0]) >= 8 {
			t := time.Unix(secs, 0)
			if len(f) == 2 {
				if z, zerr := time.Parse("-0700", f[1]); zerr == nil {
					return t.In(z.Location()), nil
				}
				return time.Time{}, fmt.Errorf("--date %q: bad timezone", s)
			}
			return t, nil
		}
	}
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05", time.RFC1123Z, time.RFC1123,
		"Mon, 2 Jan 2006 15:04:05 -0700", "Mon Jan 2 15:04:05 2006 -0700",
		"2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02",
	} {
		if strings.Contains(layout, "-0700") || strings.Contains(layout, "Z07:00") || layout == time.RFC1123 {
			if t, err := time.Parse(layout, s); err == nil {
				return t, nil
			}
			continue
		}
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--date %q: unrecognized date (use e.g. 2026-01-31T12:00:00+13:00)", s)
}

// editCommitMessage opens the operator's editor on a message template and
// returns what they wrote, comment lines stripped. used is false when the
// editor should not run at all — a stopped rebase is being resolved, whose
// commit keeps its own message.
func editCommitMessage(r *worktree.Repo, branch string, amend bool) (msg string, used bool, err error) {
	st, err := r.Status(branch)
	if err != nil {
		return "", false, mapErr(err)
	}
	if st.Rebase != "" {
		return "", false, nil
	}
	var b strings.Builder
	if amend {
		if prev, perr := r.TopMessage(branch); perr == nil && prev != "" {
			b.WriteString(prev)
		}
	}
	b.WriteString("\n# Describe this commit on " + branch + ". Lines starting with '#' are ignored;\n# an empty message aborts the commit.\n#\n")
	if n := len(st.Modified) + len(st.Added) + len(st.Deleted); n == 0 {
		b.WriteString("# No changes in the working change.\n")
	} else {
		b.WriteString("# Changes to be sealed:\n")
		for _, p := range st.Modified {
			b.WriteString("#\tmodified: " + p + "\n")
		}
		for _, p := range st.Added {
			b.WriteString("#\tnew file: " + p + "\n")
		}
		for _, p := range st.Deleted {
			b.WriteString("#\tdeleted:  " + p + "\n")
		}
	}
	path := filepath.Join(r.Root(), ".cairn", "COMMIT_EDITMSG")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", false, err
	}
	if err := runEditor(path); err != nil {
		return "", false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	return stripMessageComments(string(data)), true, nil
}

// stripMessageComments drops '#' comment lines and surrounding blank lines
// from an edited commit message (git's default cleanup).
func stripMessageComments(s string) string {
	var keep []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		keep = append(keep, strings.TrimRight(line, " \t"))
	}
	return strings.Trim(strings.Join(keep, "\n"), "\n")
}

// commitEditor picks the editor: $CAIRN_EDITOR, $GIT_EDITOR, $VISUAL, $EDITOR,
// then vi.
func commitEditor() string {
	return firstNonEmptyStr(os.Getenv("CAIRN_EDITOR"), os.Getenv("GIT_EDITOR"), os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
}

// runEditor runs the editor on path, attached to the terminal. The editor
// setting may carry arguments ("code --wait"), so it goes through the shell,
// as git does.
func runEditor(path string) error {
	editor := commitEditor()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		parts := strings.Fields(editor)
		cmd = exec.Command(parts[0], append(parts[1:], path)...)
	} else {
		cmd = exec.Command("sh", "-c", editor+` "$@"`, "sh", path)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %q failed: %w (pass -m <msg> instead, or set $CAIRN_EDITOR)", editor, err)
	}
	return nil
}
