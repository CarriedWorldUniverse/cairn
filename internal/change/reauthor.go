package change

import (
	"fmt"
	"path"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// opReauthor is the op-log type for a bulk identity rewrite. Like a seal it never
// coalesces.
const opReauthor = "reauthor"

// ReauthorSpec selects which commits to retag and what to retag them to. OldName/
// OldEmail are glob filters (path.Match syntax, e.g. "*@users.noreply.cairn"); an
// empty filter matches anything. A commit's author (and committer, independently)
// matches when BOTH filters match it. NewName/NewEmail are the replacements; an
// empty replacement leaves that field unchanged.
type ReauthorSpec struct {
	OldName, OldEmail string
	NewName, NewEmail string
	DryRun            bool
}

// ReauthorResult reports what a Reauthor pass did (or, when DryRun, would do).
// Matched counts commits whose own author/committer identity was rewritten;
// Rewritten counts every commit whose SHA changed — the matched commits plus the
// descendants that had to be rebuilt onto them.
type ReauthorResult struct {
	Matched   int
	Rewritten int
}

// matchSig reports whether sig matches the spec's old-identity filters.
func (s ReauthorSpec) matchSig(sig object.Signature) bool {
	return globMatch(s.OldName, sig.Name) && globMatch(s.OldEmail, sig.Email)
}

// apply returns sig with the new name/email substituted (preserving the
// timestamp), but only if it matched; otherwise sig is returned unchanged. The
// bool reports whether a substitution happened.
func (s ReauthorSpec) apply(sig object.Signature) (object.Signature, bool) {
	if !s.matchSig(sig) {
		return sig, false
	}
	out := sig // keeps When
	if s.NewName != "" {
		out.Name = s.NewName
	}
	if s.NewEmail != "" {
		out.Email = s.NewEmail
	}
	return out, out != sig
}

// globMatch reports whether s matches pattern. An empty pattern matches anything.
// A pattern with no wildcard is an exact match; otherwise path.Match glob syntax
// applies (a malformed pattern falls back to exact equality).
func globMatch(pattern, s string) bool {
	if pattern == "" {
		return true
	}
	ok, err := path.Match(pattern, s)
	if err != nil {
		return pattern == s
	}
	return ok
}

// Reauthor rewrites the author/committer identity of every commit in the repo
// that matches spec, across ALL lines including the root. Because changing a
// commit's identity changes its hash, every descendant is rebuilt onto the
// rewritten parent (a whole-graph rewrite, like git filter-repo's mailmap). Trees,
// messages, and timestamps are preserved exactly — only identity and parent links
// change. All catalogue SHA references are remapped in one transaction; the git
// objects are content-addressed and written outside it (idempotent). With
// spec.DryRun the graph is scanned and counted but nothing is written.
func (e *Engine) Reauthor(spec ReauthorSpec) (ReauthorResult, error) {
	// 1. Gather every commit SHA the catalogue anchors on, then walk full ancestry
	// from each to enumerate the whole reachable commit graph.
	anchors, err := e.commitAnchors()
	if err != nil {
		return ReauthorResult{}, err
	}
	order, err := e.topoCommits(anchors) // parents-before-children
	if err != nil {
		return ReauthorResult{}, err
	}

	// 2. Rebuild each commit in topo order, remapping parents through old→new and
	// retagging matched signatures. Unchanged commits map to themselves.
	mapping := make(map[string]string, len(order))
	var res ReauthorResult
	for _, sha := range order {
		c, err := e.git.CommitObject(plumbing.NewHash(sha))
		if err != nil {
			return ReauthorResult{}, fmt.Errorf("change.Reauthor: load %s: %w", sha, err)
		}
		newAuthor, aChanged := spec.apply(c.Author)
		newCommitter, cChanged := spec.apply(c.Committer)
		parentsChanged := false
		newParents := make([]plumbing.Hash, len(c.ParentHashes))
		for i, p := range c.ParentHashes {
			np := mapping[p.String()]
			if np == "" { // parent outside the walked set (shouldn't happen): keep it
				np = p.String()
			}
			if np != p.String() {
				parentsChanged = true
			}
			newParents[i] = plumbing.NewHash(np)
		}
		if aChanged || cChanged {
			res.Matched++
		}
		if !aChanged && !cChanged && !parentsChanged {
			mapping[sha] = sha // nothing to do; hash is stable
			continue
		}
		res.Rewritten++
		if spec.DryRun {
			// Predict the new hash so descendants still see a changed parent, but
			// write nothing. plumbing computes the hash from the encoded object.
			nc := rebuildCommit(c, newAuthor, newCommitter, newParents)
			mapping[sha] = nc.Hash.String()
			continue
		}
		nc := rebuildCommit(c, newAuthor, newCommitter, newParents)
		stored, err := e.writeRawCommit(nc)
		if err != nil {
			return ReauthorResult{}, err
		}
		mapping[sha] = stored
	}

	if spec.DryRun || res.Rewritten == 0 {
		return res, nil
	}

	// 3. Remap every catalogue SHA column and record the op, atomically.
	if err := e.remapCatalogue(spec, mapping); err != nil {
		return ReauthorResult{}, err
	}
	// 4. Refresh exported refs (refs/heads, refs/cairn/change, tags) onto new SHAs.
	if err := e.Export(); err != nil {
		return ReauthorResult{}, fmt.Errorf("change.Reauthor: export: %w", err)
	}
	return res, nil
}

// rebuildCommit returns a new commit object identical to c except for its author,
// committer, and parent links. The object's Hash is recomputed by Encode, so the
// returned commit's Hash field reflects the new content.
func rebuildCommit(c *object.Commit, author, committer object.Signature, parents []plumbing.Hash) *object.Commit {
	nc := &object.Commit{
		Author:       author,
		Committer:    committer,
		Message:      c.Message,
		TreeHash:     c.TreeHash,
		ParentHashes: parents,
		PGPSignature: c.PGPSignature,
		MergeTag:     c.MergeTag,
		Encoding:     c.Encoding,
	}
	// Compute the content hash so callers (incl. dry-run) can chain descendants.
	nc.Hash = commitHash(nc)
	return nc
}

// commitHash returns the git object hash a commit encodes to, without storing it.
func commitHash(c *object.Commit) plumbing.Hash {
	enc := &plumbing.MemoryObject{}
	enc.SetType(plumbing.CommitObject)
	if err := c.Encode(enc); err != nil {
		return plumbing.ZeroHash
	}
	return enc.Hash()
}

// writeRawCommit stores commit c verbatim and returns its hex sha.
func (e *Engine) writeRawCommit(c *object.Commit) (string, error) {
	obj := e.git.Storer.NewEncodedObject()
	if err := c.Encode(obj); err != nil {
		return "", fmt.Errorf("change.writeRawCommit: encode: %w", err)
	}
	h, err := e.git.Storer.SetEncodedObject(obj)
	if err != nil {
		return "", fmt.Errorf("change.writeRawCommit: store: %w", err)
	}
	return h.String(), nil
}

// commitAnchors returns every distinct, non-empty commit SHA the catalogue
// references — the roots from which the full commit graph is reachable.
func (e *Engine) commitAnchors() ([]string, error) {
	queries := []string{
		`SELECT tip_commit FROM line UNION SELECT base_commit FROM line`,
		`SELECT head_commit FROM change`,
		`SELECT commit_sha FROM tag`,
		`SELECT commit_sha FROM stash UNION SELECT base_sha FROM stash`,
		`SELECT good_sha FROM bisect UNION SELECT bad_sha FROM bisect
		   UNION SELECT current_sha FROM bisect UNION SELECT restore_tip FROM bisect`,
	}
	seen := map[string]struct{}{}
	var out []string
	for _, q := range queries {
		rows, err := e.db.Query(q)
		if err != nil {
			return nil, fmt.Errorf("change.commitAnchors: %w", err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("change.commitAnchors: scan: %w", err)
			}
			if s == "" {
				continue
			}
			if _, ok := seen[s]; !ok {
				seen[s] = struct{}{}
				out = append(out, s)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("change.commitAnchors: rows: %w", err)
		}
		_ = rows.Close()
	}
	return out, nil
}

// topoCommits returns all commits reachable from anchors (following every parent),
// ordered parents-before-children so a rewrite can remap parents as it goes.
func (e *Engine) topoCommits(anchors []string) ([]string, error) {
	visited := map[string]bool{}
	var order []string
	// Iterative post-order DFS: emit a node only after all its parents are emitted.
	type frame struct {
		sha     string
		parents []string
		idx     int
	}
	for _, a := range anchors {
		if visited[a] {
			continue
		}
		c, err := e.git.CommitObject(plumbing.NewHash(a))
		if err != nil {
			return nil, fmt.Errorf("change.topoCommits: load %s: %w", a, err)
		}
		visited[a] = true
		stack := []*frame{{sha: a, parents: parentStrings(c)}}
		for len(stack) > 0 {
			f := stack[len(stack)-1]
			if f.idx < len(f.parents) {
				p := f.parents[f.idx]
				f.idx++
				if p == "" || visited[p] {
					continue
				}
				pc, err := e.git.CommitObject(plumbing.NewHash(p))
				if err != nil {
					return nil, fmt.Errorf("change.topoCommits: load %s: %w", p, err)
				}
				visited[p] = true
				stack = append(stack, &frame{sha: p, parents: parentStrings(pc)})
				continue
			}
			order = append(order, f.sha) // all parents handled → safe to emit
			stack = stack[:len(stack)-1]
		}
	}
	return order, nil
}

func parentStrings(c *object.Commit) []string {
	ps := make([]string, len(c.ParentHashes))
	for i, h := range c.ParentHashes {
		ps[i] = h.String()
	}
	return ps
}

// remapCatalogue rewrites every commit-SHA column from old→new and appends a
// reauthor op, all in one transaction. change.author (free-text) is also retagged
// from OldName to NewName when both are plain (non-glob) names, for consistency.
func (e *Engine) remapCatalogue(spec ReauthorSpec, mapping map[string]string) error {
	before, err := e.viewMap()
	if err != nil {
		return fmt.Errorf("change.remapCatalogue: %w", err)
	}
	cols := []struct{ table, col string }{
		{"line", "tip_commit"}, {"line", "base_commit"},
		{"change", "head_commit"},
		{"tag", "commit_sha"},
		{"stash", "commit_sha"}, {"stash", "base_sha"},
		{"bisect", "good_sha"}, {"bisect", "bad_sha"},
		{"bisect", "current_sha"}, {"bisect", "restore_tip"},
	}
	ts := e.now().UTC().Format(time.RFC3339Nano)
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("change.remapCatalogue: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for old, nw := range mapping {
		if old == nw {
			continue
		}
		for _, c := range cols {
			// table/col are internal constants, not user input.
			if _, err := tx.Exec(
				"UPDATE "+c.table+" SET "+c.col+"=? WHERE "+c.col+"=?", nw, old); err != nil {
				return fmt.Errorf("change.remapCatalogue: %s.%s: %w", c.table, c.col, err)
			}
		}
	}
	// Cosmetic: retag the recorded change author name when it's a plain rename.
	if spec.NewName != "" && spec.OldName != "" && !hasGlob(spec.OldName) {
		if _, err := tx.Exec(`UPDATE change SET author=? WHERE author=?`, spec.NewName, spec.OldName); err != nil {
			return fmt.Errorf("change.remapCatalogue: change.author: %w", err)
		}
	}

	after, err := viewMapTx(tx)
	if err != nil {
		return fmt.Errorf("change.remapCatalogue: %w", err)
	}
	actor := spec.NewName
	if actor == "" {
		actor = "cairn"
	}
	if err := recordOpTx(tx, e.now().UTC(), opReauthor, actor, before, after, ts); err != nil {
		return fmt.Errorf("change.remapCatalogue: record op: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change.remapCatalogue: commit: %w", err)
	}
	return nil
}

func hasGlob(s string) bool {
	for _, r := range s {
		if r == '*' || r == '?' || r == '[' {
			return true
		}
	}
	return false
}
