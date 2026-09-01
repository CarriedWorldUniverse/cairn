package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
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
	"add": `cairn has no staging area — your edits on disk already ARE the working change; ` +
		`seal them with "cairn commit <branch> -m <msg>"`,
	"rebase": `cairn never needs a rebase — every "cairn commit" reconciles the line against ` +
		`the latest parent, so a line cannot drift behind it`,
	"reset": `cairn rewinds operations, not refs — "cairn oplog" lists what happened and ` +
		`"cairn undo" reverts the last one`,
	"revert": `cairn rewinds operations, not refs — "cairn oplog" lists what happened and ` +
		`"cairn undo" reverts the last one; to take a sealed commit back out of a line, use "cairn drop <commit>"`,
	"rm": `just delete the file on disk — with no index, the working change already tracks the ` +
		`removal; seal it with "cairn commit <branch> -m <msg>"`,
	"mv": `just move the file on disk — with no index, the working change already tracks the ` +
		`rename; seal it with "cairn commit <branch> -m <msg>"`,
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
		// git branch <name> == cairn express <name>: bring a new line into being.
		// Bare `git branch` LISTS, which is `cairn tree` / `cairn ls` — a
		// different verb, so it is a correction rather than an alias.
		args, _, err := gitPositionals(sub, rest)
		if err != nil {
			return true, err
		}
		if len(args) == 0 {
			return true, errors.New(`to list lines use "cairn tree" (the line tree) or "cairn ls" ` +
				`(the expressed folders); "cairn branch <name>" creates one, the same as "cairn express <name>"`)
		}
		return true, cmdExpress(rest)
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
	create := "b"
	if sub == "switch" {
		create = "c"
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
