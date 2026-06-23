package change

import (
	"fmt"

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
		if _, ok := byCommit[t.Commit]; !ok {
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

// LineHeight returns the first-parent distance from line.TipCommit back to
// line.BaseCommit (the fork-point). Returns 0 if the tip equals the base or
// the tip is empty.
func (e *Engine) LineHeight(line Line) (int, error) {
	if line.TipCommit == "" || line.TipCommit == line.BaseCommit {
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
			return h + 1, nil
		}
		cur = next
	}
	return 0, fmt.Errorf("change.LineHeight: ancestry exceeded %d commits", describeWalkCap)
}

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
