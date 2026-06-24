package change

import (
	"bytes"
	"sort"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change/diff3"
)

// mergeForward rebases the change's snapshot onto its line's PARENT-line tip:
// the change adopts its parent. It returns the hex hash of the merged tree plus
// any per-path conflicts recorded along the way.
//
// For a root line (no parent), or a parent line with no tip yet, there is
// nothing to merge onto and the snapshot's own tree is returned unchanged.
func (e *Engine) mergeForward(changeID, snapshotCommit string) (mergedTree string, conflicts []Conflict, err error) {
	ch, err := e.GetChange(changeID)
	if err != nil {
		return "", nil, err
	}
	line, err := e.lineByID(ch.LineID)
	if err != nil {
		return "", nil, err
	}

	// Root line: nothing to adopt.
	if line.ParentLine == "" {
		t, err := e.commitTree(snapshotCommit)
		return t, nil, err
	}

	parent, err := e.lineByID(line.ParentLine)
	if err != nil {
		return "", nil, err
	}
	// Parent line has no commits yet: nothing to adopt.
	if parent.TipCommit == "" {
		t, err := e.commitTree(snapshotCommit)
		return t, nil, err
	}

	oursTree, err := e.commitTree(parent.TipCommit)
	if err != nil {
		return "", nil, err
	}
	theirsTree, err := e.commitTree(snapshotCommit)
	if err != nil {
		return "", nil, err
	}
	baseCommit, err := e.mergeBase(parent.TipCommit, snapshotCommit)
	if err != nil {
		return "", nil, err
	}
	var baseTree string
	if baseCommit != "" {
		if baseTree, err = e.commitTree(baseCommit); err != nil {
			return "", nil, err
		}
	}
	return e.mergeTrees(changeID, baseTree, oursTree, theirsTree)
}

// mergeTrees performs a per-path three-way merge of oursTree (parent line) and
// theirsTree (the change's snapshot) against baseTree, writes the merged result,
// and returns its hex hash plus any conflicts. baseTree may be "" (treated as an
// empty tree).
func (e *Engine) mergeTrees(changeID, baseTree, oursTree, theirsTree string) (string, []Conflict, error) {
	base, err := e.maybeReadTree(baseTree)
	if err != nil {
		return "", nil, err
	}
	ours, err := e.readTree(oursTree)
	if err != nil {
		return "", nil, err
	}
	theirs, err := e.readTree(theirsTree)
	if err != nil {
		return "", nil, err
	}

	merged := map[string][]byte{}
	var conflicts []Conflict

	for _, p := range unionKeys(base, ours, theirs) {
		bv, inBase := base[p]
		ov, inOurs := ours[p]
		tv, inTheirs := theirs[p]

		switch {
		case inOurs && inTheirs:
			if bytes.Equal(ov, tv) {
				// Both sides agree.
				merged[p] = ov
				continue
			}
			// Both present and differ. If one side never touched the file
			// (equal to base), take the other side wholesale.
			if inBase && bytes.Equal(bv, ov) {
				merged[p] = tv
				continue
			}
			if inBase && bytes.Equal(bv, tv) {
				merged[p] = ov
				continue
			}
			// Genuine divergence: check for binary before attempting line-merge.
			if isBinary(bv) || isBinary(ov) || isBinary(tv) {
				// Binary whole-file conflict: keep the change/theirs side verbatim
				// (theirs = the change's snapshot in mergeForward) and record a
				// conflict without emitting any text markers.
				merged[p] = tv
				c, err := e.buildConflict(changeID, p, bv, ov, tv, tv)
				if err != nil {
					return "", nil, err
				}
				conflicts = append(conflicts, c)
				continue
			}
			// Three-way line merge for text files.
			res := diff3.Merge3(splitLines(bv), splitLines(ov), splitLines(tv))
			mergedBytes := []byte(strings.Join(res.Merged, ""))
			merged[p] = mergedBytes
			if res.Conflict {
				c, err := e.buildConflict(changeID, p, bv, ov, tv, mergedBytes)
				if err != nil {
					return "", nil, err
				}
				conflicts = append(conflicts, c)
			}

		case inOurs && !inTheirs:
			// Present only on the parent side: added on parent, or the change
			// deleted it (whether or not the parent also modified it). Phase-1
			// rule: keep the side that still has content — the parent's.
			merged[p] = ov

		case !inOurs && inTheirs:
			// Present only on the change side: added on the change, or the
			// parent deleted it (whether or not the change also modified it).
			// Phase-1 rule: keep the side that still has content — the change's.
			merged[p] = tv

		default:
			// Present only in base: deleted on both sides. Drop it.
		}
	}

	tree, err := e.writeTree(merged, nil)
	if err != nil {
		return "", nil, err
	}
	return tree.String(), conflicts, nil
}

// maybeReadTree reads a tree into a path->bytes map, returning an empty map when
// treeHash is "".
func (e *Engine) maybeReadTree(treeHash string) (map[string][]byte, error) {
	if treeHash == "" {
		return map[string][]byte{}, nil
	}
	return e.readTree(treeHash)
}

// splitLines splits b into lines, each retaining its trailing "\n", matching the
// form diff3.Merge3 expects. An empty input yields nil.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.SplitAfter(string(b), "\n")
}

// unionKeys returns the sorted union of all keys across the given maps.
func unionKeys(maps ...map[string][]byte) []string {
	set := map[string]struct{}{}
	for _, m := range maps {
		for k := range m {
			set[k] = struct{}{}
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
