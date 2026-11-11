package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
	"github.com/mattn/go-isatty"
)

// Argument handling shared by the git-shaped working-copy verbs (restore,
// reset, revert, commit, status, rm, mv, clean, stash, apply, cherry-pick).
// parseArgs already tolerates interspersed flags and attached short values;
// what git muscle memory adds on top is bundled short flags (`clean -fdx`,
// `commit -am msg`, `status -sb`), repeatable flags (`-m a -m b`) and flags
// whose value is optional (`--porcelain[=v1]`, `-u[mode]`).

// expandShortFlags splits a bundle of single-letter flags into separate
// tokens: "-fdx" → "-f -d -x", "-am" → "-a -m" (the next token stays -m's
// value), "-amfix" → "-a -m fix". A token is only treated as a bundle when it
// is not itself a defined flag name and its first letter is a defined BOOLEAN
// flag — so "-author", "-n5" and "-mmsg" are left for parseArgs. Everything
// after a literal "--" is untouched.
func expandShortFlags(fs *flag.FlagSet, args []string) []string {
	out := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			return append(out, args[i:]...)
		}
		if len(a) < 3 || a[0] != '-' || a[1] == '-' || strings.Contains(a, "=") ||
			fs.Lookup(a[1:]) != nil || !isBoolFlag(fs, a[1:2]) {
			out = append(out, a)
			continue
		}
		var parts []string
		ok := true
		for j := 1; j < len(a); j++ {
			name := a[j : j+1]
			if fs.Lookup(name) == nil {
				ok = false
				break
			}
			parts = append(parts, "-"+name)
			if !isBoolFlag(fs, name) {
				if j+1 < len(a) {
					parts = append(parts, a[j+1:]) // attached value
				}
				break
			}
		}
		if !ok {
			out = append(out, a)
			continue
		}
		out = append(out, parts...)
	}
	return out
}

// parseGitArgs is parseArgs with git's bundled short flags expanded first.
func parseGitArgs(fs *flag.FlagSet, args []string) error {
	return parseArgs(fs, expandShortFlags(fs, args))
}

// splitDashDash splits args at the first literal "--": what precedes it, what
// follows it, and whether it was there at all.
func splitDashDash(args []string) (before, after []string, found bool) {
	if i := indexOfDashDash(args); i >= 0 {
		return args[:i], args[i+1:], true
	}
	return args, nil, false
}

// takeOptionalValueFlag removes every occurrence of a flag whose value is
// OPTIONAL and, when present, attached (`--porcelain` / `--porcelain=v1`, `-u`
// / `-uno`) — a shape Go's flag package cannot express, since a string flag
// would swallow the next argument. It reports whether the flag was given and
// its last value ("" when bare). Tokens after "--" are left alone.
func takeOptionalValueFlag(args []string, long, short string) (rest []string, given bool, value string) {
	rest = make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		switch {
		case long != "" && (a == "--"+long || a == "-"+long):
			given, value = true, ""
		case long != "" && (strings.HasPrefix(a, "--"+long+"=") || strings.HasPrefix(a, "-"+long+"=")):
			given, value = true, a[strings.IndexByte(a, '=')+1:]
		case short != "" && a == "-"+short:
			given, value = true, ""
		case short != "" && strings.HasPrefix(a, "-"+short) && !strings.HasPrefix(a, "--"):
			given, value = true, a[len(short)+1:]
		default:
			rest = append(rest, a)
		}
	}
	return rest, given, value
}

// stringList is a repeatable string flag (`-m one -m two`).
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, "\n\n") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// branchFlag registers the explicit line selector the path- and commit-taking
// verbs share. Their positionals are paths or commits, so — unlike `commit
// <branch>` — the line cannot be a positional; it is inferred from the folder
// you stand in, or named with --branch.
func branchFlag(fs *flag.FlagSet) *string {
	return fs.String("branch", "", "line to act on (default: the folder you are in, else the root line)")
}

// targetBranch resolves the line a verb acts on: --branch, else the folder the
// command was run from, else the root line.
func targetBranch(r *worktree.Repo, explicit string) (string, error) {
	if explicit != "" {
		if !r.IsExpressed(explicit) {
			return "", fmt.Errorf("branch %q is not expressed", explicit)
		}
		return explicit, nil
	}
	return r.DefaultBranch()
}

// treePathspecs maps user-typed pathspecs to paths relative to the line's tree
// root. Typed from inside the line's folder they are relative to the current
// directory, as in git (so `cairn restore .` in a subfolder means that
// subfolder, and a deleted file can still be named); typed from anywhere else
// they are taken as already tree-relative. A spec that points outside the
// line's folder is an error rather than a silent no-match.
func treePathspecs(r *worktree.Repo, branch string, specs []string) ([]string, error) {
	folder, ok := r.FolderOf(branch)
	if !ok {
		return nil, fmt.Errorf("branch %q is not expressed", branch)
	}
	folder, err := filepath.Abs(folder)
	if err != nil {
		return nil, err
	}
	if resolved, rerr := filepath.EvalSymlinks(folder); rerr == nil {
		folder = resolved
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if resolved, rerr := filepath.EvalSymlinks(cwd); rerr == nil {
		cwd = resolved
	}
	inside := cwd == folder || strings.HasPrefix(cwd, folder+string(filepath.Separator))
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		spec = strings.ReplaceAll(spec, `\`, "/")
		var abs string
		switch {
		case filepath.IsAbs(spec):
			abs = filepath.Clean(spec)
			if resolved, rerr := filepath.EvalSymlinks(filepath.Dir(abs)); rerr == nil {
				abs = filepath.Join(resolved, filepath.Base(abs))
			}
		case inside:
			abs = filepath.Join(cwd, filepath.FromSlash(spec))
		default:
			abs = filepath.Join(folder, filepath.FromSlash(spec))
		}
		rel, rerr := filepath.Rel(folder, abs)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("pathspec %q is outside %s's folder", spec, branch)
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			rel = ""
		}
		out = append(out, rel)
	}
	return out, nil
}

// stdinIsTerminal reports whether stdin is a real terminal. It is stricter
// than isInteractive's character-device test, which /dev/null also passes:
// launching an editor needs an actual tty. A var so tests can force it.
var stdinIsTerminal = func() bool {
	return isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
}

// reportConflicts prints the conflict notice a verb leaves and returns
// errConflicts (exit 2), or nil when there are none.
func reportConflicts(verb string, paths []string, next string) error {
	if len(paths) == 0 {
		return nil
	}
	fmt.Fprintf(os.Stderr, "%s: %d conflict(s) in: %s — %s\n", verb, len(paths), strings.Join(paths, ", "), next)
	return errConflicts
}

// errUsage builds a usage error.
func errUsage(u string) error { return errors.New("usage: " + u) }
