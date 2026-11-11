package change

import (
	"bytes"
	"container/heap"
	"fmt"

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

// Parents returns the hex shas of every parent of commit, in order — none for
// a root. The working commit a conflicted pull leaves is a merge [local,
// upstream]; anything that rewrites that commit must carry BOTH, or the
// upstream side silently falls out of the line's history.
func (e *Engine) Parents(commit string) ([]string, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return nil, fmt.Errorf("commit %s: %w", commit, err)
	}
	out := make([]string, 0, len(c.ParentHashes))
	for _, h := range c.ParentHashes {
		out = append(out, h.String())
	}
	return out, nil
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
	now := stamp(e.now())
	if _, err := e.db.Exec(
		`UPDATE change SET head_commit=?, updated_at=? WHERE id=?`,
		head, now, changeID); err != nil {
		return fmt.Errorf("change.SetWorkingHead: %w", err)
	}
	return nil
}

// Describe finds the tag nearest to commit — `git describe --tags`: the
// tagged ancestor with the fewest commits between it and commit, over ALL
// parents. It returns the tag and that count (0 when commit itself is
// tagged), or "" when no tag is reachable. match, when non-nil, limits which
// tags are considered. Where DescribeVersion anchors a release on semver tags
// along the first-parent chain, this answers the general question for any
// commit and any tag.
func (e *Engine) Describe(commit string, match func(tag string) bool) (tag string, depth int, err error) {
	tags, err := e.ListTags()
	if err != nil {
		return "", 0, fmt.Errorf("change.Describe: %w", err)
	}
	byCommit := map[string]string{}
	for _, t := range tags {
		if match != nil && !match(t.Name) {
			continue
		}
		sha, ok := e.peelToCommit(t.Commit)
		if !ok {
			continue
		}
		if cur, dup := byCommit[sha]; dup {
			byCommit[sha] = preferTag(cur, t.Name)
		} else {
			byCommit[sha] = t.Name
		}
	}
	if len(byCommit) == 0 {
		return "", 0, nil
	}
	if name, ok := byCommit[commit]; ok {
		return name, 0, nil
	}
	// One generation-ordered walk (graph.go), git's describe algorithm made
	// exact: each candidate tag gets a bit, a commit carries the bits of the
	// candidates it is reachable from, and a commit popped WITHOUT a
	// candidate's bit lies between that tag and the commit — one more for its
	// depth. Popping by generation means a commit's bits are final when it is
	// popped. Once every queued commit is below some candidate no better one
	// can appear, and the walk runs on until the lowest count is final.
	const maxCandidates = 10
	g := e.lockGraph()
	defer e.cgMu.Unlock()
	start, err := g.id(plumbing.NewHash(commit))
	if err != nil {
		return "", 0, fmt.Errorf("change.Describe: %w", err)
	}
	if err := g.flushGenerations(); err != nil {
		return "", 0, fmt.Errorf("change.Describe: %w", err)
	}
	// Up to maxCandidates bits per commit: more than graphWalk's uint8 flags
	// hold, so this walk keeps its own flags over the same graph ids and the
	// same queue order (generation, then time, then hash).
	flags := make([]uint16, len(g.nodes))
	reached := make([]bool, len(g.nodes))
	q := &cgQueue{g: g}
	paint := func(id int32, f uint16) {
		flags[id] |= f
		if !reached[id] {
			reached[id] = true
			heap.Push(q, id)
		}
	}
	paint(start, 0)
	type candidate struct {
		name  string
		bit   uint16
		depth int
	}
	var cands []candidate
	spread := func(id int32) {
		for _, p := range g.parentsOf(id) {
			paint(p, flags[id])
		}
	}
	anyUncovered := func(mask uint16) bool {
		for _, id := range q.ids {
			if mask == 0 && flags[id] == 0 {
				return true
			}
			if mask != 0 && flags[id]&mask == 0 {
				return true
			}
		}
		return false
	}
	seen := 0
	for q.Len() > 0 && anyUncovered(0) {
		if seen > describeWalkCap {
			return "", 0, fmt.Errorf("change.Describe: walk exceeded %d commits", describeWalkCap)
		}
		id := heap.Pop(q).(int32)
		if name, ok := byCommit[g.nodes[id].hash.String()]; ok && len(cands) < maxCandidates && flags[id] == 0 {
			// Everything popped so far has a generation at least this
			// commit's, so none of it is reachable from here: it all counts.
			bit := uint16(1) << len(cands)
			cands = append(cands, candidate{name: name, bit: bit, depth: seen})
			flags[id] |= bit
		}
		seen++
		for i := range cands {
			if flags[id]&cands[i].bit == 0 {
				cands[i].depth++
			}
		}
		spread(id)
	}
	if len(cands) == 0 {
		return "", 0, nil
	}
	// Depths only grow as the walk goes on, so the candidate with the lowest
	// count is the answer once ITS count is finished — nothing queued is
	// outside its ancestry. Until then keep walking, counting for all.
	for {
		best := 0
		for i := range cands {
			if cands[i].depth < cands[best].depth {
				best = i
			}
		}
		if q.Len() == 0 || !anyUncovered(cands[best].bit) {
			return cands[best].name, cands[best].depth, nil
		}
		id := heap.Pop(q).(int32)
		for i := range cands {
			if flags[id]&cands[i].bit == 0 {
				cands[i].depth++
			}
		}
		spread(id)
	}
}

// cgQueue is a container/heap of commit-graph ids in graphWalk's order: the
// highest generation first, then the newest commit, then the lowest hash.
type cgQueue struct {
	g   *commitGraph
	ids []int32
}

func (q *cgQueue) Len() int { return len(q.ids) }
func (q *cgQueue) Less(i, j int) bool {
	a, b := &q.g.nodes[q.ids[i]], &q.g.nodes[q.ids[j]]
	if a.gen != b.gen {
		return a.gen > b.gen
	}
	if a.time != b.time {
		return a.time > b.time
	}
	return bytes.Compare(a.hash[:], b.hash[:]) < 0
}
func (q *cgQueue) Swap(i, j int) { q.ids[i], q.ids[j] = q.ids[j], q.ids[i] }
func (q *cgQueue) Push(x any)    { q.ids = append(q.ids, x.(int32)) }
func (q *cgQueue) Pop() any {
	n := len(q.ids) - 1
	x := q.ids[n]
	q.ids = q.ids[:n]
	return x
}
