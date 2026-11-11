package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

// addNote is what `cairn add` says before succeeding.
const addNote = `there is no staging area — your edits on disk already ARE the working change, so there is nothing to add; ` +
	`seal them with "cairn commit -m <msg>", or only some with "cairn commit -m <msg> -- <path>..."`

// cmdAdd accepts `git add <anything>` and does nothing, successfully: scripts
// run `git add -A && git commit -m …`, and in cairn the first half is already
// true. The one form it refuses is interactive staging, which has a different
// cairn answer (name the paths to commit).
func cmdAdd(args []string) error {
	for _, a := range args {
		if a == "--" {
			break
		}
		switch a {
		case "-p", "--patch", "-i", "--interactive", "-e", "--edit":
			return errors.New(`cairn has no staging area, so there are no hunks to stage — commit only some of your edits with ` +
				`"cairn commit -m <msg> -- <path>..." (whole paths; the rest stays in the working change)`)
		}
	}
	fmt.Fprintln(os.Stderr, "cairn: "+addNote)
	return nil
}

// cmdRm removes files from the line's working change and folder (git rm).
//
//	cairn rm [-r] [-f] [-n] [-q] [--ignore-unmatch] [--branch <line>] [--] <path>...
//
// It prints `rm '<path>'` per file, like git. A directory needs -r; a file
// with un-sealed changes needs -f. `cairn undo` brings the files back.
func cmdRm(args []string) error {
	before, pathsAfter, _ := splitDashDash(args)
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	explicit := branchFlag(fs)
	recursive := fs.Bool("r", false, "remove directories recursively")
	var force, dry, quiet bool
	fs.BoolVar(&force, "f", false, "remove even with un-sealed changes")
	fs.BoolVar(&force, "force", false, "remove even with un-sealed changes")
	fs.BoolVar(&dry, "n", false, "show what would be removed")
	fs.BoolVar(&dry, "dry-run", false, "show what would be removed")
	fs.BoolVar(&quiet, "q", false, "do not list removed files")
	fs.BoolVar(&quiet, "quiet", false, "do not list removed files")
	cached := fs.Bool("cached", false, "not supported: cairn has no index")
	ignoreUnmatch := fs.Bool("ignore-unmatch", false, "exit 0 even if nothing matched")
	if err := parseGitArgs(fs, before); err != nil {
		return err
	}
	if *cached {
		return errors.New(`rm --cached: cairn has no index to remove a file from while keeping it on disk — a file in the folder is tracked unless ` +
			`it is ignored; to stop tracking one, move it aside, "cairn commit" the removal, add it to .cairnignore (or .gitignore), and move it back`)
	}
	specs := append(fs.Args(), pathsAfter...)
	if len(specs) == 0 {
		return errUsage("cairn rm [-r] [-f] [-n] <path>...")
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
	removed, err := r.Rm(branch, specs, *recursive, force, *ignoreUnmatch, dry)
	if err != nil {
		return mapErr(err)
	}
	if !quiet {
		for _, p := range removed {
			fmt.Printf("rm '%s'\n", p)
		}
	}
	return nil
}

// cmdMv renames files or directories inside the line (git mv).
//
//	cairn mv [-f] [-n] [-v] [--branch <line>] <source>... <destination>
func cmdMv(args []string) error {
	before, pathsAfter, _ := splitDashDash(args)
	fs := flag.NewFlagSet("mv", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	explicit := branchFlag(fs)
	var force, dry, verbose bool
	fs.BoolVar(&force, "f", false, "overwrite an existing destination")
	fs.BoolVar(&force, "force", false, "overwrite an existing destination")
	fs.BoolVar(&dry, "n", false, "show what would be moved")
	fs.BoolVar(&dry, "dry-run", false, "show what would be moved")
	fs.BoolVar(&verbose, "v", false, "list each rename")
	fs.BoolVar(&verbose, "verbose", false, "list each rename")
	fs.Bool("k", false, "accepted for git compatibility (no-op)")
	if err := parseGitArgs(fs, before); err != nil {
		return err
	}
	specs := append(fs.Args(), pathsAfter...)
	if len(specs) < 2 {
		return errUsage("cairn mv [-f] [-n] <source>... <destination>")
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
	moves, err := r.Mv(branch, specs[:len(specs)-1], specs[len(specs)-1], force, dry)
	if err != nil {
		return mapErr(err)
	}
	if dry || verbose {
		for _, m := range moves {
			fmt.Printf("Renaming %s to %s\n", m.From, m.To)
		}
	}
	return nil
}

// cmdClean removes IGNORED files from the line's folder.
//
//	cairn clean [-n|--dry-run] [-f] [-d] [-x|-X] [--branch <line>] [--] [<path>...]
//
// git's clean removes untracked files. cairn has no untracked files: a new
// file that is not ignored is already part of the working change — it is the
// source you just wrote, not junk — and discarding THAT is `cairn restore` /
// `cairn reset --hard`, which are undoable. What is left for clean is exactly
// what is outside cairn's model: files matched by .gitignore/.cairnignore.
//
//   - -x or -X selects them (the two are the same here); without either there
//     is nothing clean can remove, and it says so (exit 0).
//   - -d also removes wholly-ignored directories (never one holding a nested
//     repository).
//   - -f is required to delete, as in git; -n previews instead.
//
// Ignored files were never in cairn's store, so — unlike everything else
// here — a clean cannot be undone.
func cmdClean(args []string) error {
	before, pathsAfter, _ := splitDashDash(args)
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	explicit := branchFlag(fs)
	var dry, force, quiet bool
	fs.BoolVar(&dry, "n", false, "show what would be removed")
	fs.BoolVar(&dry, "dry-run", false, "show what would be removed")
	fs.BoolVar(&force, "f", false, "actually remove")
	fs.BoolVar(&force, "force", false, "actually remove")
	fs.BoolVar(&quiet, "q", false, "do not list removed paths")
	fs.BoolVar(&quiet, "quiet", false, "do not list removed paths")
	dirs := fs.Bool("d", false, "also remove wholly-ignored directories")
	x := fs.Bool("x", false, "remove ignored files")
	bigX := fs.Bool("X", false, "remove only ignored files (the same as -x in cairn)")
	interactive := fs.Bool("i", false, "not supported")
	if err := parseGitArgs(fs, before); err != nil {
		return err
	}
	if *interactive {
		return errors.New("clean -i: cairn has no interactive clean; preview with -n")
	}
	specs := append(fs.Args(), pathsAfter...)
	if !*x && !*bigX {
		fmt.Fprintln(os.Stderr, `cairn: nothing to clean — cairn has no untracked files: a new file that is not ignored is already part of the working change `+
			`("cairn restore <path>" or "cairn reset --hard" discards those, undoably). "cairn clean -fdx" removes IGNORED files (build output, caches)`)
		return nil
	}
	if !force && !dry {
		return errors.New("clean: refusing to remove files without -f (preview with -n)")
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
	if len(specs) > 0 {
		if specs, err = treePathspecs(r, branch, specs); err != nil {
			return err
		}
	}
	removed, err := r.Clean(branch, specs, *dirs, dry)
	if err != nil {
		return mapErr(err)
	}
	if quiet {
		return nil
	}
	verb := "Removing"
	if dry {
		verb = "Would remove"
	}
	for _, p := range removed {
		fmt.Printf("%s %s\n", verb, p)
	}
	if len(removed) == 0 {
		hint := ""
		if !*dirs {
			hint = " (pass -d to include ignored directories)"
		}
		fmt.Fprintf(os.Stderr, "cairn: no ignored files to clean on %s%s\n", branch, hint)
	}
	return nil
}
