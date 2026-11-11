package change

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Revision syntax. Every command that takes a commit resolves it here, so the
// spelling is the same everywhere and it is git's:
//
//	<sha> | <abbrev>      full or abbreviated object name (ambiguity is an error)
//	<tag>                 a catalogue tag
//	<line>                a line's SEALED tip
//	<remote>/<branch>     a remote-tracking ref
//	HEAD | @              the sealed tip of the line you stand in (else the root line)
//	<rev>~N  <rev>^  <rev>^N   ancestors, chained: main~3^2
//	<rev>^{commit} <rev>^{tree} <rev>^{}
//	<rev>:<path>          the blob or tree at path (ResolveObject only)
//	<line>@{u}            the line's upstream tracking ref
//
// A line name resolves to its sealed tip, not the open working commit: git's
// HEAD is the last commit, and the working change is cairn's working tree —
// the thing you diff AGAINST a revision, never a revision itself. (Line tips
// live in the catalogue; refs/heads/* only exists as a projection written at
// push time, so it is stale between pushes and is consulted last.)
//
// Ancestry is walked on cairn's own graph code; go-git is only the object
// store here.

// ErrAmbiguousRev is returned when an abbreviated object name matches more
// than one object.
var ErrAmbiguousRev = errors.New("ambiguous revision")

// ErrNoCommits is returned when a line (or HEAD) is named but has no sealed
// commit yet — git's "your current branch does not have any commits yet".
var ErrNoCommits = fmt.Errorf("%w: no commits yet", ErrNotFound)

// SetHeadLine names the line HEAD/@ resolve against. Empty means the root line.
func (e *Engine) SetHeadLine(name string) { e.headLine = name }

// HeadLine returns the line HEAD resolves against: the one set by SetHeadLine,
// else the root line.
func (e *Engine) HeadLine() (Line, error) {
	if e.headLine != "" {
		if l, err := e.LineByName(e.headLine); err == nil {
			return l, nil
		}
	}
	return e.RootLine()
}

// SealedTip returns line's most recent sealed commit ("" when it has none) —
// what HEAD means while standing in that line.
func (e *Engine) SealedTip(line Line) (string, error) { return e.sealedTip(line) }

// ResolveCommit resolves a user-supplied revision (see the syntax above) to
// the full hash of a COMMIT. Annotated tags are peeled.
func (e *Engine) ResolveCommit(rev string) (string, error) {
	h, err := e.resolveCommit(rev)
	if err != nil {
		return "", fmt.Errorf("change.ResolveCommit: %q: %w", rev, err)
	}
	return h.String(), nil
}

// ResolveObject resolves a revision to any object — commit, tree, blob or tag
// — returning its hash and type. It accepts everything ResolveCommit does,
// plus <rev>:<path>, <rev>^{tree} and the names of trees and blobs.
func (e *Engine) ResolveObject(rev string) (sha, typ string, err error) {
	h, t, err := e.resolveObject(rev)
	if err != nil {
		return "", "", fmt.Errorf("change.ResolveObject: %q: %w", rev, err)
	}
	return h.String(), t.String(), nil
}

// PathAt returns the tree entry at path in commit's tree: its hash, whether it
// is a tree, and its cairn mode. ok is false when the path does not exist.
func (e *Engine) PathAt(commit, path string) (entry TreeEntry, isTree, ok bool, err error) {
	c, err := e.git.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return TreeEntry{}, false, false, fmt.Errorf("change.PathAt: commit %s: %w", commit, err)
	}
	te, found, err := e.treePath(c.TreeHash, cleanTreePath(path))
	if err != nil || !found {
		return TreeEntry{}, false, false, err
	}
	return TreeEntry{SHA: te.Hash.String(), Mode: entryModeOf(te.Mode)}, te.Mode == filemode.Dir, true, nil
}

func (e *Engine) resolveObject(rev string) (plumbing.Hash, plumbing.ObjectType, error) {
	// <rev>:<path>. (":/text" message search is not supported; it has no colon
	// form that is also a path.)
	if i := strings.IndexByte(rev, ':'); i >= 0 && !strings.HasPrefix(rev, ":/") {
		base, path := rev[:i], rev[i+1:]
		if base == "" {
			base = "HEAD"
		}
		ch, err := e.resolveCommit(base)
		if err != nil {
			return plumbing.ZeroHash, 0, err
		}
		c, err := e.git.CommitObject(ch)
		if err != nil {
			return plumbing.ZeroHash, 0, err
		}
		path = cleanTreePath(path)
		if path == "" {
			return c.TreeHash, plumbing.TreeObject, nil
		}
		te, ok, err := e.treePath(c.TreeHash, path)
		if err != nil {
			return plumbing.ZeroHash, 0, err
		}
		if !ok {
			return plumbing.ZeroHash, 0, fmt.Errorf("path %q does not exist in %q: %w", path, base, ErrNotFound)
		}
		switch te.Mode {
		case filemode.Dir:
			return te.Hash, plumbing.TreeObject, nil
		case filemode.Submodule:
			return te.Hash, plumbing.CommitObject, nil
		}
		return te.Hash, plumbing.BlobObject, nil
	}
	base, suffix := splitRevSuffix(rev)
	wantTree := false
	if strings.HasSuffix(suffix, "^{tree}") {
		wantTree = true
		suffix = strings.TrimSuffix(suffix, "^{tree}")
		rev = strings.TrimSuffix(rev, "^{tree}")
	}
	// A bare object name may be a tree, a blob or a tag object; only a
	// suffix-free spelling can name one. Refs still win over abbreviations.
	if isHex(base) && len(base) >= 4 && base != "HEAD" {
		if _, isRef, _ := e.resolveRefName(base); !isRef {
			h, t, err := e.resolveHexAny(base)
			switch {
			case err == nil && (t == plumbing.TreeObject || t == plumbing.BlobObject):
				if suffix != "" && suffix != "^{}" && suffix != "^{"+t.String()+"}" {
					return plumbing.ZeroHash, 0, fmt.Errorf("%s is a %s, not a commit", base, t)
				}
				if wantTree && t != plumbing.TreeObject {
					return plumbing.ZeroHash, 0, fmt.Errorf("%s is a %s, not a tree", base, t)
				}
				return h, t, nil
			case err == nil && t == plumbing.TagObject && suffix == "" && !wantTree:
				return h, t, nil
			}
		}
	}
	// An annotated tag named with no suffix stays a tag object (cat-file -t).
	if suffix == "" && !wantTree {
		// TODO(merge): the refs stream adds tag.obj for annotated tags cairn
		// creates; prefer a non-empty obj here (one-line change). Imported
		// tags store the TAG OBJECT hash in commit_sha, handled below.
		if sha, isCatTag := e.catalogueTag(base); isCatTag {
			if h := plumbing.NewHash(sha); isHex(sha) && len(sha) == 40 {
				if _, terr := e.git.TagObject(h); terr == nil {
					return h, plumbing.TagObject, nil
				}
			}
		} else {
			if ref, err := e.git.Reference(plumbing.NewTagReferenceName(base), true); err == nil {
				if _, terr := e.git.TagObject(ref.Hash()); terr == nil {
					return ref.Hash(), plumbing.TagObject, nil
				}
			}
		}
	}
	h, err := e.resolveCommit(rev)
	if err != nil {
		return plumbing.ZeroHash, 0, err
	}
	if wantTree {
		c, err := e.git.CommitObject(h)
		if err != nil {
			return plumbing.ZeroHash, 0, err
		}
		return c.TreeHash, plumbing.TreeObject, nil
	}
	return h, plumbing.CommitObject, nil
}

func (e *Engine) resolveCommit(rev string) (plumbing.Hash, error) {
	if rev == "" {
		return plumbing.ZeroHash, errors.New("empty revision")
	}
	base, suffix := splitRevSuffix(rev)
	h, err := e.resolveName(base)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	for suffix != "" {
		switch {
		case strings.HasPrefix(suffix, "^{"):
			end := strings.IndexByte(suffix, '}')
			if end < 0 {
				return plumbing.ZeroHash, fmt.Errorf("unterminated ^{ in %q", rev)
			}
			switch kind := suffix[2:end]; kind {
			case "", "commit":
			default:
				return plumbing.ZeroHash, fmt.Errorf("^{%s} does not name a commit", kind)
			}
			suffix = suffix[end+1:]
		case suffix[0] == '~' || suffix[0] == '^':
			op := suffix[0]
			j := 1
			for j < len(suffix) && suffix[j] >= '0' && suffix[j] <= '9' {
				j++
			}
			n := 1
			if j > 1 {
				if n, err = strconv.Atoi(suffix[1:j]); err != nil {
					return plumbing.ZeroHash, fmt.Errorf("bad count in %q", rev)
				}
			}
			suffix = suffix[j:]
			if op == '~' {
				for ; n > 0; n-- {
					c, err := e.git.CommitObject(h)
					if err != nil {
						return plumbing.ZeroHash, err
					}
					if len(c.ParentHashes) == 0 {
						return plumbing.ZeroHash, fmt.Errorf("%q reaches past the root commit: %w", rev, ErrNotFound)
					}
					h = c.ParentHashes[0]
				}
				continue
			}
			if n == 0 {
				continue
			}
			c, err := e.git.CommitObject(h)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			if n > len(c.ParentHashes) {
				return plumbing.ZeroHash, fmt.Errorf("%q: commit %s has no parent %d: %w", rev, h.String()[:8], n, ErrNotFound)
			}
			h = c.ParentHashes[n-1]
		default:
			return plumbing.ZeroHash, fmt.Errorf("unrecognised revision suffix %q", suffix)
		}
	}
	return h, nil
}

// splitRevSuffix splits "main~3^2" into the name "main" and the ancestry
// suffix "~3^2". Ref names cannot contain '~' or '^', so the first of either
// starts the suffix.
func splitRevSuffix(rev string) (base, suffix string) {
	if i := strings.IndexAny(rev, "~^"); i >= 0 {
		return rev[:i], rev[i:]
	}
	return rev, ""
}

// resolveName resolves a suffix-free name to a commit hash. Order: HEAD, a
// full object name, tags, lines, remote-tracking and other refs, then an
// abbreviated object name — refs before abbreviations, as git has it.
func (e *Engine) resolveName(name string) (plumbing.Hash, error) {
	if name == "" {
		return plumbing.ZeroHash, errors.New("empty revision")
	}
	if up, ok := cutUpstream(name); ok {
		return e.resolveUpstream(up)
	}
	if name == "HEAD" || name == "@" {
		return e.headCommit()
	}
	if len(name) == 40 && isHex(name) {
		return e.peelCommitHash(plumbing.NewHash(name))
	}
	if h, ok, err := e.resolveRefName(name); err != nil {
		return plumbing.ZeroHash, err
	} else if ok {
		return h, nil
	}
	if len(name) >= 4 && isHex(name) {
		return e.resolveHexCommit(name)
	}
	// Anything else go-git understands (e.g. a describe-style name). Its
	// answer is only trusted when it is a commit.
	if h, err := e.git.ResolveRevision(plumbing.Revision(name)); err == nil {
		return e.peelCommitHash(*h)
	}
	return plumbing.ZeroHash, fmt.Errorf("unknown revision (not a commit, tag, line or remote branch): %w", ErrNotFound)
}

// headCommit is HEAD: the sealed tip of the head line.
func (e *Engine) headCommit() (plumbing.Hash, error) {
	if e.db == nil {
		// A bare engine over a plain git repo (tests): git's own HEAD.
		ref, err := e.git.Head()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		return ref.Hash(), nil
	}
	line, err := e.HeadLine()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	tip, err := e.sealedTip(line)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if tip == "" {
		return plumbing.ZeroHash, fmt.Errorf("line %q has no sealed commit yet (cairn commit seals the working change): %w", line.Name, ErrNoCommits)
	}
	return plumbing.NewHash(tip), nil
}

func cutUpstream(name string) (string, bool) {
	for _, suf := range []string{"@{u}", "@{upstream}"} {
		if strings.HasSuffix(name, suf) {
			return strings.TrimSuffix(name, suf), true
		}
	}
	return "", false
}

// resolveUpstream resolves <line>@{u}: the tracking ref of the same name on
// origin, else on the only remote that has one.
func (e *Engine) resolveUpstream(lineName string) (plumbing.Hash, error) {
	if lineName == "" || lineName == "HEAD" || lineName == "@" {
		line, err := e.HeadLine()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		lineName = line.Name
	}
	if ref, err := e.git.Reference(plumbing.NewRemoteReferenceName("origin", lineName), true); err == nil {
		return ref.Hash(), nil
	}
	rems, err := e.git.Remotes()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	for _, r := range rems {
		if ref, err := e.git.Reference(plumbing.NewRemoteReferenceName(r.Config().Name, lineName), true); err == nil {
			return ref.Hash(), nil
		}
	}
	return plumbing.ZeroHash, fmt.Errorf("line %q has no upstream tracking ref (fetch or push first): %w", lineName, ErrNotFound)
}

// catalogueTag looks a tag up in the catalogue.
func (e *Engine) catalogueTag(name string) (string, bool) {
	if e.db == nil {
		return "", false
	}
	var sha string
	if err := e.db.QueryRow(`SELECT commit_sha FROM tag WHERE name=?`, name).Scan(&sha); err != nil {
		return "", false
	}
	return sha, sha != ""
}

// resolveRefName resolves name as a tag, a line, or a git ref.
func (e *Engine) resolveRefName(name string) (plumbing.Hash, bool, error) {
	short := name
	kind := "" // "" = any, else "tags" | "heads" | "remotes"
	for _, p := range []struct{ prefix, kind string }{
		{"refs/tags/", "tags"}, {"refs/heads/", "heads"}, {"refs/remotes/", "remotes"},
		{"tags/", "tags"}, {"heads/", "heads"}, {"remotes/", "remotes"},
	} {
		if strings.HasPrefix(name, p.prefix) {
			short, kind = strings.TrimPrefix(name, p.prefix), p.kind
			break
		}
	}
	if kind == "" || kind == "tags" {
		if sha, ok := e.catalogueTag(short); ok {
			h, err := e.peelCommitHash(plumbing.NewHash(sha))
			return h, err == nil, err
		}
	}
	if (kind == "" || kind == "heads") && e.db != nil {
		line, err := e.LineByName(short)
		if err == nil {
			tip, terr := e.sealedTip(line)
			if terr != nil {
				return plumbing.ZeroHash, false, terr
			}
			if tip == "" {
				return plumbing.ZeroHash, false, fmt.Errorf("line %q has no sealed commit yet (cairn commit seals the working change): %w", short, ErrNoCommits)
			}
			return plumbing.NewHash(tip), true, nil
		} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
			return plumbing.ZeroHash, false, err
		}
	}
	var candidates []plumbing.ReferenceName
	switch kind {
	case "tags":
		candidates = []plumbing.ReferenceName{plumbing.NewTagReferenceName(short)}
	case "heads":
		candidates = []plumbing.ReferenceName{plumbing.NewBranchReferenceName(short)}
	case "remotes":
		candidates = []plumbing.ReferenceName{
			plumbing.ReferenceName("refs/remotes/" + short),
			plumbing.ReferenceName("refs/remotes/" + short + "/HEAD"),
		}
	default:
		candidates = []plumbing.ReferenceName{
			plumbing.NewTagReferenceName(short),
			plumbing.NewBranchReferenceName(short),
			plumbing.ReferenceName("refs/remotes/" + short),
			plumbing.ReferenceName("refs/remotes/" + short + "/HEAD"),
		}
		if strings.HasPrefix(name, "refs/") {
			candidates = append([]plumbing.ReferenceName{plumbing.ReferenceName(name)}, candidates...)
		}
	}
	for _, rn := range candidates {
		ref, err := e.git.Reference(rn, true)
		if err != nil {
			continue
		}
		h, err := e.peelCommitHash(ref.Hash())
		if err != nil {
			continue
		}
		return h, true, nil
	}
	return plumbing.ZeroHash, false, nil
}

// peelCommitHash follows annotated tags down to the commit they name, and
// rejects an object that is not (and does not peel to) a commit.
func (e *Engine) peelCommitHash(h plumbing.Hash) (plumbing.Hash, error) {
	for i := 0; i < 8; i++ {
		if _, err := e.git.CommitObject(h); err == nil {
			return h, nil
		}
		t, err := e.git.TagObject(h)
		if err != nil {
			break
		}
		h = t.Target
	}
	obj, err := e.git.Storer.EncodedObject(plumbing.AnyObject, h)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("no such object %s: %w", h, ErrNotFound)
	}
	return plumbing.ZeroHash, fmt.Errorf("%s is a %s, not a commit", h.String()[:8], obj.Type())
}

// hashesWithPrefix lists every object whose name starts with the hex prefix.
func (e *Engine) hashesWithPrefix(prefix string) ([]plumbing.Hash, error) {
	prefix = strings.ToLower(prefix)
	st, ok := e.git.Storer.(interface {
		HashesWithPrefix(prefix []byte) ([]plumbing.Hash, error)
	})
	if !ok {
		// No prefix index (an in-memory store): let go-git scan.
		h, err := e.git.ResolveRevision(plumbing.Revision(prefix))
		if err != nil {
			return nil, nil
		}
		return []plumbing.Hash{*h}, nil
	}
	// The index is keyed by whole bytes; an odd-length prefix searches on its
	// even part and filters on the rest.
	raw, err := hex.DecodeString(prefix[:len(prefix)&^1])
	if err != nil {
		return nil, err
	}
	hs, err := st.HashesWithPrefix(raw)
	if err != nil {
		return nil, err
	}
	seen := map[plumbing.Hash]struct{}{}
	out := hs[:0]
	for _, h := range hs {
		if _, dup := seen[h]; dup || !strings.HasPrefix(h.String(), prefix) {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

// resolveHexCommit resolves an abbreviated object name to a commit. Like git,
// it disambiguates by type: among several matches a lone commit wins.
func (e *Engine) resolveHexCommit(prefix string) (plumbing.Hash, error) {
	hs, err := e.hashesWithPrefix(prefix)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	var commits []plumbing.Hash
	for _, h := range hs {
		if c, err := e.peelCommitHash(h); err == nil {
			commits = append(commits, c)
		}
	}
	switch {
	case len(commits) == 1:
		return commits[0], nil
	case len(commits) > 1:
		return plumbing.ZeroHash, ambiguousErr(prefix, commits)
	case len(hs) > 0:
		return plumbing.ZeroHash, fmt.Errorf("%s names an object that is not a commit", prefix)
	}
	return plumbing.ZeroHash, fmt.Errorf("unknown revision (no commit, tag, line or remote branch by that name): %w", ErrNotFound)
}

// resolveHexAny resolves a full or abbreviated object name of any type.
func (e *Engine) resolveHexAny(name string) (plumbing.Hash, plumbing.ObjectType, error) {
	var hs []plumbing.Hash
	if len(name) == 40 {
		hs = []plumbing.Hash{plumbing.NewHash(name)}
	} else {
		var err error
		if hs, err = e.hashesWithPrefix(name); err != nil {
			return plumbing.ZeroHash, 0, err
		}
	}
	switch len(hs) {
	case 0:
		return plumbing.ZeroHash, 0, fmt.Errorf("no object named %s: %w", name, ErrNotFound)
	case 1:
		obj, err := e.git.Storer.EncodedObject(plumbing.AnyObject, hs[0])
		if err != nil {
			return plumbing.ZeroHash, 0, fmt.Errorf("no object named %s: %w", name, ErrNotFound)
		}
		return hs[0], obj.Type(), nil
	}
	return plumbing.ZeroHash, 0, ambiguousErr(name, hs)
}

func ambiguousErr(prefix string, hs []plumbing.Hash) error {
	names := make([]string, 0, len(hs))
	for i, h := range hs {
		if i == 5 {
			names = append(names, "…")
			break
		}
		names = append(names, h.String()[:12])
	}
	return fmt.Errorf("short object name %s is ambiguous (%s) — use more characters: %w",
		prefix, strings.Join(names, ", "), ErrAmbiguousRev)
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// cleanTreePath normalises a user-typed tree path: forward slashes, no
// leading "./" or "/", no trailing "/".
func cleanTreePath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	p = strings.Trim(p, "/")
	if p == "." {
		return ""
	}
	return p
}

// tree returns the decoded tree h, memoized: the inspection walks revisit the
// same subtrees commit after commit.
func (e *Engine) tree(h plumbing.Hash) (*object.Tree, error) {
	e.treeMu.Lock()
	if t, ok := e.trees[h]; ok {
		e.treeMu.Unlock()
		return t, nil
	}
	e.treeMu.Unlock()
	t, err := e.git.TreeObject(h)
	if err != nil {
		return nil, fmt.Errorf("tree %s: %w", h, err)
	}
	e.treeMu.Lock()
	if e.trees == nil || len(e.trees) >= 4096 {
		e.trees = map[plumbing.Hash]*object.Tree{}
	}
	e.trees[h] = t
	e.treeMu.Unlock()
	return t, nil
}

// treePath finds the entry at a "/"-separated path under tree root, decoding
// only the trees along the path — never the whole tree.
func (e *Engine) treePath(root plumbing.Hash, path string) (object.TreeEntry, bool, error) {
	if path == "" {
		return object.TreeEntry{Mode: filemode.Dir, Hash: root}, true, nil
	}
	cur := root
	for {
		name, rest, more := strings.Cut(path, "/")
		t, err := e.tree(cur)
		if err != nil {
			return object.TreeEntry{}, false, err
		}
		var hit *object.TreeEntry
		for i := range t.Entries {
			if t.Entries[i].Name == name {
				hit = &t.Entries[i]
				break
			}
		}
		if hit == nil {
			return object.TreeEntry{}, false, nil
		}
		if !more {
			return *hit, true, nil
		}
		if hit.Mode != filemode.Dir {
			return object.TreeEntry{}, false, nil
		}
		cur, path = hit.Hash, rest
	}
}

// entryModeOf maps a git file mode to cairn's EntryMode.
func entryModeOf(m filemode.FileMode) EntryMode {
	switch m {
	case filemode.Executable:
		return ModeExecutable
	case filemode.Symlink:
		return ModeSymlink
	case filemode.Submodule:
		return ModeGitlink
	}
	return ModeRegular
}

// RevRange is a parsed set of revision arguments: walk everything reachable
// from Include that is not reachable from Exclude.
type RevRange struct {
	Include []string
	Exclude []string
}

// ParseRevisions parses git-style revision arguments into a RevRange:
//
//	a        include a
//	^a       exclude a
//	a..b     exclude a, include b       (either side defaults to HEAD)
//	a...b    include both, exclude their merge bases
//	--not    flips include/exclude for the arguments after it
func (e *Engine) ParseRevisions(args []string) (RevRange, error) {
	var rr RevRange
	not := false
	add := func(sha string, exclude bool) {
		if exclude != not {
			rr.Exclude = append(rr.Exclude, sha)
		} else {
			rr.Include = append(rr.Include, sha)
		}
	}
	side := func(s string) (string, error) {
		if s == "" {
			s = "HEAD"
		}
		return e.ResolveCommit(s)
	}
	for _, a := range args {
		switch {
		case a == "--not":
			not = !not
		case strings.Contains(a, "..."):
			l, r, _ := strings.Cut(a, "...")
			lh, err := side(l)
			if err != nil {
				return rr, err
			}
			rh, err := side(r)
			if err != nil {
				return rr, err
			}
			bases, err := e.mergeBases(lh, rh)
			if err != nil {
				return rr, err
			}
			add(lh, false)
			add(rh, false)
			for _, b := range bases {
				add(b, true)
			}
		case strings.Contains(a, ".."):
			l, r, _ := strings.Cut(a, "..")
			lh, err := side(l)
			if err != nil {
				return rr, err
			}
			rh, err := side(r)
			if err != nil {
				return rr, err
			}
			add(lh, true)
			add(rh, false)
		case strings.HasPrefix(a, "^") && len(a) > 1:
			h, err := e.ResolveCommit(a[1:])
			if err != nil {
				return rr, err
			}
			add(h, true)
		default:
			h, err := e.ResolveCommit(a)
			if err != nil {
				return rr, err
			}
			add(h, false)
		}
	}
	return rr, nil
}

// MergeBases returns every best common ancestor of two revisions, newest
// first — `git merge-base --all`.
func (e *Engine) MergeBases(a, b string) ([]string, error) {
	ha, err := e.ResolveCommit(a)
	if err != nil {
		return nil, err
	}
	hb, err := e.ResolveCommit(b)
	if err != nil {
		return nil, err
	}
	return e.mergeBases(ha, hb)
}

// AllTips returns the commits `--all` starts from: every open line's sealed
// tip, every tag, and every remote-tracking ref — deduplicated, in that order.
func (e *Engine) AllTips() ([]string, error) {
	decs, err := e.refNames()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range decs {
		if !seen[d.SHA] {
			seen[d.SHA] = true
			out = append(out, d.SHA)
		}
	}
	return out, nil
}

// RefName is one name for a commit: a line, a tag, or a remote-tracking ref.
type RefName struct {
	Kind string // "head" | "tag" | "remote"
	Name string
	SHA  string
	Head bool // the line HEAD stands on
}

// RefNames lists every name a commit can be decorated with: open lines (at
// their sealed tips), tags, and remote-tracking refs.
func (e *Engine) RefNames() ([]RefName, error) { return e.refNames() }

func (e *Engine) refNames() ([]RefName, error) {
	var out []RefName
	if e.db == nil {
		// A bare engine over a plain git repo (tests): the refs are the names.
		iter, err := e.git.Storer.IterReferences()
		if err != nil {
			return nil, err
		}
		defer iter.Close()
		head := ""
		if hr, err := e.git.Reference(plumbing.HEAD, false); err == nil && hr.Type() == plumbing.SymbolicReference {
			head = hr.Target().String()
		}
		err = iter.ForEach(func(ref *plumbing.Reference) error {
			if ref.Type() != plumbing.HashReference {
				return nil
			}
			n := ref.Name()
			h, perr := e.peelCommitHash(ref.Hash())
			if perr != nil {
				return nil
			}
			switch {
			case n.IsBranch():
				out = append(out, RefName{Kind: "head", Name: n.Short(), SHA: h.String(), Head: n.String() == head})
			case n.IsTag():
				out = append(out, RefName{Kind: "tag", Name: n.Short(), SHA: h.String()})
			case n.IsRemote():
				out = append(out, RefName{Kind: "remote", Name: n.Short(), SHA: h.String()})
			}
			return nil
		})
		sortRefNames(out)
		return out, err
	}
	headName := ""
	if hl, err := e.HeadLine(); err == nil {
		headName = hl.Name
	}
	rows, err := e.db.Query(`SELECT id, name, tip_commit FROM line WHERE status='open' AND tip_commit != ''`)
	if err != nil {
		return nil, fmt.Errorf("change.RefNames: %w", err)
	}
	var lines []Line
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.ID, &l.Name, &l.TipCommit); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("change.RefNames: %w", err)
		}
		lines = append(lines, l)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("change.RefNames: %w", err)
	}
	for _, l := range lines {
		tip, err := e.sealedTip(l)
		if err != nil {
			return nil, fmt.Errorf("change.RefNames: %w", err)
		}
		if tip != "" {
			out = append(out, RefName{Kind: "head", Name: l.Name, SHA: tip, Head: l.Name == headName})
		}
	}
	tags, err := e.ListTags()
	if err != nil {
		return nil, err
	}
	for _, t := range tags {
		if h, err := e.peelCommitHash(plumbing.NewHash(t.Commit)); err == nil {
			out = append(out, RefName{Kind: "tag", Name: t.Name, SHA: h.String()})
		}
	}
	iter, err := e.git.Storer.IterReferences()
	if err != nil {
		return nil, fmt.Errorf("change.RefNames: %w", err)
	}
	defer iter.Close()
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference || !ref.Name().IsRemote() {
			return nil
		}
		if h, perr := e.peelCommitHash(ref.Hash()); perr == nil {
			out = append(out, RefName{Kind: "remote", Name: ref.Name().Short(), SHA: h.String()})
		}
		return nil
	})
	sortRefNames(out)
	return out, err
}

// sortRefNames orders names heads, remotes, tags — each by name — which is
// the order git enumerates refs/ in.
func sortRefNames(rs []RefName) {
	rank := map[string]int{"head": 0, "remote": 1, "tag": 2}
	sort.SliceStable(rs, func(i, j int) bool {
		if rank[rs[i].Kind] != rank[rs[j].Kind] {
			return rank[rs[i].Kind] < rank[rs[j].Kind]
		}
		return rs[i].Name < rs[j].Name
	})
}
