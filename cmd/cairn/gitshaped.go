package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// Git-shaped command surface (#139, lean-design-standard P6 "corpus
// conservatism"). Both models and human muscle memory drive cairn through
// git's grooves. Where cairn's semantics MATCH git's, the git spelling is
// accepted as a pure alias — the same cmd func, the same args, no behavior of
// its own. Where they do NOT match, cairn refuses to fake the verb and instead
// teaches the translation at the exact moment the reflex misfires, which is
// cheaper and far more reliable than documentation that may not be in context.
//
// The guardrail: an alias must stay a byte-for-byte synonym of its canonical
// verb. The moment `merge` does anything `fold` does not, there are two
// surfaces to maintain and the win inverts — so aliases dispatch straight into
// the canonical cmd func and never grow a flag or a branch of their own.

// gitOnlyVerbs maps a git subcommand with NO cairn equivalent to the
// correction for it. Keep each one to a single actionable sentence naming the
// cairn command to run instead: it is printed INSTEAD of the usage wall, so it
// is the only thing the reader sees.
var gitOnlyVerbs = map[string]string{
	"rebase": `cairn never needs a rebase — every "cairn commit" reconciles the line against ` +
		`the latest parent, so a line cannot drift behind it`,
	"notes": `cairn has no notes refs — put the information in the commit message ("cairn reword <commit> <message>") ` +
		`or in an annotated tag ("cairn tag -a -m <msg> <name> <commit>")`,
	"bundle": `cairn has no bundle files — a local path is a remote: "cairn remote add <name> <path>" then ` +
		`"cairn push"/"cairn fetch" exchange history; "cairn archive <rev>" writes one commit's tree`,
	"submodule": `cairn does not manage submodules — a gitlink entry is carried through unchanged, but each nested ` +
		`repo is cloned and updated on its own ("cairn clone <url> <dir>")`,
	"sparse-checkout": `cairn materializes whole lines — there is no sparse checkout; work in the sub-directory you need ` +
		`inside the line's folder, and use "cairn ls-tree"/"cairn show <rev>:<path>" to read the rest`,
	"maintenance": `use "cairn gc" (repack, vacuum, prune) — scheduling it (cron/systemd timer) is up to you`,
	"range-diff":  `compare two ranges with "cairn log <a>..<b>" and "cairn diff <a>...<b>" — cairn has no range-diff`,
	"verify-commit": `cairn does not sign or verify commits (no GPG), so there is no signature to check; ` +
		`"cairn show <commit>" prints what the commit holds`,
	"verify-tag": `cairn does not sign or verify tags (no GPG), so there is no signature to check; ` +
		`"cairn tag -l -n <name>" shows an annotated tag's message`,
	"update-ref": `refs are moved by the verbs that mean it — "cairn tag [-f] <name> <rev>", "cairn reset <rev>" ` +
		`(move the line's tip), "cairn branch <name> <start>" / "cairn branch -d <name>"`,
	"worktree": `every expressed line IS a folder — "cairn express <branch>" materializes one and ` +
		`"cairn ls" lists them; there is no separate worktree command`,
}

// checkoutHint is shared by `checkout <branch>` and `switch <branch>`: both ask
// to move the working copy onto an existing line, which in cairn is a cd.
const checkoutHint = `lines are folders in cairn — "cd <repo>/%s/" selects that line ` +
	`(any command run inside a line's folder acts on it); "cairn express %s" materializes the folder first if it has none`

// runGitShaped handles a subcommand that is a git spelling rather than a cairn
// verb. handled is false for anything cairn's own dispatcher should keep
// owning, so the caller falls through to its usual unknown-subcommand path.
func runGitShaped(sub string, rest []string) (handled bool, err error) {
	switch sub {
	// ── Tier 1: aliases where the semantics genuinely match ──────────────────
	case "merge":
		// git merge <branch> == cairn fold <branch>: integrate a line into the
		// one it forked from. Every model reaches for "merge" first.
		return true, cmdFold(rest)
	case "branch":
		// A real verb now (cmd_branch.go): `branch <name>` = express, plus
		// listing, delete, rename and the graph filters.
		return true, cmdBranch(rest)
	case "checkout", "switch":
		return true, gitCheckout(sub, rest)
	}

	// ── Tier 2: git verbs cairn deliberately does not have ───────────────────
	if hint, ok := gitOnlyVerbs[sub]; ok {
		return true, errors.New(hint)
	}
	return false, nil
}

// gitCheckout splits the two things `git checkout`/`git switch` do. The
// create-a-branch form is an alias for express; the move-onto-a-branch form has
// no cairn equivalent (lines are folders, so it is a cd) and is corrected.
func gitCheckout(sub string, rest []string) error {
	// `git checkout [<rev>] -- <path>...` discards edits to paths: that is
	// restore, whose semantics match exactly, so it is an alias.
	if sub == "checkout" {
		if before, paths, found := splitDashDash(rest); found && len(paths) > 0 {
			return gitCheckoutPaths(before, paths)
		}
	}
	create := "b"
	if sub == "switch" {
		create = "c"
	}
	for _, a := range rest {
		if a == "--detach" || (sub == "switch" && a == "-d") {
			return errors.New(`lines are folders — there is no detached HEAD. "cairn show <rev>" / "cairn show <rev>:<path>" read an old commit, ` +
				`"cairn archive <rev>" exports it, and "cairn express <name> --from <line>" starts a line to work from`)
		}
	}
	for _, a := range rest {
		if a == "-" {
			return errors.New(`cairn does not remember a "previous" line — lines are folders, so "cd -" (or cd back to <repo>/<line>/) returns to it; "cairn ls" lists the expressed lines`)
		}
	}
	args, flags, err := gitPositionals(sub, rest, create)
	if err != nil {
		return err
	}
	if flags[create] {
		if len(args) == 0 {
			return fmt.Errorf("%s -%s: branch required", sub, create)
		}
		// `git checkout -b <name> <start-point>` names a parent. express spells
		// that --from, and SILENTLY dropping it would fork off the wrong line —
		// so translate explicitly rather than guess.
		if len(args) > 1 {
			return fmt.Errorf(`to fork %s off %s, run "cairn express %s --from %s"`, args[0], args[1], args[0], args[1])
		}
		return cmdExpress(withoutToken(rest, "-"+create))
	}
	target := "<branch>"
	if len(args) > 0 {
		target = args[0]
	}
	return fmt.Errorf(checkoutHint, target, target)
}

// gitPositionals reports which of rest's tokens are POSITIONAL, and which of
// the named bool flags were given. It exists because a git spelling has to be
// told apart from the cairn flags around it: in `checkout --repo /x -b feat`,
// a naive scan reads "/x" as the branch and misses "-b" entirely (the first
// version of this file did exactly that).
//
// It parses against express/fold's own flag surface purely to SEE the split —
// the alias still dispatches the ORIGINAL args into the canonical cmd func, so
// this never becomes a second place that defines what a flag means.
func gitPositionals(name string, rest []string, boolFlags ...string) ([]string, map[string]bool, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	repoFlags(fs)
	fs.String("from", "", "parent branch to fork from")
	fs.Bool("force", false, "discard un-sealed work")
	given := make(map[string]*bool, len(boolFlags))
	for _, b := range boolFlags {
		given[b] = fs.Bool(b, false, "create the branch")
	}
	if err := parseArgs(fs, rest); err != nil {
		return nil, nil, err
	}
	out := make(map[string]bool, len(given))
	for k, v := range given {
		out[k] = *v
	}
	return fs.Args(), out, nil
}

// withoutToken returns args with the first exact occurrence of tok removed —
// used to hand express the git invocation minus its create flag.
func withoutToken(args []string, tok string) []string {
	out := make([]string, 0, len(args))
	dropped := false
	for _, a := range args {
		if !dropped && a == tok {
			dropped = true
			continue
		}
		out = append(out, a)
	}
	return out
}

// gitCheckoutPaths translates `checkout [flags] [<rev>] -- <path>...` into
// `restore [flags] [--source=<rev>] -- <path>...`. The flags before "--" are
// parsed only to find the optional <rev>; restore receives them untouched.
func gitCheckoutPaths(before, paths []string) error {
	before, err := checkoutFlagsForRestore(before)
	if err != nil {
		return err
	}
	args, _, err := gitPositionals("checkout", before)
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return fmt.Errorf("checkout: expected at most one <rev> before --, got %d", len(args))
	}
	out := before
	if len(args) == 1 {
		out = append(withoutToken(before, args[0]), "--source="+args[0])
	}
	return cmdRestore(append(append(out, "--"), paths...))
}

// checkoutFlagsForRestore drops the checkout flags that mean nothing to a
// path restore (-f/--force, -q/--quiet, the progress/overlay/submodule
// switches — restore always overwrites, quietly) and refuses every other flag
// with the cairn spelling, instead of letting a raw flag error through.
func checkoutFlagsForRestore(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if len(a) < 2 || a[0] != '-' {
			out = append(out, a) // <rev>, or the value of --repo/--author
			continue
		}
		name := strings.TrimLeft(a, "-")
		if k := strings.IndexByte(name, '='); k >= 0 {
			name = name[:k]
		}
		switch {
		case name == "repo" || name == "author":
			out = append(out, a)
		case name == "force" || name == "quiet" || name == "progress" || name == "no-progress" ||
			name == "overlay" || name == "no-overlay" || name == "guess" || name == "no-guess" ||
			name == "recurse-submodules" || name == "no-recurse-submodules":
			continue
		case a[1] != '-' && strings.Trim(name, "fq") == "":
			continue // -f, -q, -fq, ...
		case name == "ours" || name == "theirs" || name == "m" || name == "merge" || name == "conflict":
			return nil, fmt.Errorf(`checkout %s: a conflicted path is settled by editing the file and running "cairn resolve <branch> <path>"`, a)
		default:
			return nil, fmt.Errorf(`checkout %s: not meaningful with paths; "cairn restore [--source <rev>] -- <path>..." restores files, "cairn express <name>" creates a line`, a)
		}
	}
	return out, nil
}
