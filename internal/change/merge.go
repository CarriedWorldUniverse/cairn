package change

import (
	"sort"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change/diff3"
)

// mergeForward rebases the change's snapshot onto its line's PARENT-line tip:
// the change adopts its parent. It returns the hex hash of the merged tree, the
// parent-line tip that was adopted (so the caller can record it as a MERGE parent
// of the merged commit — see below), and any per-path conflicts recorded along
// the way.
//
// The adopted-parent return is load-bearing: when the merged tree differs from
// the snapshot, the caller re-commits it and MUST list adoptedParent as a second
// parent. Otherwise the merge base between this line and its parent never
// advances, so a conflict that the user resolves is re-detected (and re-marked)
// on the very next commit — the resolution can never stick.
//
// For a root line (no parent), or a parent line with no tip yet, there is
// nothing to merge onto: the snapshot's own tree is returned and adoptedParent
// is "".
func (e *Engine) mergeForward(changeID, snapshotCommit string) (mergedTree, adoptedParent string, conflicts []Conflict, err error) {
	ch, err := e.GetChange(changeID)
	if err != nil {
		return "", "", nil, err
	}
	line, err := e.lineByID(ch.LineID)
	if err != nil {
		return "", "", nil, err
	}

	// Root line: nothing to adopt.
	if line.ParentLine == "" {
		t, err := e.commitTree(snapshotCommit)
		return t, "", nil, err
	}

	parent, err := e.lineByID(line.ParentLine)
	if err != nil {
		return "", "", nil, err
	}
	// Parent line has no commits yet: nothing to adopt.
	if parent.TipCommit == "" {
		t, err := e.commitTree(snapshotCommit)
		return t, "", nil, err
	}

	oursTree, err := e.commitTree(parent.TipCommit)
	if err != nil {
		return "", "", nil, err
	}
	theirsTree, err := e.commitTree(snapshotCommit)
	if err != nil {
		return "", "", nil, err
	}
	baseCommit, err := e.mergeBase(parent.TipCommit, snapshotCommit)
	if err != nil {
		return "", "", nil, err
	}
	var baseTree string
	if baseCommit != "" {
		if baseTree, err = e.commitTree(baseCommit); err != nil {
			return "", "", nil, err
		}
	}
	merged, conflicts, err := e.mergeTrees(changeID, baseTree, oursTree, theirsTree)
	return merged, parent.TipCommit, conflicts, err
}

// mergeTrees performs a per-path three-way merge of oursTree (parent line) and
// theirsTree (the change's snapshot) against baseTree, writes the merged result,
// and returns its hex hash plus any conflicts. baseTree may be "" (treated as an
// empty tree).
//
// This is meta-first: base/ours/theirs are read as SHA+mode metadata only
// (readTreeRefs), never full content. Every one of the "one side untouched" /
// "both agree" / "deletion propagates" shortcuts below is decided by comparing
// SHAs (content-addressed, so SHA equality IS byte equality) — no blob content
// is loaded for any of those paths. Blob content is fetched (readBlob) ONLY for
// a path reaching genuine divergence (both sides present, differ, and neither
// matches base) — the sole case where an actual diff3/binary-conflict decision
// needs bytes. The merged tree is then written by REFERENCE (writeTreeRefs):
// every path whose content is carried over from an existing blob costs zero
// blob writes/rehashes; only genuinely new merged content is written fresh.
func (e *Engine) mergeTrees(changeID, baseTree, oursTree, theirsTree string) (string, []Conflict, error) {
	base, err := e.readTreeRefs(baseTree)
	if err != nil {
		return "", nil, err
	}
	ours, err := e.readTreeRefs(oursTree)
	if err != nil {
		return "", nil, err
	}
	theirs, err := e.readTreeRefs(theirsTree)
	if err != nil {
		return "", nil, err
	}

	merged := map[string]TreeEntry{}
	var conflicts []Conflict

	for _, p := range unionKeysMeta(base, ours, theirs) {
		be, inBase := base[p]
		oe, inOurs := ours[p]
		te, inTheirs := theirs[p]

		switch {
		case inOurs && inTheirs:
			if oe.SHA == te.SHA {
				// Both sides agree (content-identical).
				merged[p] = oe
				continue
			}
			// Both present and differ. If one side never touched the file
			// (equal to base, content AND mode), take the other side wholesale —
			// no content load needed, just the reference.
			if inBase && be.SHA == oe.SHA && be.Mode == oe.Mode {
				merged[p] = te
				continue
			}
			if inBase && be.SHA == te.SHA && be.Mode == te.Mode {
				merged[p] = oe
				continue
			}
			// Genuine divergence: only NOW load content, to decide binary vs.
			// line-mergeable and build the actual merged bytes.
			bv, err := e.blobOrNil(be, inBase)
			if err != nil {
				return "", nil, err
			}
			ov, err := e.readBlob(oe.SHA)
			if err != nil {
				return "", nil, err
			}
			tv, err := e.readBlob(te.SHA)
			if err != nil {
				return "", nil, err
			}
			if isBinary(bv) || isBinary(ov) || isBinary(tv) {
				// Binary whole-file conflict: keep the change/theirs side verbatim
				// (theirs = the change's snapshot in mergeForward) and record a
				// conflict without emitting any text markers. theirs' blob already
				// exists — reference it, don't rewrite it.
				merged[p] = te
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
			h, err := e.writeBlob(mergedBytes)
			if err != nil {
				return "", nil, err
			}
			merged[p] = TreeEntry{SHA: h.String(), Mode: te.Mode}
			if res.Conflict {
				c, err := e.buildConflict(changeID, p, bv, ov, tv, mergedBytes)
				if err != nil {
					return "", nil, err
				}
				conflicts = append(conflicts, c)
			}

		case inOurs && !inTheirs:
			// Present only on the parent/ours side. Real 3-way semantics (#103 —
			// the old "keep the side that still has content" rule made deletions
			// NEVER propagate, resurrecting moved/deleted trees on reconcile):
			//  - not in base            → genuinely added on ours → keep it.
			//  - in base, ours == base  (content AND mode) → theirs DELETED an
			//                             untouched file → the deletion wins.
			//  - in base, ours != base  → modify(ours)/delete(theirs) → keep the
			//                             surviving content and record a conflict
			//                             (same keep-content posture as binary
			//                             conflicts; resolve decides).
			if !inBase {
				merged[p] = oe
				continue
			}
			if be.SHA == oe.SHA && be.Mode == oe.Mode {
				continue // content AND mode untouched — deletion propagates
			}
			merged[p] = oe
			bv, err := e.readBlob(be.SHA)
			if err != nil {
				return "", nil, err
			}
			ov, err := e.readBlob(oe.SHA)
			if err != nil {
				return "", nil, err
			}
			c, err := e.buildConflict(changeID, p, bv, ov, nil, ov)
			if err != nil {
				return "", nil, err
			}
			conflicts = append(conflicts, c)

		case !inOurs && inTheirs:
			// Present only on the change/theirs side — mirror of the case above
			// (#103: ours deleted/moved it; a theirs snapshot that never touched
			// the file must not resurrect it).
			if !inBase {
				merged[p] = te
				continue
			}
			if be.SHA == te.SHA && be.Mode == te.Mode {
				continue // content AND mode untouched — deletion propagates
			}
			merged[p] = te
			bv, err := e.readBlob(be.SHA)
			if err != nil {
				return "", nil, err
			}
			tv, err := e.readBlob(te.SHA)
			if err != nil {
				return "", nil, err
			}
			c, err := e.buildConflict(changeID, p, bv, nil, tv, tv)
			if err != nil {
				return "", nil, err
			}
			conflicts = append(conflicts, c)

		default:
			// Present only in base: deleted on both sides. Drop it.
		}
	}

	tree, err := e.writeTreeRefs(merged)
	if err != nil {
		return "", nil, err
	}
	return tree.String(), conflicts, nil
}

// blobOrNil reads entry's blob content, or returns nil when present is false
// (mirroring the zero-value []byte a missing map lookup used to yield before
// this package went meta-first).
func (e *Engine) blobOrNil(entry TreeEntry, present bool) ([]byte, error) {
	if !present {
		return nil, nil
	}
	return e.readBlob(entry.SHA)
}

// splitLines splits b into lines, each retaining its trailing "\n", matching the
// form diff3.Merge3 expects. An empty input yields nil.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	return strings.SplitAfter(string(b), "\n")
}

// unionKeysMeta returns the sorted union of all keys across the given
// path->TreeEntry metadata maps.
func unionKeysMeta(maps ...map[string]TreeEntry) []string {
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
