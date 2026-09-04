package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// cmdDiff prints the unified diff for working-vs-tip (default or named branch) or
// commit-vs-commit. Binary files print a "Binary files differ" line.
func cmdDiff(args []string) error {
	// Split off pathspecs after a literal "--" (git-style): everything after it
	// filters the diff to those files/dirs. `cairn diff -- <file>` diffs one file
	// of the current line against its sealed parent (#89).
	var paths []string
	if i := indexOfDashDash(args); i >= 0 {
		paths = args[i+1:]
		args = args[:i]
	}
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	var diffs []change.FileDiff
	var bareArg string // set when a lone positional was auto-promoted to a pathspec
	switch fs.NArg() {
	case 0:
		branch, derr := r.DefaultBranch()
		if derr != nil {
			return mapErr(derr)
		}
		diffs, err = r.WorkingDiff(branch)
	case 1:
		// One positional: a branch if it is expressed, otherwise treat it as a
		// file path filter on the current/default line — so `cairn diff <path>`
		// (the natural single-file form) works without the explicit "--" (#89).
		arg := fs.Arg(0)
		if r.IsExpressed(arg) {
			diffs, err = r.WorkingDiff(arg)
		} else if len(paths) == 0 {
			bareArg = arg
			paths = []string{arg}
			branch, derr := r.DefaultBranch()
			if derr != nil {
				return mapErr(derr)
			}
			diffs, err = r.WorkingDiff(branch)
		} else {
			// A base was given alongside explicit `--` paths but is not an
			// expressed line — surface it clearly rather than silently diffing.
			return mapErr(fmt.Errorf("worktree.WorkingDiff: branch %q is not expressed", arg))
		}
	case 2:
		diffs, err = r.DiffCommits(fs.Arg(0), fs.Arg(1))
	default:
		return errors.New("usage: cairn diff [branch] [-- <path>...] | cairn diff <commitA> <commitB> [-- <path>...]")
	}
	if err != nil {
		return mapErr(err)
	}
	if len(paths) > 0 {
		// Canonicalize each pathspec to the TREE-ROOT-relative form diff paths
		// use, so `./file`, an absolute path, and a cwd-relative path typed from
		// inside a branch folder all match (git rewrites pathspecs the same way).
		paths = canonicalPathspecs(paths, *repo)
		diffs = filterDiffsByPaths(diffs, paths)
	}
	// Guard against a silent empty diff: if pathspecs matched nothing AND none
	// of them exist on disk, the arg was most likely a mistyped branch (bare
	// form) or a mistyped path (`--` form) — error loudly rather than printing
	// nothing, which reads as "no changes". (A deleted-but-committed file still
	// appears in the diff, and an existing-but-unchanged file legitimately
	// prints an empty diff.)
	if len(paths) > 0 && len(diffs) == 0 && !anyPathExistsNear(paths, *repo) {
		if bareArg != "" {
			return mapErr(fmt.Errorf("worktree.WorkingDiff: %q is neither an expressed branch nor a path in the working copy", bareArg))
		}
		return mapErr(fmt.Errorf("worktree.WorkingDiff: pathspec %q did not match any files in the working copy", strings.Join(paths, " ")))
	}
	for _, d := range diffs {
		if d.Binary {
			fmt.Printf("Binary files differ: %s\n", d.Path)
			continue
		}
		if d.Unified != "" {
			fmt.Print(d.Unified)
		} else {
			fmt.Printf("%s: %s\n", d.Status, d.Path)
		}
	}
	return nil
}

// canonicalPathspecs maps user-typed pathspecs to the TREE-ROOT-relative form
// FileDiff paths use. A spec that resolves to a real file/dir on disk (relative
// to cwd, or absolute) inside a branch folder is rewritten by stripping the
// "<repo>/<branch-folder>/" prefix; a spec that does not resolve on disk (e.g.
// a deleted file) is kept as typed, just cleaned — assumed already
// tree-relative. Specs pointing AT a branch folder itself are dropped (no
// filter is narrower than the whole tree).
func canonicalPathspecs(paths []string, repo string) []string {
	repoAbs, err := filepath.Abs(repo)
	if err != nil {
		repoAbs = repo
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = path.Clean(strings.ReplaceAll(p, `\`, "/"))
		abs := p
		if !filepath.IsAbs(abs) {
			if a, err := filepath.Abs(p); err == nil {
				abs = a
			}
		}
		if _, err := os.Stat(abs); err == nil {
			if rel, rerr := filepath.Rel(repoAbs, abs); rerr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "." {
				rel = filepath.ToSlash(rel)
				if i := strings.IndexByte(rel, '/'); i >= 0 {
					out = append(out, rel[i+1:]) // strip the branch-folder component
					continue
				}
				// The spec IS a branch folder → equivalent to no filter.
				continue
			}
		}
		out = append(out, strings.TrimPrefix(p, "/"))
	}
	return out
}

// anyPathExistsNear reports whether any canonical (tree-relative) pathspec
// exists on disk under some expressed branch folder, the repo root, or cwd.
// Used to distinguish a real (but unchanged) pathspec from a typo.
func anyPathExistsNear(paths []string, repo string) bool {
	for _, p := range paths {
		if pathExistsNear(p, repo) {
			return true
		}
	}
	return false
}

// pathExistsNear reports whether p exists relative to the current directory,
// the repo root, or any expressed branch folder (diff paths are tree-relative,
// so on disk the file lives at <repo>/<branch-folder>/<p>).
func pathExistsNear(p, repo string) bool {
	if _, err := os.Stat(p); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(repo, p)); err == nil {
		return true
	}
	ents, err := os.ReadDir(repo)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if !e.IsDir() || e.Name() == ".cairn" {
			continue
		}
		if _, err := os.Stat(filepath.Join(repo, e.Name(), p)); err == nil {
			return true
		}
	}
	return false
}

// indexOfDashDash returns the index of the first standalone "--" in args, or -1.
func indexOfDashDash(args []string) int {
	for i, a := range args {
		if a == "--" {
			return i
		}
	}
	return -1
}

// filterDiffsByPaths keeps only diffs whose path matches one of the pathspecs:
// an exact file match, or a directory prefix (spec is a parent dir of the path).
// Backslashes are normalised to "/" so Windows-style paths match the tree's
// forward-slash paths. An empty pathspec set returns diffs unchanged.
func filterDiffsByPaths(diffs []change.FileDiff, paths []string) []change.FileDiff {
	if len(paths) == 0 {
		return diffs
	}
	specs := make([]string, 0, len(paths))
	for _, p := range paths {
		// Normalise backslashes explicitly (filepath.ToSlash is a no-op for "\"
		// off Windows) so a Windows-style pathspec matches the tree's "/" paths.
		p = strings.TrimSuffix(strings.ReplaceAll(p, `\`, "/"), "/")
		if p != "" {
			specs = append(specs, p)
		}
	}
	out := diffs[:0:0]
	for _, d := range diffs {
		dp := strings.ReplaceAll(d.Path, `\`, "/")
		for _, spec := range specs {
			if dp == spec || strings.HasPrefix(dp, spec+"/") {
				out = append(out, d)
				break
			}
		}
	}
	return out
}
