package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/gitpatch"
)

// readPatchInputs concatenates the named patch files; no names, or "-", reads
// stdin.
func readPatchInputs(names []string) ([]byte, error) {
	if len(names) == 0 {
		names = []string{"-"}
	}
	var all []byte
	for _, n := range names {
		data, err := readFileOrStdin(n)
		if err != nil {
			return nil, err
		}
		all = append(all, data...)
		if len(data) > 0 && data[len(data)-1] != '\n' {
			all = append(all, '\n')
		}
	}
	return all, nil
}

// cmdApply applies a unified diff to the line's working change (git apply).
//
//	cairn apply [--check] [-R] [--stat] [-p<n>] [--branch <line>] [<patch>...|-]
//
// Like git it is all-or-nothing and strict about context. The result lands in
// the working change (and the folder); `cairn undo` reverts it.
func cmdApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	explicit := branchFlag(fs)
	check := fs.Bool("check", false, "only verify the patch applies")
	var reverse bool
	fs.BoolVar(&reverse, "R", false, "apply the patch in reverse")
	fs.BoolVar(&reverse, "reverse", false, "apply the patch in reverse")
	stat := fs.Bool("stat", false, "print a diffstat instead of applying")
	numstat := fs.Bool("numstat", false, "print added/removed counts instead of applying")
	strip := fs.Int("p", 1, "strip <n> leading path components")
	cached := fs.Bool("cached", false, "not supported: cairn has no index")
	threeWay := fs.Bool("3way", false, "not supported")
	fs.BoolVar(threeWay, "3", false, "not supported")
	fs.String("whitespace", "", "accepted for git compatibility (ignored)")
	for _, name := range []string{"index", "v", "verbose", "q", "quiet", "summary", "allow-empty", "apply"} {
		fs.Bool(name, false, "accepted for git compatibility (no-op)")
	}
	if err := parseGitArgs(fs, args); err != nil {
		return err
	}
	if *cached {
		return errors.New("apply --cached: cairn has no index; plain 'cairn apply' patches the working change")
	}
	if *threeWay {
		return errors.New("apply --3way is not supported: the patch must apply exactly (for a three-way pick of a commit use 'cairn cherry-pick')")
	}
	data, err := readPatchInputs(fs.Args())
	if err != nil {
		return err
	}
	files, err := gitpatch.Parse(data, *strip)
	if err != nil {
		return fmt.Errorf("apply: %w", err)
	}
	if len(files) == 0 {
		return errors.New("apply: no valid patches in input")
	}
	if reverse {
		// Undo the last file first, as git does.
		rev := make([]gitpatch.File, 0, len(files))
		for i := len(files) - 1; i >= 0; i-- {
			rev = append(rev, files[i].Reverse())
		}
		files = rev
	}
	if *stat || *numstat {
		printPatchStat(files, *numstat)
		if !*check {
			return nil
		}
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
	if _, err := r.ApplyPatch(branch, files, *check); err != nil {
		return fmt.Errorf("apply: %w", mapErr(err))
	}
	return nil
}

// printPatchStat prints `git apply --stat` (or --numstat) for files.
func printPatchStat(files []gitpatch.File, numstat bool) {
	width, total, adds, dels := 0, 0, 0, 0
	for _, f := range files {
		if n := len(f.Path()); n > width {
			width = n
		}
	}
	for _, f := range files {
		a, d := f.Stat()
		if numstat {
			fmt.Printf("%d\t%d\t%s\n", a, d, f.Path())
			continue
		}
		adds, dels, total = adds+a, dels+d, total+1
		bar := strings.Repeat("+", min(a, 40)) + strings.Repeat("-", min(d, 40))
		fmt.Printf(" %-*s | %4d %s\n", width, f.Path(), a+d, bar)
	}
	if numstat {
		return
	}
	plural := func(n int, one string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, one)
		}
		return fmt.Sprintf("%d %ss", n, one)
	}
	line := " " + plural(total, "file") + " changed"
	if adds > 0 {
		line += ", " + plural(adds, "insertion") + "(+)"
	}
	if dels > 0 {
		line += ", " + plural(dels, "deletion") + "(-)"
	}
	fmt.Println(line)
}

// cmdAm applies `git format-patch` output as sealed commits, one per mail,
// keeping each author, date and message (git am).
//
//	cairn am [-p<n>] [--branch <line>] [<mbox>...|-]
//
// Each patch must apply exactly; the first that does not stops the run with
// the earlier ones sealed. There is no am session to --continue: fix the
// cause and run am again with the remaining patches (or `cairn undo`).
func cmdAm(args []string) error {
	fs := flag.NewFlagSet("am", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	explicit := branchFlag(fs)
	strip := fs.Int("p", 1, "strip <n> leading path components")
	cont := fs.Bool("continue", false, "not supported")
	abort := fs.Bool("abort", false, "not supported")
	skip := fs.Bool("skip", false, "not supported")
	for _, name := range []string{"3", "3way", "q", "quiet", "s", "signoff", "k", "keep-cr", "no-keep-cr", "u", "utf8"} {
		fs.Bool(name, false, "accepted for git compatibility (no-op)")
	}
	if err := parseGitArgs(fs, args); err != nil {
		return err
	}
	if *cont || *abort || *skip {
		return errors.New(`am keeps no session in cairn: each patch is sealed as it applies, and the run stops at the first that does not — ` +
			`"cairn undo" rewinds the last one applied; re-run "cairn am" with the patches still to apply`)
	}
	data, err := readPatchInputs(fs.Args())
	if err != nil {
		return err
	}
	mails, err := gitpatch.ParseMbox(data)
	if err != nil {
		return fmt.Errorf("am: %w", err)
	}
	if len(mails) == 0 {
		return errors.New("am: no patches in input")
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
	ensureIdentity(r)
	res, err := r.Am(branch, mails, *strip)
	for i := range res.Commits {
		fmt.Printf("Applying: %s\n", mails[i].Subject)
	}
	if err != nil {
		return mapErr(err)
	}
	if len(res.Conflicts) > 0 {
		paths := make([]string, 0, len(res.Conflicts))
		for _, c := range res.Conflicts {
			paths = append(paths, c.Path)
		}
		next := fmt.Sprintf("the patch is sealed, but it overlaps your un-sealed edits: edit the markers out and 'cairn resolve %s <path>' each", branch)
		if rest := len(mails) - len(res.Commits); rest > 0 {
			next += fmt.Sprintf("; %d patch(es) not yet applied", rest)
		}
		return reportConflicts("am", paths, next)
	}
	return nil
}
