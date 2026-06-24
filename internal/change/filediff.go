package change

import (
	"bytes"
	"sort"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// FileStatus is how a path changed between two trees.
type FileStatus int

const (
	Added FileStatus = iota
	Modified
	Deleted
)

func (s FileStatus) String() string {
	switch s {
	case Added:
		return "added"
	case Modified:
		return "modified"
	case Deleted:
		return "deleted"
	default:
		return "unknown"
	}
}

// FileDiff is one path's change between two trees.
type FileDiff struct {
	Path    string
	Status  FileStatus
	Binary  bool   // true when either side is binary (no Unified is produced)
	Unified string // unified-diff hunks for text changes ("" for Added/Deleted unless desired)
}

// DiffTrees compares two path->bytes trees (old -> new) and returns per-path
// diffs sorted by path. Modified text files include a unified diff; binary files
// are flagged without hunks.
func DiffTrees(old, new map[string][]byte, oldLabel, newLabel string) []FileDiff {
	set := map[string]struct{}{}
	for k := range old {
		set[k] = struct{}{}
	}
	for k := range new {
		set[k] = struct{}{}
	}
	paths := make([]string, 0, len(set))
	for k := range set {
		paths = append(paths, k)
	}
	sort.Strings(paths)

	out := make([]FileDiff, 0, len(paths))
	for _, p := range paths {
		ov, inOld := old[p]
		nv, inNew := new[p]
		switch {
		case inNew && !inOld:
			out = append(out, FileDiff{Path: p, Status: Added, Binary: isBinary(nv)})
		case inOld && !inNew:
			out = append(out, FileDiff{Path: p, Status: Deleted, Binary: isBinary(ov)})
		case inOld && inNew:
			if bytes.Equal(ov, nv) {
				continue // unchanged
			}
			d := FileDiff{Path: p, Status: Modified}
			if isBinary(ov) || isBinary(nv) {
				d.Binary = true
			} else {
				u, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
					A:        splitKeepNL(ov),
					B:        splitKeepNL(nv),
					FromFile: oldLabel + "/" + p,
					ToFile:   newLabel + "/" + p,
					Context:  3,
				})
				if err == nil {
					d.Unified = u
				}
			}
			out = append(out, d)
		}
	}
	return out
}

// splitKeepNL splits b into lines each retaining its trailing "\n", dropping the
// trailing empty element strings.SplitAfter produces for newline-terminated
// input. This matches what difflib's unified-diff renderer expects (one element
// per line, no spurious final blank line).
func splitKeepNL(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	parts := strings.SplitAfter(string(b), "\n")
	if n := len(parts); n > 0 && parts[n-1] == "" {
		parts = parts[:n-1]
	}
	return parts
}

// DiffCommits returns the per-path diff from commit a's tree to commit b's tree.
// An empty sha is treated as the empty tree (so a=="" diffs b against nothing —
// every path Added), letting callers diff a first commit against its absent
// parent.
func (e *Engine) DiffCommits(a, b string) ([]FileDiff, error) {
	fa, err := e.filesOrEmpty(a)
	if err != nil {
		return nil, err
	}
	fb, err := e.filesOrEmpty(b)
	if err != nil {
		return nil, err
	}
	return DiffTrees(fa, fb, shortSha(a), shortSha(b)), nil
}

// filesOrEmpty returns the tree of sha, or an empty map when sha is "".
func (e *Engine) filesOrEmpty(sha string) (map[string][]byte, error) {
	if sha == "" {
		return map[string][]byte{}, nil
	}
	return e.Files(sha)
}

// shortSha abbreviates a hex sha to 7 chars for diff labels, leaving shorter or
// empty inputs (e.g. "HEAD"/"working") untouched.
func shortSha(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	if s == "" {
		return "(none)"
	}
	return s
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}
