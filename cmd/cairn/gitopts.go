package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/userconfig"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

// Git-shaped option parsing for the inspection commands (log, show, diff,
// grep, rev-parse, …). Go's flag package cannot express what git's command
// lines take for granted — clustered short flags (-ni), attached values
// (-n5, -U10, -Lfoo), a bare count (-3), optional values (--color[=when]),
// --no-<flag>, repeated flags (-e a -e b) — and these commands exist to be
// driven by the same muscle memory and the same scripts as git's, so they
// parse the way git does. The cairn-wide --repo/--author keep working.

type optKind int

const (
	optBool   optKind = iota // --flag / --no-flag
	optValue                 // --flag=v, --flag v, -fv, -f v
	optOptVal                // --flag[=v]: a value only when attached
)

type optDef struct {
	name string // canonical (first) name
	kind optKind
}

// gitOpts is a parsed command line.
type gitOpts struct {
	cmd      string
	defs     map[string]*optDef
	vals     map[string][]string
	pos      []string // positionals before "--"
	paths    []string // everything after "--"
	hasDD    bool
	num      int // a bare -<N>
	numSet   bool
	allowNum bool
	// notNow tracks --not toggles as they are parsed, allNot records whether
	// --all came after an odd number of them (`--not HEAD~2 --all` makes every
	// ref uninteresting, as in git).
	notNow, allNot bool
}

// newGitOpts declares a command's options. spec is space-separated; each item
// is "name|alias|…" with a suffix: none = boolean, "=" = takes a value, "?" =
// optional value. A "#" item allows a bare -<N>. --repo is always accepted.
func newGitOpts(cmd, spec string) *gitOpts {
	g := &gitOpts{cmd: cmd, defs: map[string]*optDef{}, vals: map[string][]string{}}
	for _, item := range strings.Fields("repo= " + spec) {
		if item == "#" {
			g.allowNum = true
			continue
		}
		kind := optBool
		switch {
		case strings.HasSuffix(item, "="):
			kind, item = optValue, strings.TrimSuffix(item, "=")
		case strings.HasSuffix(item, "?"):
			kind, item = optOptVal, strings.TrimSuffix(item, "?")
		}
		names := strings.Split(item, "|")
		def := &optDef{name: names[0], kind: kind}
		for _, n := range names {
			g.defs[n] = def
		}
	}
	return g
}

func (g *gitOpts) set(d *optDef, v string) { g.vals[d.name] = append(g.vals[d.name], v) }

// parse reads args. Flags may appear anywhere before a literal "--".
func (g *gitOpts) parse(args []string) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			g.hasDD = true
			g.paths = append(g.paths, args[i+1:]...)
			return nil
		case a == "--not" && g.defs["not"] == nil:
			// A revision-list toggle, not an option: it stays in sequence.
			g.pos = append(g.pos, a)
			g.notNow = !g.notNow
		case a == "-h" && g.defs["h"] == nil, a == "--help":
			return flag.ErrHelp
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a[2:], "=")
			d := g.defs[name]
			if d == nil && strings.HasPrefix(name, "no-") && !hasVal {
				if nd := g.defs[name[3:]]; nd != nil && nd.kind != optValue {
					g.vals[nd.name] = append(g.vals[nd.name], "\x00no")
					continue
				}
			}
			if d == nil {
				return g.unknown(a)
			}
			if d.name == "all" {
				g.allNot = g.notNow
			}
			switch d.kind {
			case optBool:
				if hasVal {
					return fmt.Errorf("%s: --%s takes no value", g.cmd, name)
				}
				g.set(d, "true")
			case optOptVal:
				g.set(d, val)
			case optValue:
				if !hasVal {
					if i+1 >= len(args) {
						return fmt.Errorf("%s: --%s needs a value", g.cmd, name)
					}
					i++
					val = args[i]
				}
				g.set(d, val)
			}
		case len(a) > 1 && a[0] == '-':
			body := a[1:]
			// -<N>: a bare count.
			if n, err := strconv.Atoi(body); err == nil && n >= 0 {
				if !g.allowNum {
					return g.unknown(a)
				}
				g.num, g.numSet = n, true
				continue
			}
			// Go-style single-dash long flag (-repo x, -author=y).
			if name, val, hasVal := strings.Cut(body, "="); len(name) > 1 && g.defs[name] != nil {
				d := g.defs[name]
				switch {
				case d.kind == optBool && !hasVal:
					g.set(d, "true")
					continue
				case d.kind == optOptVal:
					g.set(d, val)
					continue
				case d.kind == optValue && hasVal:
					g.set(d, val)
					continue
				case d.kind == optValue && len(name) > 2:
					// Only names that cannot be a short flag with an attached
					// value (-n5 is -n 5, but -repo is --repo).
					if i+1 >= len(args) {
						return fmt.Errorf("%s: -%s needs a value", g.cmd, name)
					}
					i++
					g.set(d, args[i])
					continue
				}
			}
			// A cluster of short flags; a value-taking one consumes the rest.
			for j := 0; j < len(body); j++ {
				d := g.defs[body[j:j+1]]
				if d == nil {
					return g.unknown("-" + body[j:j+1])
				}
				if d.kind == optBool {
					g.set(d, "true")
					continue
				}
				rest := strings.TrimPrefix(body[j+1:], "=")
				if rest == "" && d.kind == optValue {
					if i+1 >= len(args) {
						return fmt.Errorf("%s: -%s needs a value", g.cmd, body[j:j+1])
					}
					i++
					rest = args[i]
				}
				g.set(d, rest)
				break
			}
		default:
			g.pos = append(g.pos, a)
		}
	}
	return nil
}

func (g *gitOpts) unknown(tok string) error {
	return fmt.Errorf("%s: unknown option %s", g.cmd, tok)
}

// has reports whether the option was given at all (including as --no-x).
func (g *gitOpts) has(name string) bool { return len(g.vals[g.defs[name].name]) > 0 }

// on reports whether a boolean (or optional-value) option is in effect: given,
// and not last given as --no-<name>.
func (g *gitOpts) on(name string) bool {
	vs := g.vals[g.defs[name].name]
	return len(vs) > 0 && vs[len(vs)-1] != "\x00no"
}

// negated reports whether the option was last given as --no-<name>.
func (g *gitOpts) negated(name string) bool {
	vs := g.vals[g.defs[name].name]
	return len(vs) > 0 && vs[len(vs)-1] == "\x00no"
}

// str returns the option's last value ("" when absent or negated).
func (g *gitOpts) str(name string) string {
	vs := g.vals[g.defs[name].name]
	if len(vs) == 0 || vs[len(vs)-1] == "\x00no" {
		return ""
	}
	return vs[len(vs)-1]
}

// strs returns every value the option was given.
func (g *gitOpts) strs(name string) []string {
	var out []string
	for _, v := range g.vals[g.defs[name].name] {
		if v != "\x00no" {
			out = append(out, v)
		}
	}
	return out
}

// integer returns the option's value as an int, or def when absent.
func (g *gitOpts) integer(name string, def int) (int, error) {
	if !g.has(name) {
		return def, nil
	}
	n, err := strconv.Atoi(g.str(name))
	if err != nil {
		return 0, fmt.Errorf("%s: --%s wants a number, got %q", g.cmd, name, g.str(name))
	}
	return n, nil
}

// onlyGiven reports whether every option given is one of names — how a
// command tells its classic cairn invocation from a git-shaped one.
func (g *gitOpts) onlyGiven(names ...string) bool {
	if g.numSet || g.hasDD {
		return false
	}
	allowed := map[string]bool{}
	for _, n := range names {
		allowed[g.defs[n].name] = true
	}
	for name, vs := range g.vals {
		if len(vs) > 0 && !allowed[name] {
			return false
		}
	}
	return true
}

// repoDir is the --repo value (default ".").
func (g *gitOpts) repoDir() string {
	if g.has("repo") {
		return g.str("repo")
	}
	return "."
}

// exitError ends the process with a specific status and NO message: the
// answer a script asked for with `diff --quiet`, `grep -q`, `cat-file -e` or
// `rev-parse --verify -q` is the status itself.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

// errExit1 is "the answer is no" — differences found, nothing matched.
var errExit1 error = &exitError{1}

// exitCodeOf reports the status a silent exitError asks for.
func exitCodeOf(err error) (int, bool) {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code, true
	}
	return 0, false
}

// ── colour ──────────────────────────────────────────────────────────────────

const (
	cReset   = "\x1b[m"
	cBold    = "\x1b[1m"
	cRed     = "\x1b[31m"
	cGreen   = "\x1b[32m"
	cYellow  = "\x1b[33m"
	cBlue    = "\x1b[34m"
	cMagenta = "\x1b[35m"
	cCyan    = "\x1b[36m"
	cBoldRed = "\x1b[1;31m"
)

// stdoutIsTTY reports whether stdout is a terminal.
func stdoutIsTTY() bool {
	if pagerActive {
		return true // stdout is the pager's pipe; the pager shows a terminal
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// useColor decides whether to colour output, shared by diff, log, show and
// grep. The --color[=when] / --no-color flag wins; then the color.ui config
// key (repo, else global); then auto. "auto" colours only a terminal, and
// never when NO_COLOR is set or TERM is dumb. Under a pager it counts as a terminal.
func useColor(g *gitOpts, r *worktree.Repo) bool {
	when := "auto"
	if r != nil {
		if v, ok, _ := r.GetConfig("color.ui"); ok && v != "" {
			when = v
		} else if v := userconfig.Get("color.ui"); v != "" {
			when = v
		}
	}
	if g != nil && g.defs["color"] != nil && g.has("color") {
		switch {
		case g.negated("color"):
			when = "never"
		case g.str("color") == "":
			when = "always"
		default:
			when = g.str("color")
		}
	}
	switch strings.ToLower(when) {
	case "always", "true", "on", "yes", "1":
		return true
	case "never", "false", "off", "no", "0":
		return false
	}
	return stdoutIsTTY() && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

// paint wraps s in a colour when on.
func paint(on bool, color, s string) string {
	if !on || s == "" {
		return s
	}
	return color + s + cReset
}

// ── where am I ──────────────────────────────────────────────────────────────

// lineContext is the line a command acts on and where the operator stands in
// it: git's "current branch", its work tree, and the cwd prefix inside it.
type lineContext struct {
	branch string // the line (the folder stood in, else the root line)
	dir    string // absolute path of the line's folder ("" when not expressed)
	prefix string // cwd relative to dir, "/"-separated, "" at its top
}

func currentLine(r *worktree.Repo, repoDir string) (lineContext, error) {
	branch, err := r.DefaultBranch()
	if err != nil {
		return lineContext{}, mapErr(err)
	}
	lc := lineContext{branch: branch}
	if ent, ok := r.Ls()[branch]; ok {
		lc.dir = filepath.Join(r.Root(), ent.Path)
	}
	if _, in := r.CWDBranch(); in && lc.dir != "" {
		if abs, err := filepath.Abs(repoDir); err == nil {
			if rel, err := filepath.Rel(lc.dir, abs); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				lc.prefix = filepath.ToSlash(rel)
			}
		}
	}
	return lc, nil
}

// treePaths maps user-typed paths to tree-root-relative ones: a relative path
// is taken from where the operator stands inside the line folder (as git
// does), an absolute one is located under the line folder.
func (lc lineContext) treePaths(paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		tp, err := lc.treePath(p)
		if err != nil {
			return nil, err
		}
		out = append(out, tp)
	}
	return out, nil
}

func (lc lineContext) treePath(p string) (string, error) {
	p = strings.ReplaceAll(p, `\`, "/")
	if filepath.IsAbs(p) {
		if lc.dir == "" {
			return "", fmt.Errorf("path %q is outside the line's folder", p)
		}
		rel, err := filepath.Rel(lc.dir, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("path %q is outside the line's folder %s", p, lc.dir)
		}
		if rel == "." {
			return "", nil
		}
		return filepath.ToSlash(rel), nil
	}
	joined := filepath.ToSlash(filepath.Join(lc.prefix, p))
	// --repo may name the repo root while the shell really stands deeper in
	// the line's folder: a path that only exists relative to the real cwd is
	// taken from there.
	if lc.dir != "" {
		if _, err := os.Lstat(filepath.Join(lc.dir, filepath.FromSlash(joined))); err != nil {
			if abs, aerr := filepath.Abs(filepath.FromSlash(p)); aerr == nil {
				if _, serr := os.Lstat(abs); serr == nil {
					if rel, rerr := filepath.Rel(lc.dir, abs); rerr == nil && rel != "." && !strings.HasPrefix(rel, "..") {
						return filepath.ToSlash(rel), nil
					}
				}
			}
		}
	}
	if joined == "." {
		return "", nil
	}
	if strings.HasPrefix(joined, "../") || joined == ".." {
		return "", fmt.Errorf("path %q is outside the line's folder", p)
	}
	return joined, nil
}

// display renders a tree path relative to where the operator stands.
func (lc lineContext) display(treePath string) string {
	if lc.prefix == "" {
		return treePath
	}
	rel, err := filepath.Rel(filepath.FromSlash(lc.prefix), filepath.FromSlash(treePath))
	if err != nil {
		return treePath
	}
	return filepath.ToSlash(rel)
}

// prefixSpec is the pathspec that limits a command to the directory the
// operator stands in (nil at the top of the line).
func (lc lineContext) prefixSpec() []string {
	if lc.prefix == "" {
		return nil
	}
	return []string{lc.prefix}
}

// splitRevsPaths sorts a command's positionals into revisions and paths the
// way git does: everything after "--" is a path; before it, arguments are
// revisions until the first one that is not, after which all are paths — and
// an argument that is neither is an error rather than a silent empty result.
func splitRevsPaths(eng *change.Engine, lc lineContext, g *gitOpts) (revs, paths []string, err error) {
	paths = append(paths, g.paths...)
	if g.hasDD {
		return g.pos, paths, nil
	}
	for i, a := range g.pos {
		if a == "--not" {
			revs = append(revs, a)
			continue
		}
		if _, rerr := eng.ParseRevisions([]string{a}); rerr == nil {
			revs = append(revs, a)
			continue
		} else if errors.Is(rerr, change.ErrAmbiguousRev) || errors.Is(rerr, change.ErrNoCommits) {
			return nil, nil, rerr
		}
		for _, p := range g.pos[i:] {
			if !lc.pathExists(eng, p) {
				return nil, nil, fmt.Errorf("%s: %q is neither a revision nor a path in the working copy (use '--' to separate paths from revisions)", g.cmd, p)
			}
		}
		paths = append(g.pos[i:], paths...)
		break
	}
	return revs, paths, nil
}

// pathExists reports whether p names something on disk, or in HEAD's tree.
func (lc lineContext) pathExists(eng *change.Engine, p string) bool {
	if strings.ContainsAny(p, "*?[") {
		return true
	}
	if _, err := os.Lstat(p); err == nil {
		return true
	}
	tp, err := lc.treePath(p)
	if err != nil {
		return false
	}
	if lc.dir != "" {
		if _, err := os.Lstat(filepath.Join(lc.dir, filepath.FromSlash(tp))); err == nil {
			return true
		}
	}
	if head, err := eng.ResolveCommit("HEAD"); err == nil {
		if _, _, ok, _ := eng.PathAt(head, tp); ok {
			return true
		}
	}
	return false
}

// treePathExists reports whether a tree-root-relative path exists in the
// line's folder or in HEAD's tree.
func (lc lineContext) treePathExists(eng *change.Engine, tp string) bool {
	if strings.ContainsAny(tp, "*?[") || tp == "" {
		return true
	}
	if lc.dir != "" {
		if _, err := os.Lstat(filepath.Join(lc.dir, filepath.FromSlash(tp))); err == nil {
			return true
		}
	}
	if head, err := eng.ResolveCommit("HEAD"); err == nil {
		if _, _, ok, _ := eng.PathAt(head, tp); ok {
			return true
		}
	}
	return false
}
