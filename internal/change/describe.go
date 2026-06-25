package change

import (
	"fmt"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/version"
	"github.com/go-git/go-git/v5/plumbing"
)

const describeWalkCap = 100000

// DescribeVersion walks first-parent ancestry from commit back to the nearest
// tagged commit and returns the tag name and the distance (0 means the commit
// is itself tagged). If no tag is found the walk terminates at the root and
// returns ("", totalDepth, nil).
func (e *Engine) DescribeVersion(commit string) (string, int, error) {
	if commit == "" {
		return "", 0, nil
	}
	tags, err := e.ListTags()
	if err != nil {
		return "", 0, fmt.Errorf("change.DescribeVersion: %w", err)
	}
	byCommit := make(map[string]string, len(tags))
	for _, t := range tags {
		// Only semver tags anchor a derived version. A repo can carry arbitrary
		// non-semver tags (e.g. "v1", "forgejo-archive", inherited release tags);
		// anchoring on one would make `version`/`release` hard-fail at Parse. Skip
		// them so the walk continues to the nearest real version tag (or none).
		if _, err := version.Parse(t.Name); err != nil {
			continue
		}
		if cur, ok := byCommit[t.Commit]; ok {
			byCommit[t.Commit] = preferTag(cur, t.Name)
		} else {
			byCommit[t.Commit] = t.Name
		}
	}
	cur := commit
	for dist := 0; dist < describeWalkCap; dist++ {
		if name, ok := byCommit[cur]; ok {
			return name, dist, nil
		}
		next, err := e.firstParent(cur)
		if err != nil {
			return "", 0, fmt.Errorf("change.DescribeVersion: %w", err)
		}
		if next == "" {
			return "", dist + 1, nil
		}
		cur = next
	}
	return "", 0, fmt.Errorf("change.DescribeVersion: ancestry exceeded %d commits", describeWalkCap)
}

// LineHeight returns the number of commits on line since its base (branch point):
// the first-parent distance from TipCommit back to BaseCommit. A line with no
// commits beyond its base (or no base recorded) has height 0.
func (e *Engine) LineHeight(line Line) (int, error) {
	if line.TipCommit == "" || line.BaseCommit == "" || line.TipCommit == line.BaseCommit {
		return 0, nil
	}
	cur := line.TipCommit
	for h := 0; h < describeWalkCap; h++ {
		if cur == line.BaseCommit {
			return h, nil
		}
		next, err := e.firstParent(cur)
		if err != nil {
			return 0, fmt.Errorf("change.LineHeight: %w", err)
		}
		if next == "" {
			// Reached a root without crossing BaseCommit (e.g. the tip was
			// fast-forwarded onto a remote head whose first-parent chain bypasses
			// the local fork point). h is still a deterministic, monotonic
			// first-parent depth — adequate for a unique per-line pre-release.
			return h, nil
		}
		cur = next
	}
	return 0, fmt.Errorf("change.LineHeight: ancestry exceeded %d commits", describeWalkCap)
}

// preferTag returns the higher-precedence of two tag names that point at the same
// commit, so DescribeVersion bases on the most specific release. Unparseable tags
// sort below parseable ones; two unparseable tags fall back to the larger string.
func preferTag(a, b string) string {
	pa, ea := version.Parse(a)
	pb, eb := version.Parse(b)
	switch {
	case ea != nil && eb != nil:
		if a >= b {
			return a
		}
		return b
	case ea != nil:
		return b
	case eb != nil:
		return a
	default:
		if version.Compare(pa, pb) >= 0 {
			return a
		}
		return b
	}
}

// FirstParent returns the hex sha of the first parent of commit, or "" if the
// commit is a root (has no parents). It is the exported wrapper over firstParent
// for callers that need a working commit's parent (e.g. working-vs-parent diffs).
func (e *Engine) FirstParent(commit string) (string, error) { return e.firstParent(commit) }

// firstParent returns the hex sha of the first parent of commit, or "" if the
// commit is a root (has no parents).
func (e *Engine) firstParent(commit string) (string, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return "", fmt.Errorf("commit %s: %w", commit, err)
	}
	if c.NumParents() == 0 {
		return "", nil
	}
	first, err := c.Parent(0)
	if err != nil {
		return "", fmt.Errorf("parent of %s: %w", commit, err)
	}
	return first.Hash.String(), nil
}

// ChangeIDOf reads commitSha and returns the value of its `Change-Id:` trailer,
// or "" if the commit carries no trailer (e.g. an externally-authored commit).
// Callers reconciling an open change's head after an undo treat "" as "not our
// working commit" and start a fresh working commit on top.
func (e *Engine) ChangeIDOf(commitSha string) (string, error) {
	if commitSha == "" {
		return "", nil
	}
	c, err := e.git.CommitObject(plumbing.NewHash(commitSha))
	if err != nil {
		return "", fmt.Errorf("change.ChangeIDOf: commit %s: %w", commitSha, err)
	}
	return parseChangeID(c.Message), nil
}

// SetWorkingHead repoints an open change's head_commit. An empty head means the
// change has no working commit yet, so the next snapshot roots on the line tip;
// this is how undo-reconciliation restarts a working commit on the restored tip.
func (e *Engine) SetWorkingHead(changeID, head string) error {
	now := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(
		`UPDATE change SET head_commit=?, updated_at=? WHERE id=?`,
		head, now, changeID); err != nil {
		return fmt.Errorf("change.SetWorkingHead: %w", err)
	}
	return nil
}
