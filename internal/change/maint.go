package change

import (
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// Maintenance: fsck, count-objects, gc, and the tree walk archive uses.

var hex40 = regexp.MustCompile(`\b[0-9a-f]{40}\b`)

// catalogueShas returns every object id the catalogue refers to: line tips and
// bases, change heads, conflict blobs, stashes, tags, the bisect session, the
// operation log (the old tips `undo` restores), rebase state and the undo
// steps. The refs alone do not cover these — a working snapshot or an
// undo target may be reachable from the catalogue only.
func (e *Engine) catalogueShas() (map[string]bool, error) {
	out := map[string]bool{}
	for _, table := range []string{"line", "change", "conflict", "stash", "tag", "bisect", "operation", "rebase_state", "rebase_history", "op_undo", "embargo"} {
		rows, err := e.db.Query("SELECT * FROM " + table)
		if err != nil {
			return nil, fmt.Errorf("change.catalogueShas: %s: %w", table, err)
		}
		cols, _ := rows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("change.catalogueShas: %w", err)
			}
			for _, v := range vals {
				var s string
				switch t := v.(type) {
				case string:
					s = t
				case []byte:
					s = string(t)
				default:
					continue
				}
				for _, m := range hex40.FindAllString(s, -1) {
					out[m] = true
				}
			}
		}
		_ = rows.Close()
	}
	return out, nil
}

// FsckReport is the outcome of Fsck.
type FsckReport struct {
	Commits  int
	Trees    int
	Problems []string
}

// Fsck verifies that every object reachable from the line tips, open-change
// heads, tags and refs exists (commits, trees and blobs), and that the
// catalogue is self-consistent.
func (e *Engine) Fsck() (FsckReport, error) {
	var rep FsckReport
	bad := func(f string, a ...any) { rep.Problems = append(rep.Problems, fmt.Sprintf(f, a...)) }
	has := func(h plumbing.Hash) bool { return e.git.Storer.HasEncodedObject(h) == nil }

	type root struct{ sha, why string }
	var roots []root
	rows, err := e.db.Query(`SELECT name, tip_commit, base_commit, status FROM line`)
	if err != nil {
		return rep, fmt.Errorf("change.Fsck: %w", err)
	}
	nroots := 0
	for rows.Next() {
		var name, tip, base, status string
		if err := rows.Scan(&name, &tip, &base, &status); err != nil {
			_ = rows.Close()
			return rep, fmt.Errorf("change.Fsck: %w", err)
		}
		if status != "abandoned" && tip != "" {
			roots = append(roots, root{tip, "line " + name})
		}
		if base != "" && !has(plumbing.NewHash(base)) {
			bad("line %s: base commit %s is missing", name, base)
		}
	}
	_ = rows.Close()
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM line WHERE parent_line IS NULL`).Scan(&nroots); err == nil && nroots != 1 {
		bad("catalogue has %d root lines (want exactly 1)", nroots)
	}
	var orphans int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM line l WHERE parent_line IS NOT NULL AND NOT EXISTS (SELECT 1 FROM line p WHERE p.id=l.parent_line)`).Scan(&orphans); err == nil && orphans > 0 {
		bad("%d line(s) point at a parent line that does not exist", orphans)
	}
	crow, err := e.db.Query(`SELECT id, head_commit FROM change WHERE status='open' AND head_commit != ''`)
	if err == nil {
		for crow.Next() {
			var id, head string
			if crow.Scan(&id, &head) == nil {
				roots = append(roots, root{head, "change " + id})
			}
		}
		_ = crow.Close()
	}
	tags, err := e.ListTagInfos()
	if err != nil {
		return rep, err
	}
	for _, t := range tags {
		roots = append(roots, root{t.Commit, "tag " + t.Name})
		if t.Obj != "" && !has(plumbing.NewHash(t.Obj)) {
			bad("tag %s: tag object %s is missing", t.Name, t.Obj)
		}
	}
	// A conflict row's blobs.
	brow, err := e.db.Query(`SELECT id, base_blob, parent_blob, change_blob, marked_blob FROM conflict WHERE status='open'`)
	if err == nil {
		for brow.Next() {
			var id string
			bl := make([]string, 4)
			if brow.Scan(&id, &bl[0], &bl[1], &bl[2], &bl[3]) == nil {
				for _, b := range bl {
					if len(b) == 40 && !has(plumbing.NewHash(b)) {
						bad("conflict %s: blob %s is missing", id, b)
					}
				}
			}
		}
		_ = brow.Close()
	}

	seenCommit := map[plumbing.Hash]bool{}
	seenTree := map[plumbing.Hash]bool{}
	var walkTree func(h plumbing.Hash, why string)
	walkTree = func(h plumbing.Hash, why string) {
		if seenTree[h] {
			return
		}
		seenTree[h] = true
		rep.Trees++
		t, err := e.git.TreeObject(h)
		if err != nil {
			bad("missing tree %s (from %s)", h, why)
			return
		}
		for _, ent := range t.Entries {
			switch ent.Mode {
			case filemode.Dir:
				walkTree(ent.Hash, why)
			case filemode.Submodule:
			default:
				if !has(ent.Hash) {
					bad("missing blob %s (%s in tree %s, from %s)", ent.Hash, ent.Name, h, why)
				}
			}
		}
	}
	for _, r := range roots {
		stack := []plumbing.Hash{plumbing.NewHash(r.sha)}
		for len(stack) > 0 {
			h := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seenCommit[h] {
				continue
			}
			seenCommit[h] = true
			c, err := e.git.CommitObject(h)
			if err != nil {
				bad("missing commit %s (from %s)", h, r.why)
				continue
			}
			rep.Commits++
			walkTree(c.TreeHash, r.why)
			stack = append(stack, c.ParentHashes...)
		}
	}
	sort.Strings(rep.Problems)
	return rep, nil
}

// ObjectCounts mirrors `git count-objects -v`.
type ObjectCounts struct {
	Loose, LooseKiB     int64
	InPack, Packs       int64
	PackKiB             int64
	Garbage, GarbageKiB int64
}

func kib(n int64) int64 { return (n + 1023) / 1024 }

// CountObjects tallies loose and packed objects on disk.
func (e *Engine) CountObjects() (ObjectCounts, error) {
	var c ObjectCounts
	objDir := filepath.Join(e.dir, "objects.git", "objects")
	ents, err := os.ReadDir(objDir)
	if err != nil {
		return c, fmt.Errorf("change.CountObjects: %w", err)
	}
	for _, d := range ents {
		if !d.IsDir() || len(d.Name()) != 2 {
			continue
		}
		fs2, _ := os.ReadDir(filepath.Join(objDir, d.Name()))
		for _, f := range fs2 {
			if info, err := f.Info(); err == nil && !f.IsDir() {
				c.Loose++
				c.LooseKiB += (info.Size() + 4095) / 4096 * 4
			}
		}
	}
	packs, _ := os.ReadDir(filepath.Join(objDir, "pack"))
	for _, f := range packs {
		info, err := f.Info()
		if err != nil {
			continue
		}
		switch {
		case strings.HasSuffix(f.Name(), ".pack"):
			c.Packs++
			c.PackKiB += kib(info.Size())
		case strings.HasSuffix(f.Name(), ".idx"):
			b, err := os.ReadFile(filepath.Join(objDir, "pack", f.Name()))
			if err == nil && len(b) >= 8+256*4 {
				c.InPack += int64(binary.BigEndian.Uint32(b[8+255*4:]))
			}
			c.PackKiB += kib(info.Size())
		case strings.HasPrefix(f.Name(), "tmp_"):
			c.Garbage++
			c.GarbageKiB += kib(info.Size())
		}
	}
	return c, nil
}

// StoreBytes is the size on disk of the object store and catalogue.
func (e *Engine) StoreBytes() int64 {
	var n int64
	for _, p := range []string{filepath.Join(e.dir, "objects.git"), filepath.Join(e.dir, "cairn.db"), filepath.Join(e.dir, "cairn.db-wal"), filepath.Join(e.dir, "cairn.db-shm")} {
		_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if info, err := d.Info(); err == nil {
					n += info.Size()
				}
			}
			return nil
		})
	}
	return n
}

// GcReport is the outcome of Gc.
type GcReport struct {
	Packed      int
	PrunedLoose int
	BeforeBytes int64
	AfterBytes  int64
}

// Gc packs every reachable object into one pack, drops the loose copies and
// the superseded packs, removes unreferenced loose objects older than
// pruneOlder (zero = never prune), then checkpoints, vacuums and analyzes the
// catalogue. "Reachable" includes everything the catalogue refers to (see
// catalogueShas), so an undo target or an un-sealed working snapshot is never
// collected; superseded packs younger than packKeep are kept.
func (e *Engine) Gc(pruneOlder time.Time, packKeep time.Time) (GcReport, error) {
	rep := GcReport{BeforeBytes: e.StoreBytes()}
	if err := e.Export(); err != nil {
		return rep, fmt.Errorf("change.Gc: %w", err)
	}
	st := e.git.Storer
	var roots []plumbing.Hash
	seenRoot := map[plumbing.Hash]bool{}
	add := func(h plumbing.Hash) {
		if h.IsZero() || seenRoot[h] || st.HasEncodedObject(h) != nil {
			return
		}
		seenRoot[h] = true
		roots = append(roots, h)
	}
	iter, err := st.IterReferences()
	if err != nil {
		return rep, fmt.Errorf("change.Gc: %w", err)
	}
	_ = iter.ForEach(func(r *plumbing.Reference) error {
		if r.Type() == plumbing.HashReference && !strings.HasPrefix(r.Name().String(), pushPinRefPrefix) {
			add(r.Hash())
		}
		return nil
	})
	iter.Close()
	shas, err := e.catalogueShas()
	if err != nil {
		return rep, err
	}
	for s := range shas {
		add(plumbing.NewHash(s))
	}
	// A pin of an in-flight push is just a ref the push re-reads: keep it too.
	if pins, err := e.pushPinRefNames(); err == nil {
		for n := range pins {
			if r, err := st.Reference(n); err == nil {
				add(r.Hash())
			}
		}
	}
	objs, err := revlist.Objects(st, roots, nil)
	if err != nil {
		return rep, fmt.Errorf("change.Gc: walking reachable objects: %w", err)
	}
	reach := make(map[plumbing.Hash]bool, len(objs))
	for _, h := range objs {
		reach[h] = true
	}
	pos, ok1 := st.(storer.PackedObjectStorer)
	pfw, ok2 := st.(storer.PackfileWriter)
	los, ok3 := st.(storer.LooseObjectStorer)
	if !ok1 || !ok2 || !ok3 {
		return rep, fmt.Errorf("change.Gc: object store does not support packing")
	}
	oldPacks, err := pos.ObjectPacks()
	if err != nil {
		return rep, fmt.Errorf("change.Gc: %w", err)
	}
	if len(objs) > 0 {
		wc, err := pfw.PackfileWriter()
		if err != nil {
			return rep, fmt.Errorf("change.Gc: %w", err)
		}
		enc := packfile.NewEncoder(wc, st, false)
		newPack, encErr := enc.Encode(objs, 10)
		if cerr := wc.Close(); encErr == nil {
			encErr = cerr
		}
		if encErr != nil {
			return rep, fmt.Errorf("change.Gc: writing pack: %w", encErr)
		}
		rep.Packed = len(objs)
		for _, h := range oldPacks {
			if h != newPack {
				if err := pos.DeleteOldObjectPackAndIndex(h, packKeep); err != nil {
					return rep, fmt.Errorf("change.Gc: %w", err)
				}
			}
		}
	}
	var drop []plumbing.Hash
	_ = los.ForEachObjectHash(func(h plumbing.Hash) error {
		if reach[h] {
			drop = append(drop, h)
		} else if !pruneOlder.IsZero() {
			if t, err := los.LooseObjectTime(h); err == nil && t.Before(pruneOlder) {
				drop = append(drop, h)
				rep.PrunedLoose++
			}
		}
		return nil
	})
	for _, h := range drop {
		if err := los.DeleteLooseObject(h); err != nil {
			return rep, fmt.Errorf("change.Gc: %w", err)
		}
	}
	for _, q := range []string{`PRAGMA wal_checkpoint(TRUNCATE)`, `VACUUM`, `ANALYZE`, `PRAGMA wal_checkpoint(TRUNCATE)`} {
		if _, err := e.db.Exec(q); err != nil {
			return rep, fmt.Errorf("change.Gc: %s: %w", q, err)
		}
	}
	rep.AfterBytes = e.StoreBytes()
	return rep, nil
}

// WalkCommitFiles calls fn for every file in the commit's tree (regular files,
// executables, symlinks; directories and submodules are skipped), in tree
// order, returning the commit's committer time.
func (e *Engine) WalkCommitFiles(commit string, fn func(path string, mode filemode.FileMode, f *object.File) error) (time.Time, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return time.Time{}, fmt.Errorf("change.WalkCommitFiles: %w", err)
	}
	t, err := c.Tree()
	if err != nil {
		return time.Time{}, fmt.Errorf("change.WalkCommitFiles: %w", err)
	}
	err = t.Files().ForEach(func(f *object.File) error { return fn(f.Name, f.Mode, f) })
	return c.Committer.When, err
}

// CommitTime is the commit's committer time.
func (e *Engine) CommitTime(commit string) (time.Time, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return time.Time{}, fmt.Errorf("change.CommitTime: %w", err)
	}
	return c.Committer.When, nil
}
