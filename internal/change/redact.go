package change

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// redactor builds the redacted projection pushed when privacy flags are set. It
// rewrites the trees of every commit reachable from the pushed refs so withheld
// paths are dropped (omit) or replaced by a placeholder blob (shape-only),
// producing a parallel redacted history. The local object store is only ADDED to
// — real objects are never deleted or repointed by the redactor; the caller
// repoints the pushed refs to the redacted SHAs and restores them afterward.
type redactor struct {
	e           *Engine
	flags       []PrivateEntry
	placeholder string            // hex SHA of the <<private>> blob, written lazily
	treeCache   map[string]string // realTreeSHA -> redactedTreeSHA (memoized)
}

// newRedactor loads the current privacy flags. Returns (nil,false,nil) when no
// flag is set, so callers take the byte-identical fast path.
func (e *Engine) newRedactor() (*redactor, bool, error) {
	flags, err := e.ListPrivate()
	if err != nil {
		return nil, false, err
	}
	if len(flags) == 0 {
		return nil, false, nil
	}
	return &redactor{e: e, flags: flags, treeCache: map[string]string{}}, true, nil
}

func (r *redactor) placeholderSHA() (string, error) {
	if r.placeholder == "" {
		h, err := r.e.writeBlob(privatePlaceholder)
		if err != nil {
			return "", err
		}
		r.placeholder = h.String()
	}
	return r.placeholder, nil
}

// redactTree returns the redacted tree hash for a real tree, applying the privacy
// flags to every path beneath it. Memoized by real tree SHA. When no withheld
// path is present the original tree hash is returned unchanged (so non-private
// subtrees are byte-identical and shared).
func (r *redactor) redactTree(treeHash string) (string, error) {
	if treeHash == "" {
		return treeHash, nil
	}
	if cached, ok := r.treeCache[treeHash]; ok {
		return cached, nil
	}
	entries, err := r.e.readTreeRefs(treeHash)
	if err != nil {
		return "", err
	}
	out := make(map[string]TreeEntry, len(entries))
	changed := false
	for path, ent := range entries {
		mode, private := matchPrivacy(r.flags, path)
		if !private {
			out[path] = ent
			continue
		}
		changed = true
		switch mode {
		case PrivacyOmit:
			// drop entirely — contributes no entry; an emptied folder vanishes
			// because writeTreeRefs only emits dirs that have entries.
		case PrivacyShapeOnly:
			ph, err := r.placeholderSHA()
			if err != nil {
				return "", err
			}
			out[path] = TreeEntry{SHA: ph, Mode: ModeRegular}
		}
	}
	if !changed {
		r.treeCache[treeHash] = treeHash
		return treeHash, nil
	}
	h, err := r.e.writeTreeRefs(out)
	if err != nil {
		return "", err
	}
	r.treeCache[treeHash] = h.String()
	return h.String(), nil
}

// project rewrites every commit reachable from anchors in topological order,
// redacting each commit's tree and rechaining onto redacted parents. It returns
// mapping[realSHA]->redactedSHA; a commit with no withheld content AND unchanged
// parents maps to itself (no new object written).
func (r *redactor) project(anchors []string) (map[string]string, error) {
	order, err := r.e.topoCommits(anchors)
	if err != nil {
		return nil, err
	}
	mapping := make(map[string]string, len(order))
	for _, sha := range order {
		c, err := r.e.git.CommitObject(plumbing.NewHash(sha))
		if err != nil {
			return nil, fmt.Errorf("change.redact: load %s: %w", sha, err)
		}
		newTree, err := r.redactTree(c.TreeHash.String())
		if err != nil {
			return nil, err
		}
		treeChanged := newTree != c.TreeHash.String()
		parentsChanged := false
		newParents := make([]plumbing.Hash, len(c.ParentHashes))
		for i, p := range c.ParentHashes {
			np := mapping[p.String()]
			if np == "" { // parent outside the walked set: keep it
				np = p.String()
			}
			if np != p.String() {
				parentsChanged = true
			}
			newParents[i] = plumbing.NewHash(np)
		}
		if !treeChanged && !parentsChanged {
			mapping[sha] = sha
			continue
		}
		stored, err := r.e.writeRawCommit(redactCommit(c, plumbing.NewHash(newTree), newParents))
		if err != nil {
			return nil, err
		}
		mapping[sha] = stored
	}
	return mapping, nil
}

// redactCommit clones c with a different tree and parents, preserving author,
// committer, message, and signature (the inverse of reauthor's rebuildCommit,
// which preserved the tree and changed identity).
func redactCommit(c *object.Commit, newTree plumbing.Hash, parents []plumbing.Hash) *object.Commit {
	return &object.Commit{
		Author:       c.Author,
		Committer:    c.Committer,
		Message:      c.Message,
		TreeHash:     newTree,
		ParentHashes: parents,
		PGPSignature: c.PGPSignature,
		MergeTag:     c.MergeTag,
		Encoding:     c.Encoding,
	}
}

// redactedMeta rebuilds the cairn-meta commit so every commit-SHA field it
// records (line tip/base, change head) points at the REDACTED commit, not the
// real one — otherwise the meta object references commits that were never pushed
// (a clone breaks) or, worse, makes a real commit reachable. Conflict blob SHAs
// are left as-is: they are not reachable from any pushed ref (side blobs, never
// in a tree), so they do not travel; importMeta records them as text only.
func (r *redactor) redactedMeta(mapping map[string]string) (string, error) {
	doc, err := r.e.buildMetaDoc()
	if err != nil {
		return "", err
	}
	remap := func(sha string) string {
		if sha == "" {
			return sha
		}
		if nw, ok := mapping[sha]; ok {
			return nw
		}
		return sha
	}
	for i := range doc.Lines {
		doc.Lines[i].TipCommit = remap(doc.Lines[i].TipCommit)
		doc.Lines[i].BaseCommit = remap(doc.Lines[i].BaseCommit)
	}
	for i := range doc.Changes {
		doc.Changes[i].HeadCommit = remap(doc.Changes[i].HeadCommit)
	}
	return r.e.writeMetaDoc(doc)
}
