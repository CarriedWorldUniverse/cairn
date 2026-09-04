package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

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
// An optional trailing positional selects the branch (default: structural root).
func cmdStashPush(args []string) error {
	// Strip a leading literal "push" sub-verb if present.
	if len(args) > 0 && args[0] == "push" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("stash", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	msg := fs.String("m", "", "stash message")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	var branch string
	if fs.NArg() > 0 {
		branch = fs.Arg(0)
	} else {
		branch, err = r.DefaultBranch()
		if err != nil {
			return mapErr(err)
		}
	}
	if err := r.Stash(branch, *msg); err != nil {
		// Nothing to stash is a no-op, not a failure (matches git's "No local
		// changes to save" → exit 0), so `stash` is script-safe.
		if errors.Is(err, change.ErrNothingToStash) {
			fmt.Fprintf(os.Stderr, "cairn: no working changes to stash on %s\n", branch)
			return nil
		}
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: shelved working changes; folder reset to %s's sealed state\n", branch)
	return nil
}

// cmdStashPop restores a stash entry onto the working branch. The optional
// positionals select the branch (default: the folder you stand in, else the
// structural root) and the stash id from `stash list` (default: most recent),
// in either order — an id is what `stash drop` accepts, so `stash pop` takes
// the same thing.
func cmdStashPop(args []string) error {
	fs := flag.NewFlagSet("stash pop", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 2 {
		return fmt.Errorf("usage: cairn stash pop [branch] [id]")
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	// Classify each positional: an expressed branch name wins (so a line really
	// named "5" is still reachable), otherwise a bare integer is a stash id.
	var (
		branch  string
		stashID int64
	)
	for _, arg := range fs.Args() {
		if !r.IsExpressed(arg) {
			if id, perr := strconv.ParseInt(arg, 10, 64); perr == nil {
				if stashID != 0 {
					return fmt.Errorf("usage: cairn stash pop [branch] [id]")
				}
				stashID = id
				continue
			}
		}
		if branch != "" {
			return fmt.Errorf("usage: cairn stash pop [branch] [id]")
		}
		branch = arg
	}
	if branch == "" {
		var berr error
		branch, berr = r.DefaultBranch()
		if berr != nil {
			return mapErr(berr)
		}
	}
	if err := r.StashPop(branch, stashID); err != nil {
		return mapErr(err)
	}
	if stashID != 0 {
		fmt.Fprintf(os.Stderr, "cairn: restored stash %d\n", stashID)
	} else {
		fmt.Fprintln(os.Stderr, "cairn: restored the most recent stash")
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
	if err := parseArgs(fs, args); err != nil {
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
