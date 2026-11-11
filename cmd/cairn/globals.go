package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/userconfig"
)

// peelGlobals consumes git's global options that may precede the subcommand:
//
//	-C <dir>             run as if started in <dir> (repeatable, cumulative)
//	-c <key>=<value>     transient config override for this invocation
//	--git-dir/--work-tree  a cairn repo has no git dir: map a ".cairn" path to
//	                     its repo, refuse anything else with the translation
//	--no-optional-locks, --no-replace-objects, --literal-pathspecs, ...
//	                     accepted, no effect
//
// The pager flags (--no-pager/-P/--paginate/-p) are passed through, in front
// of the remaining arguments, for pagerGlobals. $GIT_DIR / $GIT_WORK_TREE are
// ignored (never consulted), so a script's environment cannot misdirect cairn.
func peelGlobals(args []string) ([]string, error) {
	var pager []string
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "-C":
			if len(args) < 2 {
				return nil, fmt.Errorf("no directory given for -C")
			}
			if err := os.Chdir(args[1]); err != nil {
				return nil, fmt.Errorf("cannot change to '%s': %v", args[1], unwrapPathErr(err))
			}
			args = args[2:]
		case a == "-c":
			if len(args) < 2 {
				return nil, fmt.Errorf("-c expects a configuration string")
			}
			if err := setTransientConfig(args[1]); err != nil {
				return nil, err
			}
			args = args[2:]
		case strings.HasPrefix(a, "-c") && strings.Contains(a, "=") && !strings.HasPrefix(a, "--"):
			// -ckey=value
			if err := setTransientConfig(a[2:]); err != nil {
				return nil, err
			}
			args = args[1:]
		case a == "--git-dir" || a == "--work-tree":
			if len(args) < 2 {
				return nil, fmt.Errorf("%s expects a <path>", a)
			}
			if err := mapGitDir(a, args[1]); err != nil {
				return nil, err
			}
			args = args[2:]
		case strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree="):
			k, v, _ := strings.Cut(a, "=")
			if err := mapGitDir(k, v); err != nil {
				return nil, err
			}
			args = args[1:]
		case a == "--no-optional-locks" || a == "--no-replace-objects" || a == "--literal-pathspecs" ||
			a == "--glob-pathspecs" || a == "--noglob-pathspecs" || a == "--icase-pathspecs" || a == "--no-advice":
			args = args[1:]
		case a == "--no-pager" || a == "-P" || a == "--paginate" || a == "-p":
			pager = append(pager, a)
			args = args[1:]
		default:
			return append(pager, args...), nil
		}
	}
	return pager, nil
}

func unwrapPathErr(err error) error {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err
	}
	return err
}

// setTransientConfig records "key=value" (a bare "key" means "true", as git).
func setTransientConfig(kv string) error {
	k, v, ok := strings.Cut(kv, "=")
	if k == "" || !strings.Contains(k, ".") {
		return fmt.Errorf("-c %q: key does not contain a section: expected <section>.<name>[=<value>]", kv)
	}
	if !ok {
		v = "true"
	}
	userconfig.SetOverride(k, v)
	return nil
}

// mapGitDir translates --git-dir/--work-tree. A repo's metadata dir (.cairn)
// or its root names the repo: cairn acts as if started there. Anything else
// has no cairn meaning and is refused with the equivalent.
func mapGitDir(opt, path string) error {
	p := filepath.Clean(path)
	if filepath.Base(p) == ".cairn" {
		p = filepath.Dir(p)
	}
	if opt == "--git-dir" {
		if fi, err := os.Stat(filepath.Join(p, ".cairn")); err == nil && fi.IsDir() {
			return os.Chdir(p)
		}
	}
	return fmt.Errorf("%s %s: a cairn repo has no git dir or separate work tree — run from the repo, or use `cairn -C <repo> ...` / `--repo <repo>`", opt, path)
}

// isGlobalOpt reports whether a is one of the global options peelGlobals eats.
func isGlobalOpt(a string) bool {
	switch {
	case a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree",
		strings.HasPrefix(a, "--git-dir="), strings.HasPrefix(a, "--work-tree="),
		a == "--no-optional-locks", a == "--no-replace-objects", a == "--literal-pathspecs":
		return true
	}
	return strings.HasPrefix(a, "-c") && strings.Contains(a, "=") && !strings.HasPrefix(a, "--")
}

// isFatal marks the failures git reports as "fatal:" (exit 128) rather than as
// an ordinary failed command (exit 1): not in a repository, an unknown
// revision, a pathspec that matches nothing. Scripts that branch on $? to tell
// "bad input" from "the command said no" get the same answer they get from git.
// Conflicts stay exit 2 and ordinary refusals stay exit 1.
func isFatal(err error) bool {
	msg := err.Error()
	for _, s := range []string{"not a cairn repo", "unknown revision", "did not match any", "bad revision", "ambiguous argument"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
