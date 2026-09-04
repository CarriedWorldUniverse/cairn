package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// cmdPrivate withholds a path from every push, or (with the `ls` subcommand)
// lists withheld paths. Usage:
//
//	cairn private <path> [--shape-only]
//	cairn private ls
//
// Withheld content is tracked locally (readable on disk, in local commits) but is
// stripped from every push — omit by default (path gone entirely), or
// --shape-only to keep the path with placeholder bytes.
func cmdPrivate(args []string) error {
	fs := flag.NewFlagSet("private", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	shapeOnly := fs.Bool("shape-only", false, "keep the path with placeholder bytes instead of removing it entirely")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn private <path> [--shape-only]  |  cairn private ls")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if fs.Arg(0) == "ls" {
		entries, err := r.ListPrivate()
		if err != nil {
			return mapErr(err)
		}
		for _, e := range entries {
			fmt.Printf("%s\t%s\n", e.Path, e.Mode)
		}
		return nil
	}
	path := fs.Arg(0)
	if err := r.MarkPrivate(path, *shapeOnly); err != nil {
		return mapErr(err)
	}
	mode := "omit"
	if *shapeOnly {
		mode = "shape-only"
	}
	fmt.Fprintf(os.Stderr, "cairn: withholding %s from pushes (%s)\n", path, mode)
	// Footgun guard: if this path is already on a remote, withholding it does NOT
	// remove the copy that's already out there — the secret is compromised.
	if remotes, rerr := r.PathOnRemote(path); rerr == nil && len(remotes) > 0 {
		fmt.Fprintf(os.Stderr,
			"cairn: WARNING — %s is already present on %s. Withholding stops FUTURE pushes\n"+
				"  from carrying it, but the copy already on the remote is NOT removed (it lingers as\n"+
				"  a recoverable object and in any existing clones/forks). Rotate the secret.\n",
			path, strings.Join(remotes, ", "))
	}
	return nil
}

// cmdEmbargo holds a commit (and everything after it) out of the public
// projection, or lists embargoed commits. Distinct from `private` (secrets that
// are never pushed): an embargoed commit is content you DO intend to distribute,
// just gated and not-yet-public — pushed to a git remote it is held back; served
// from a cairn server it goes to authorized recipients now (gated), the patch-gap.
//
//	cairn embargo <commit>
//	cairn embargo ls
func cmdEmbargo(args []string) error {
	fs := flag.NewFlagSet("embargo", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn embargo <commit>  |  cairn embargo ls")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if fs.Arg(0) == "ls" {
		shas, err := r.ListEmbargo()
		if err != nil {
			return mapErr(err)
		}
		for _, s := range shas {
			fmt.Println(s)
		}
		return nil
	}
	sha, err := r.MarkEmbargo(fs.Arg(0))
	if err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: embargoed %s — held out of the public projection until 'cairn disclose %s'\n", sha[:min(8, len(sha))], fs.Arg(0))
	return nil
}

// cmdDisclose makes something public again: it lifts an embargo if the argument
// is an embargoed commit, otherwise it stops withholding a private path.
//
//	cairn disclose <path|commit>
func cmdDisclose(args []string) error {
	fs := flag.NewFlagSet("disclose", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn disclose <path|commit>")
	}
	arg := fs.Arg(0)
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	// An embargoed commit takes precedence; otherwise treat the arg as a private path.
	if handled, err := r.DiscloseCommit(arg); err != nil {
		return mapErr(err)
	} else if handled {
		fmt.Fprintf(os.Stderr, "cairn: disclosed embargo on %s\n", arg)
		fmt.Fprintln(os.Stderr, "cairn: it becomes public on your next push (the push is what publishes it)")
		return nil
	}
	if err := r.UnmarkPrivate(arg); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: disclosed %s (no longer withheld)\n", arg)
	return nil
}
