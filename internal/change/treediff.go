package change

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pmezard/go-difflib/difflib"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// Tree comparison for the inspection commands (log --stat, show, diff --stat,
// format-patch, …). DiffCommits/diffTreesMeta flatten BOTH trees first, which
// is right for one diff of a working change but is O(files) per commit; a
// history walk that summarises thousands of commits cannot pay that. The walk
// here descends only into subtrees whose hashes differ (and that a pathspec
// can match), so its cost follows the size of the CHANGE.

// Pathspec is a set of git-style path limits: a file, a directory (matching
// everything beneath it), or a glob (* ? [..], where * also crosses "/").
// A nil or empty Pathspec matches every path.
type Pathspec struct {
	specs []pathSpecItem
}

type pathSpecItem struct {
	pat    string
	glob   bool
	nowild string // the literal prefix of a glob
}

// NewPathspec builds a Pathspec from tree-root-relative paths.
func NewPathspec(paths []string) *Pathspec {
	ps := &Pathspec{}
	for _, p := range paths {
		p = cleanTreePath(p)
		it := pathSpecItem{pat: p}
		if i := strings.IndexAny(p, "*?["); i >= 0 {
			it.glob, it.nowild = true, p[:i]
		}
		ps.specs = append(ps.specs, it)
	}
	return ps
}

// All reports whether the pathspec places no limit.
func (ps *Pathspec) All() bool {
	if ps == nil || len(ps.specs) == 0 {
		return true
	}
	for _, s := range ps.specs {
		if s.pat == "" {
			return true
		}
	}
	return false
}

// Match reports whether a file path is selected.
func (ps *Pathspec) Match(p string) bool {
	if ps.All() {
		return true
	}
	for _, s := range ps.specs {
		if !s.glob {
			if p == s.pat || strings.HasPrefix(p, s.pat+"/") {
				return true
			}
			continue
		}
		if wildMatch(s.pat, p) {
			return true
		}
		// A glob that matches a leading directory selects everything in it.
		for i := strings.IndexByte(p, '/'); i >= 0; {
			if wildMatch(s.pat, p[:i]) {
				return true
			}
			j := strings.IndexByte(p[i+1:], '/')
			if j < 0 {
				break
			}
			i += 1 + j
		}
	}
	return false
}

// mayContain reports whether directory dir can hold a selected path, so a
// tree walk can skip it otherwise.
func (ps *Pathspec) mayContain(dir string) bool {
	if ps.All() {
		return true
	}
	d := dir + "/"
	for _, s := range ps.specs {
		lit := s.pat
		if s.glob {
			lit = s.nowild
			if strings.HasPrefix(d, lit) || strings.HasPrefix(lit, d) || ps.Match(dir) {
				return true
			}
			continue
		}
		if dir == lit || strings.HasPrefix(dir, lit+"/") || strings.HasPrefix(lit, d) {
			return true
		}
	}
	return false
}

// wildMatch is git's default pathspec glob: * and ? and [..], with * matching
// across "/".
func wildMatch(pat, s string) bool {
	for len(pat) > 0 {
		switch pat[0] {
		case '*':
			for len(pat) > 0 && pat[0] == '*' {
				pat = pat[1:]
			}
			if pat == "" {
				return true
			}
			for i := 0; i <= len(s); i++ {
				if wildMatch(pat, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if s == "" {
				return false
			}
			pat, s = pat[1:], s[1:]
		case '[':
			end := strings.IndexByte(pat[1:], ']')
			if end < 0 || s == "" {
				if s == "" || s[0] != '[' {
					return false
				}
				pat, s = pat[1:], s[1:]
				continue
			}
			ok, err := path.Match(pat[:end+2], s[:1])
			if err != nil || !ok {
				return false
			}
			pat, s = pat[end+2:], s[1:]
		case '\\':
			if len(pat) > 1 {
				pat = pat[1:]
			}
			fallthrough
		default:
			if s == "" || s[0] != pat[0] {
				return false
			}
			pat, s = pat[1:], s[1:]
		}
	}
	return s == ""
}

// TreeChange is one path that differs between two trees. A side whose SHA is
// "" is absent.
type TreeChange struct {
	Path string
	Old  TreeEntry
	New  TreeEntry
}

// Status is git's one-letter status: A(dded), D(eleted), M(odified), or
// T(ype changed, e.g. file to symlink).
func (c TreeChange) Status() byte {
	switch {
	case c.Old.SHA == "":
		return 'A'
	case c.New.SHA == "":
		return 'D'
	case modeKind(c.Old.Mode) != modeKind(c.New.Mode):
		return 'T'
	}
	return 'M'
}

// Reversed swaps the two sides (diff -R).
func (c TreeChange) Reversed() TreeChange { return TreeChange{Path: c.Path, Old: c.New, New: c.Old} }

func modeKind(m EntryMode) int {
	switch m {
	case ModeSymlink:
		return 1
	case ModeGitlink:
		return 2
	}
	return 0
}

// GitMode renders a cairn EntryMode as git's octal mode string.
func GitMode(m EntryMode) string {
	switch m {
	case ModeExecutable:
		return "100755"
	case ModeSymlink:
		return "120000"
	case ModeGitlink:
		return "160000"
	}
	return "100644"
}

var errEnoughChanges = errors.New("enough changes")

// TreeChanges returns the paths that differ between two commits' trees,
// sorted by path, limited to ps (nil = everything). An empty commit name is
// the empty tree, so TreeChanges("", c, nil) lists c's whole tree as added.
func (e *Engine) TreeChanges(commitA, commitB string, ps *Pathspec) ([]TreeChange, error) {
	ta, err := e.treeOfCommit(commitA)
	if err != nil {
		return nil, fmt.Errorf("change.TreeChanges: %w", err)
	}
	tb, err := e.treeOfCommit(commitB)
	if err != nil {
		return nil, fmt.Errorf("change.TreeChanges: %w", err)
	}
	out, err := e.treeChanges(ta, tb, ps, 0)
	if err != nil {
		return nil, fmt.Errorf("change.TreeChanges: %w", err)
	}
	return out, nil
}

func (e *Engine) treeOfCommit(sha string) (plumbing.Hash, error) {
	if sha == "" {
		return plumbing.ZeroHash, nil
	}
	c, err := e.git.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("commit %s: %w", sha, err)
	}
	return c.TreeHash, nil
}

// treeChanges diffs two TREES (zero hash = empty). max > 0 stops after that
// many changes.
func (e *Engine) treeChanges(a, b plumbing.Hash, ps *Pathspec, max int) ([]TreeChange, error) {
	var out []TreeChange
	err := e.walkTreeDiff(a, b, "", ps, max, &out)
	if err != nil && !errors.Is(err, errEnoughChanges) {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// treesDiffer reports whether any path selected by ps differs between two
// trees.
func (e *Engine) treesDiffer(a, b plumbing.Hash, ps *Pathspec) (bool, error) {
	if a == b {
		return false, nil
	}
	// Literal paths (the common `log -- <file>`): two lookups down the path
	// per tree, instead of a merge of every directory level.
	if !ps.All() && ps.literal() && !a.IsZero() && !b.IsZero() {
		for _, s := range ps.specs {
			ea, oka, err := e.treePath(a, s.pat)
			if err != nil {
				return false, err
			}
			eb, okb, err := e.treePath(b, s.pat)
			if err != nil {
				return false, err
			}
			if oka != okb || ea.Hash != eb.Hash || ea.Mode != eb.Mode {
				return true, nil
			}
		}
		return false, nil
	}
	out, err := e.treeChanges(a, b, ps, 1)
	return len(out) > 0, err
}

// literal reports whether no spec is a glob.
func (ps *Pathspec) literal() bool {
	for _, s := range ps.specs {
		if s.glob {
			return false
		}
	}
	return true
}

func (e *Engine) walkTreeDiff(a, b plumbing.Hash, prefix string, ps *Pathspec, max int, out *[]TreeChange) error {
	if a == b {
		return nil
	}
	var ea, eb []object.TreeEntry
	if !a.IsZero() {
		t, err := e.tree(a)
		if err != nil {
			return err
		}
		ea = t.Entries
	}
	if !b.IsZero() {
		t, err := e.tree(b)
		if err != nil {
			return err
		}
		eb = t.Entries
	}
	inB := make(map[string]*object.TreeEntry, len(eb))
	for i := range eb {
		inB[eb[i].Name] = &eb[i]
	}
	add := func(p string, oe, ne *object.TreeEntry) error {
		if !ps.Match(p) {
			return nil
		}
		ch := TreeChange{Path: p}
		if oe != nil {
			ch.Old = TreeEntry{SHA: oe.Hash.String(), Mode: entryModeOf(oe.Mode)}
		}
		if ne != nil {
			ch.New = TreeEntry{SHA: ne.Hash.String(), Mode: entryModeOf(ne.Mode)}
		}
		*out = append(*out, ch)
		if max > 0 && len(*out) >= max {
			return errEnoughChanges
		}
		return nil
	}
	one := func(name string, oe, ne *object.TreeEntry) error {
		if oe != nil && ne != nil && oe.Hash == ne.Hash && oe.Mode == ne.Mode {
			return nil
		}
		p := prefix + name
		oDir := oe != nil && oe.Mode == filemode.Dir
		nDir := ne != nil && ne.Mode == filemode.Dir
		if !oDir && !nDir {
			return add(p, oe, ne)
		}
		// A file replaced by a directory (or the reverse) is a deletion plus
		// additions, never a modification.
		if oe != nil && !oDir {
			if err := add(p, oe, nil); err != nil {
				return err
			}
		}
		if ne != nil && !nDir {
			if err := add(p, nil, ne); err != nil {
				return err
			}
		}
		if !ps.mayContain(p) {
			return nil
		}
		var oh, nh plumbing.Hash
		if oDir {
			oh = oe.Hash
		}
		if nDir {
			nh = ne.Hash
		}
		return e.walkTreeDiff(oh, nh, p+"/", ps, max, out)
	}
	seen := make(map[string]struct{}, len(ea))
	for i := range ea {
		seen[ea[i].Name] = struct{}{}
		if err := one(ea[i].Name, &ea[i], inB[ea[i].Name]); err != nil {
			return err
		}
	}
	for i := range eb {
		if _, ok := seen[eb[i].Name]; ok {
			continue
		}
		if err := one(eb[i].Name, nil, &eb[i]); err != nil {
			return err
		}
	}
	return nil
}

// TreeFile is one entry of a tree listing (ls-tree, ls-files, grep).
type TreeFile struct {
	Path string
	SHA  string
	Mode string // git octal: 100644, 100755, 120000, 160000, 040000
	Type string // blob | tree | commit
}

// ListTree lists the tree named by treeSha under ps. recursive descends into
// subtrees; withTrees also yields the tree entries themselves (ls-tree -t /
// -d). Entries come out in git's tree order.
func (e *Engine) ListTree(treeSha string, ps *Pathspec, recursive, withTrees bool) ([]TreeFile, error) {
	var out []TreeFile
	if err := e.listTree(plumbing.NewHash(treeSha), "", ps, recursive, withTrees, &out); err != nil {
		return nil, fmt.Errorf("change.ListTree: %w", err)
	}
	return out, nil
}

func (e *Engine) listTree(h plumbing.Hash, prefix string, ps *Pathspec, recursive, withTrees bool, out *[]TreeFile) error {
	t, err := e.tree(h)
	if err != nil {
		return err
	}
	for _, ent := range t.Entries {
		p := prefix + ent.Name
		if ent.Mode == filemode.Dir {
			if !ps.mayContain(p) {
				continue
			}
			// Without -r a directory is listed as an entry, unless a pathspec
			// reaches INSIDE it (ls-tree HEAD dir/file), which descends.
			inside := !ps.All() && !ps.Match(p)
			if !recursive && !inside {
				*out = append(*out, TreeFile{Path: p, SHA: ent.Hash.String(), Mode: "040000", Type: "tree"})
				continue
			}
			if withTrees && !inside {
				*out = append(*out, TreeFile{Path: p, SHA: ent.Hash.String(), Mode: "040000", Type: "tree"})
			}
			if err := e.listTree(ent.Hash, p+"/", ps, recursive, withTrees, out); err != nil {
				return err
			}
			continue
		}
		if !ps.Match(p) {
			continue
		}
		typ := "blob"
		if ent.Mode == filemode.Submodule {
			typ = "commit"
		}
		*out = append(*out, TreeFile{Path: p, SHA: ent.Hash.String(), Mode: GitMode(entryModeOf(ent.Mode)), Type: typ})
	}
	return nil
}

// CommitFiles lists every file (blobs and gitlinks) in commit's tree under ps,
// in tree order.
func (e *Engine) CommitFiles(commit string, ps *Pathspec) ([]TreeFile, error) {
	t, err := e.treeOfCommit(commit)
	if err != nil {
		return nil, fmt.Errorf("change.CommitFiles: %w", err)
	}
	if t.IsZero() {
		return nil, nil
	}
	return e.ListTree(t.String(), ps, true, false)
}

// ObjectInfo returns an object's type ("commit", "tree", "blob", "tag") and
// size in bytes — cat-file -t / -s.
func (e *Engine) ObjectInfo(sha string) (typ string, size int64, err error) {
	obj, err := e.git.Storer.EncodedObject(plumbing.AnyObject, plumbing.NewHash(sha))
	if err != nil {
		return "", 0, fmt.Errorf("change.ObjectInfo: %s: %w", sha, ErrNotFound)
	}
	return obj.Type().String(), obj.Size(), nil
}

// ObjectBytes returns an object's raw content (the bytes git hashes, without
// the header) — cat-file <type> / cat-file -p for blobs, commits and tags.
func (e *Engine) ObjectBytes(sha string) ([]byte, error) {
	obj, err := e.git.Storer.EncodedObject(plumbing.AnyObject, plumbing.NewHash(sha))
	if err != nil {
		return nil, fmt.Errorf("change.ObjectBytes: %s: %w", sha, ErrNotFound)
	}
	r, err := obj.Reader()
	if err != nil {
		return nil, fmt.Errorf("change.ObjectBytes: %w", err)
	}
	defer func() { _ = r.Close() }()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return nil, fmt.Errorf("change.ObjectBytes: %w", err)
	}
	return buf.Bytes(), nil
}

// renameSource looks for the path that `to` (present in tree b, absent from
// tree a) was renamed from: a file deleted between a and b with the same
// content, else the most similar one (at least half its lines shared).
func (e *Engine) renameSource(a, b plumbing.Hash, to string) (string, error) {
	target, ok, err := e.treePath(b, to)
	if err != nil || !ok {
		return "", err
	}
	changes, err := e.treeChanges(a, b, nil, 0)
	if err != nil {
		return "", err
	}
	var deleted []TreeChange
	for _, ch := range changes {
		if ch.New.SHA != "" || ch.Old.Mode == ModeGitlink {
			continue
		}
		if ch.Old.SHA == target.Hash.String() {
			return ch.Path, nil
		}
		deleted = append(deleted, ch)
	}
	if len(deleted) == 0 || len(deleted) > 400 {
		return "", nil
	}
	want, err := e.readBlob(target.Hash.String())
	if err != nil || isBinary(want) || len(want) > 1<<20 {
		return "", nil
	}
	wl := splitKeepNL(want)
	best, bestScore := "", 0.5
	for _, ch := range deleted {
		have, err := e.readBlob(ch.Old.SHA)
		if err != nil || isBinary(have) || len(have) > 1<<20 {
			continue
		}
		score := difflib.NewMatcher(splitKeepNL(have), wl).Ratio()
		if path.Base(ch.Path) == path.Base(to) {
			score += 0.01
		}
		if score > bestScore {
			best, bestScore = ch.Path, score
		}
	}
	return best, nil
}

// changeBlobs returns the content of both sides of a change (nil when a side
// is absent). A gitlink has no blob in this store; it reads as the line git
// prints for a submodule.
func (e *Engine) changeBlobs(ch TreeChange) (oldB, newB []byte, err error) {
	read := func(te TreeEntry) ([]byte, error) {
		switch {
		case te.SHA == "":
			return nil, nil
		case te.Mode == ModeGitlink:
			return []byte("Subproject commit " + te.SHA + "\n"), nil
		}
		return e.readBlob(te.SHA)
	}
	if oldB, err = read(ch.Old); err != nil {
		return nil, nil, err
	}
	if newB, err = read(ch.New); err != nil {
		return nil, nil, err
	}
	return oldB, newB, nil
}

// diffOp is one run of a line diff: 'e'qual, 'r'eplace, 'd'elete, 'i'nsert of
// a[i1:i2] against b[j1:j2].
type diffOp struct {
	tag            byte
	i1, i2, j1, j2 int
}

// diffOps computes the line diff of a against b. ka/kb, when non-nil, are the
// keys lines are COMPARED by (whitespace-insensitive diffs); the ops still
// index a and b. The shared head and tail are peeled off before the matcher
// runs, which is what makes a small edit to a large file cheap.
func diffOps(a, b, ka, kb []string) []diffOp {
	if ka == nil {
		ka, kb = a, b
	}
	pre := 0
	for pre < len(ka) && pre < len(kb) && ka[pre] == kb[pre] {
		pre++
	}
	suf := 0
	for suf < len(ka)-pre && suf < len(kb)-pre && ka[len(ka)-1-suf] == kb[len(kb)-1-suf] {
		suf++
	}
	var ops []diffOp
	if pre > 0 {
		ops = append(ops, diffOp{'e', 0, pre, 0, pre})
	}
	ma, mb := ka[pre:len(ka)-suf], kb[pre:len(kb)-suf]
	switch {
	case len(ma) == 0 && len(mb) == 0:
	case len(ma) == 0:
		ops = append(ops, diffOp{'i', pre, pre, pre, pre + len(mb)})
	case len(mb) == 0:
		ops = append(ops, diffOp{'d', pre, pre + len(ma), pre, pre})
	default:
		ops = append(ops, myersOps(ma, mb, pre)...)
	}
	if suf > 0 {
		ops = append(ops, diffOp{'e', len(ka) - suf, len(ka), len(kb) - suf, len(kb)})
	}
	// Merge adjacent equal runs (head + matcher's leading equal, …).
	merged := ops[:0]
	for _, op := range ops {
		if n := len(merged); n > 0 && merged[n-1].tag == 'e' && op.tag == 'e' {
			merged[n-1].i2, merged[n-1].j2 = op.i2, op.j2
			continue
		}
		merged = append(merged, op)
	}
	return merged
}

// myersOps diffs the differing middle of two files (offset by pre lines) with
// Myers' algorithm — the one git uses, so hunks come out as git's do, and
// linear in the size of the CHANGE where difflib's matcher was quadratic in
// the size of the file (a 100-commit `diff --stat` took half a minute). Each
// distinct line is interned as one rune so the diff runs over lines.
func myersOps(a, b []string, pre int) []diffOp {
	ids := map[string]rune{}
	intern := func(lines []string) []rune {
		out := make([]rune, len(lines))
		for i, ln := range lines {
			id, ok := ids[ln]
			if !ok {
				id = rune(len(ids) + 1)
				if id >= 0xD800 {
					id += 0x800 // skip the surrogate range: not valid runes
				}
				ids[ln] = id
			}
			out[i] = id
		}
		return out
	}
	ra, rb := intern(a), intern(b)
	if len(ids) > 0x10F000 {
		// More distinct lines than runes: fall back to the general matcher.
		var ops []diffOp
		for _, oc := range difflib.NewMatcher(a, b).GetOpCodes() {
			ops = append(ops, diffOp{oc.Tag, oc.I1 + pre, oc.I2 + pre, oc.J1 + pre, oc.J2 + pre})
		}
		return ops
	}
	dmp := diffmatchpatch.New()
	dmp.DiffTimeout = 5 * time.Second // past this it settles for a coarser, still correct, diff
	var ops []diffOp
	i, j := pre, pre
	for _, d := range dmp.DiffMainRunes(ra, rb, false) {
		n := utf8.RuneCountInString(d.Text)
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			ops = append(ops, diffOp{'e', i, i + n, j, j + n})
			i, j = i+n, j+n
		case diffmatchpatch.DiffDelete:
			// A delete followed by an insert is one replace run.
			ops = append(ops, diffOp{'d', i, i + n, j, j})
			i += n
		case diffmatchpatch.DiffInsert:
			if k := len(ops); k > 0 && ops[k-1].tag == 'd' && ops[k-1].i2 == i {
				ops[k-1].tag, ops[k-1].j2 = 'r', j+n
			} else {
				ops = append(ops, diffOp{'i', i, i, j, j + n})
			}
			j += n
		}
	}
	return ops
}

// PatchOptions controls how a file's patch is rendered.
type PatchOptions struct {
	Context        int  // lines of context around each hunk
	IgnoreAllSpace bool // -w: lines that differ only in whitespace are equal
	FullIndex      bool // 40-char object names on the index line
	Binary         bool // emit a "GIT binary patch" instead of "Binary files differ"
	StatOnly       bool // count lines, skip the patch text
	reversed       bool // the change was swapped for -R
}

// FilePatch is one file's rendered change.
type FilePatch struct {
	Path    string
	Status  byte   // A | D | M | T
	Header  string // "diff --git" down to the "+++" line
	Body    string // the hunks (or the binary notice / binary patch)
	Binary  bool
	Added   int // lines added (text) …
	Deleted int // … and removed
	OldSize int // blob sizes, for a binary file's stat line
	NewSize int
	// Empty reports a change with nothing to show: under -w, a file whose
	// only differences are whitespace.
	Empty bool
}

// Text is the whole patch for the file.
func (p FilePatch) Text() string { return p.Header + p.Body }

// FilePatch renders one change as a git-format patch (diff --git header,
// index line, unified hunks) and counts its added/removed lines.
func (e *Engine) FilePatch(ch TreeChange, o PatchOptions) (FilePatch, error) {
	oldB, newB, err := e.changeBlobs(ch)
	if err != nil {
		return FilePatch{}, fmt.Errorf("change.FilePatch: %s: %w", ch.Path, err)
	}
	return renderFilePatch(ch, oldB, newB, o), nil
}

func renderFilePatch(ch TreeChange, oldB, newB []byte, o PatchOptions) FilePatch {
	fp := FilePatch{Path: ch.Path, Status: ch.Status(), OldSize: len(oldB), NewSize: len(newB)}
	qa, qb := QuotePath("a/"+ch.Path), QuotePath("b/"+ch.Path)
	if o.reversed {
		// git -R keeps each side's a/ b/ label with its content.
		qa, qb = QuotePath("b/"+ch.Path), QuotePath("a/"+ch.Path)
	}
	var hdr strings.Builder
	fmt.Fprintf(&hdr, "diff --git %s %s\n", qa, qb)
	hasOld, hasNew := ch.Old.SHA != "", ch.New.SHA != ""
	oldMode, newMode := GitMode(ch.Old.Mode), GitMode(ch.New.Mode)
	switch {
	case !hasOld:
		fmt.Fprintf(&hdr, "new file mode %s\n", newMode)
	case !hasNew:
		fmt.Fprintf(&hdr, "deleted file mode %s\n", oldMode)
	case oldMode != newMode:
		fmt.Fprintf(&hdr, "old mode %s\nnew mode %s\n", oldMode, newMode)
	}
	abbrev := func(sha string) string {
		if sha == "" {
			sha = plumbing.ZeroHash.String()
		}
		if o.FullIndex || o.Binary {
			return sha
		}
		return sha[:7]
	}
	contentDiffers := ch.Old.SHA != ch.New.SHA
	if contentDiffers {
		fmt.Fprintf(&hdr, "index %s..%s", abbrev(ch.Old.SHA), abbrev(ch.New.SHA))
		if hasOld && hasNew && oldMode == newMode {
			hdr.WriteString(" " + oldMode)
		}
		hdr.WriteByte('\n')
	}
	from, to := qa, qb
	if !hasOld {
		from = "/dev/null"
	}
	if !hasNew {
		to = "/dev/null"
	}
	if isBinary(oldB) || isBinary(newB) {
		fp.Binary = true
		fp.Header = hdr.String()
		if !o.StatOnly && contentDiffers {
			if o.Binary {
				fp.Body = "GIT binary patch\n" + binaryLiteral(newB) + "\n" + binaryLiteral(oldB) + "\n"
			} else {
				fp.Body = fmt.Sprintf("Binary files %s and %s differ\n", from, to)
			}
		}
		return fp
	}
	a, b := splitKeepNL(oldB), splitKeepNL(newB)
	var ka, kb []string
	if o.IgnoreAllSpace {
		ka, kb = stripSpaceKeys(a), stripSpaceKeys(b)
	}
	ops := diffOps(a, b, ka, kb)
	for _, op := range ops {
		if op.tag != 'e' {
			fp.Deleted += op.i2 - op.i1
			fp.Added += op.j2 - op.j1
		}
	}
	if fp.Added == 0 && fp.Deleted == 0 {
		// Nothing textual: an empty file added/removed, a pure mode change,
		// or (under -w) whitespace only — which git omits entirely.
		fp.Empty = contentDiffers && hasOld && hasNew && oldMode == newMode
		fp.Header = hdr.String()
		return fp
	}
	fmt.Fprintf(&hdr, "--- %s\n+++ %s\n", from, to)
	fp.Header = hdr.String()
	if !o.StatOnly {
		fp.Body = renderHunks(a, b, ops, o.Context)
	}
	return fp
}

func stripSpaceKeys(lines []string) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		out[i] = strings.Map(func(r rune) rune {
			switch r {
			case ' ', '\t', '\r', '\n', '\v', '\f':
				return -1
			}
			return r
		}, ln)
	}
	return out
}

// renderHunks writes unified-diff hunks for ops with n lines of context, in
// git's dialect: a count of 1 is omitted from the header, an empty side
// starts at the line before, and a final line with no newline is marked.
func renderHunks(a, b []string, ops []diffOp, n int) string {
	if n < 0 {
		n = 0
	}
	var sb strings.Builder
	line := func(prefix byte, s string) {
		sb.WriteByte(prefix)
		sb.WriteString(s)
		if !strings.HasSuffix(s, "\n") {
			sb.WriteString("\n\\ No newline at end of file\n")
		}
	}
	rng := func(start, count int) string {
		switch count {
		case 0:
			return fmt.Sprintf("%d,0", start)
		case 1:
			return fmt.Sprintf("%d", start+1)
		}
		return fmt.Sprintf("%d,%d", start+1, count)
	}
	// Group ops into hunks: changes closer than 2n lines share one.
	var group []diffOp
	heading, scanned := "", 0
	flush := func() {
		if len(group) == 0 {
			return
		}
		first, last := group[0], group[len(group)-1]
		// The hunk's section heading: the nearest earlier line of the old
		// file that starts like a declaration (a letter, '_' or '$'), as
		// git's default funcname rule has it.
		for ; scanned < first.i1 && scanned < len(a); scanned++ {
			if c := a[scanned][0]; c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '$' {
				heading = a[scanned]
			}
		}
		fmt.Fprintf(&sb, "@@ -%s +%s @@", rng(first.i1, last.i2-first.i1), rng(first.j1, last.j2-first.j1))
		if h := strings.TrimRight(heading, " \t\r\n"); h != "" {
			if len(h) > 80 {
				h = h[:80]
			}
			sb.WriteString(" " + h)
		}
		sb.WriteByte('\n')
		for _, op := range group {
			if op.tag == 'e' {
				// Context is printed from the NEW side (they differ only
				// under -w, where git shows the post-image too).
				for _, s := range b[op.j1:op.j2] {
					line(' ', s)
				}
				continue
			}
			for _, s := range a[op.i1:op.i2] {
				line('-', s)
			}
			for _, s := range b[op.j1:op.j2] {
				line('+', s)
			}
		}
		group = group[:0]
	}
	for i, op := range ops {
		if op.tag != 'e' {
			group = append(group, op)
			continue
		}
		size := op.i2 - op.i1
		switch {
		case len(group) == 0:
			// Leading context: the last n lines before the first change.
			if i == len(ops)-1 {
				continue
			}
			k := min(size, n)
			if k > 0 {
				group = append(group, diffOp{'e', op.i2 - k, op.i2, op.j2 - k, op.j2})
			}
		case i == len(ops)-1:
			if k := min(size, n); k > 0 {
				group = append(group, diffOp{'e', op.i1, op.i1 + k, op.j1, op.j1 + k})
			}
		case size > 2*n:
			if n > 0 {
				group = append(group, diffOp{'e', op.i1, op.i1 + n, op.j1, op.j1 + n})
			}
			flush()
			if n > 0 {
				group = append(group, diffOp{'e', op.i2 - n, op.i2, op.j2 - n, op.j2})
			}
		default:
			group = append(group, op)
		}
	}
	// A group holding only leading context has no change to show.
	hasChange := false
	for _, op := range group {
		if op.tag != 'e' {
			hasChange = true
		}
	}
	if hasChange {
		flush()
	}
	return sb.String()
}

// QuotePath quotes a path the way git does in diff headers and listings: left
// alone unless it holds a control character, a quote, a backslash or a
// non-ASCII byte, in which case it is C-quoted.
func QuotePath(p string) string {
	need := false
	for i := 0; i < len(p); i++ {
		if c := p[i]; c < 0x20 || c >= 0x7f || c == '"' || c == '\\' {
			need = true
			break
		}
	}
	if !need {
		return p
	}
	var sb strings.Builder
	sb.WriteByte('"')
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '"' || c == '\\':
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\t':
			sb.WriteString(`\t`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&sb, `\%03o`, c)
		default:
			sb.WriteByte(c)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

const base85Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz!#$%&()*+-;<=>?@^_`{|}~"

// binaryLiteral renders data as the "literal" half of a git binary patch:
// zlib-deflated, then base85 in lines of up to 52 bytes, each led by a
// length letter.
func binaryLiteral(data []byte) string {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(data)
	_ = zw.Close()
	raw := z.Bytes()
	var sb strings.Builder
	fmt.Fprintf(&sb, "literal %d\n", len(data))
	for len(raw) > 0 {
		n := min(len(raw), 52)
		chunk := raw[:n]
		raw = raw[n:]
		if n <= 26 {
			sb.WriteByte(byte('A' + n - 1))
		} else {
			sb.WriteByte(byte('a' + n - 27))
		}
		for i := 0; i < n; i += 4 {
			var acc uint32
			for j := 0; j < 4; j++ {
				acc <<= 8
				if i+j < n {
					acc |= uint32(chunk[i+j])
				}
			}
			var enc [5]byte
			for k := 4; k >= 0; k-- {
				enc[k] = base85Alphabet[acc%85]
				acc /= 85
			}
			sb.Write(enc[:])
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// CommitPatches returns the rendered change of every file between two commits
// (a == "" is the empty tree), limited to ps. Files with nothing to show
// (whitespace-only under -w) are dropped.
func (e *Engine) CommitPatches(a, b string, ps *Pathspec, o PatchOptions, reverse bool) ([]FilePatch, error) {
	changes, err := e.TreeChanges(a, b, ps)
	if err != nil {
		return nil, err
	}
	out := make([]FilePatch, 0, len(changes))
	for _, ch := range changes {
		if reverse {
			ch = ch.Reversed()
			o.reversed = true
		}
		fp, err := e.FilePatch(ch, o)
		if err != nil {
			return nil, err
		}
		if fp.Empty {
			continue
		}
		out = append(out, fp)
	}
	return out, nil
}

// FormatStat renders git's --stat block for patches: one " path | N ++--"
// line per file, scaled to width columns, then the summary line. Empty when
// there are no patches.
func FormatStat(patches []FilePatch, width int) string {
	if len(patches) == 0 {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	names := make([]string, len(patches))
	maxLen, maxChange, numberWidth, binWidth := 0, 0, 0, 0
	for i, p := range patches {
		names[i] = QuotePath(p.Path)
		maxLen = max(maxLen, len([]rune(names[i])))
		if p.Binary {
			w := 14 + decimalWidth(p.OldSize) + decimalWidth(p.NewSize)
			binWidth = max(binWidth, w)
			numberWidth = max(numberWidth, 3)
			continue
		}
		maxChange = max(maxChange, p.Added+p.Deleted)
	}
	numberWidth = max(numberWidth, decimalWidth(maxChange))
	graphWidth := maxChange
	if maxChange+4 <= binWidth {
		graphWidth = binWidth - 4
	}
	nameWidth := maxLen
	if nameWidth+numberWidth+6+graphWidth > width {
		if graphWidth > width*3/8-numberWidth-6 {
			graphWidth = width*3/8 - numberWidth - 6
			if graphWidth < 6 {
				graphWidth = 6
			}
		}
		if nameWidth > width-numberWidth-6-graphWidth {
			nameWidth = width - numberWidth - 6 - graphWidth
		} else {
			graphWidth = width - numberWidth - 6 - nameWidth
		}
	}
	scale := func(it int) int {
		if it == 0 || maxChange == 0 {
			return 0
		}
		return 1 + it*(graphWidth-1)/maxChange
	}
	var sb strings.Builder
	for i, p := range patches {
		name := names[i]
		if r := []rune(name); len(r) > nameWidth {
			// Too long: keep the tail, cut at a directory boundary.
			cut := len(r) - nameWidth + 3
			if cut > len(r) {
				cut = len(r)
			}
			tail := string(r[cut:])
			if j := strings.IndexByte(tail, '/'); j >= 0 {
				tail = tail[j:]
			}
			name = "..." + tail
		}
		pad := nameWidth - len([]rune(name))
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintf(&sb, " %s%s |", name, strings.Repeat(" ", pad))
		if p.Binary {
			fmt.Fprintf(&sb, " %*s", numberWidth, "Bin")
			if p.OldSize == 0 && p.NewSize == 0 {
				sb.WriteByte('\n')
				continue
			}
			fmt.Fprintf(&sb, " %d -> %d bytes\n", p.OldSize, p.NewSize)
			continue
		}
		add, del := p.Added, p.Deleted
		fmt.Fprintf(&sb, " %*d", numberWidth, add+del)
		if maxChange > graphWidth {
			total := scale(add + del)
			if total < 2 && add > 0 && del > 0 {
				total = 2
			}
			if add < del {
				add = scale(add)
				del = total - add
			} else {
				del = scale(del)
				add = total - del
			}
		}
		if add+del > 0 {
			sb.WriteByte(' ')
			sb.WriteString(strings.Repeat("+", add))
			sb.WriteString(strings.Repeat("-", del))
		}
		sb.WriteByte('\n')
	}
	sb.WriteString(FormatShortStat(patches))
	return sb.String()
}

// FormatShortStat renders git's --shortstat summary line (with its leading
// space and trailing newline). Empty when there are no patches.
func FormatShortStat(patches []FilePatch) string {
	if len(patches) == 0 {
		return ""
	}
	ins, del := 0, 0
	for _, p := range patches {
		if !p.Binary {
			ins += p.Added
			del += p.Deleted
		}
	}
	plural := func(n int, one, many string) string {
		if n == 1 {
			return one
		}
		return many
	}
	s := fmt.Sprintf(" %d %s changed", len(patches), plural(len(patches), "file", "files"))
	if ins > 0 || del == 0 {
		s += fmt.Sprintf(", %d %s", ins, plural(ins, "insertion(+)", "insertions(+)"))
	}
	if del > 0 || ins == 0 {
		s += fmt.Sprintf(", %d %s", del, plural(del, "deletion(-)", "deletions(-)"))
	}
	return s + "\n"
}

func decimalWidth(n int) int {
	w := 1
	for n >= 10 {
		n /= 10
		w++
	}
	return w
}
