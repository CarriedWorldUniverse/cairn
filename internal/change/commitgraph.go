package change

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-git/go-git/v5/plumbing"
)

// commitGraph is cairn's own commit-graph: for every commit it has been asked
// about (and, always, every ancestor of it) the parents, the root tree, the
// committer time and the generation number — held in memory as dense integer
// ids, and kept on disk between runs.
//
// It exists because history walks are the thing a large repository makes
// slow, and almost none of that cost is the walk. Asking go-git for a commit
// inflates it from the pack and decodes author, committer, message and
// signature to hand back two parent hashes; a walk that crosses 50,000
// commits does that 50,000 times, and `tree` walks once or twice per LINE.
// On a 60k-commit clone with 64 lines that was 15 seconds of decoding. Here a
// commit is a 48-byte struct and its parents are array indices: the same
// `tree` is a few tens of milliseconds, most of it reading the file.
//
// Why this can never be stale: a commit is content-addressed. Its parents,
// tree and committer time are part of what its hash names, and its generation
// follows from its parents', so a record once written is true for ever.
// Nothing a command does — undo, reword, drop, squash, pull, a history
// rewrite — changes a commit; they make NEW commits, with new hashes, which
// are simply not here yet and get added the first time something asks. There
// is no invalidation because there is nothing to invalidate. Records of
// commits no line reaches any more are dead weight, never wrong answers.
//
// The invariant everything below leans on: the graph is CLOSED UNDER
// ANCESTRY. A commit is added only after all its parents (add is a post-order
// walk), so if a commit has an id, every ancestor has one too, with a lower
// id. A walk therefore resolves its starting commits once and never touches
// the object store again.
//
// The file (.cairn/commit-graph) is append-only: a header, then one
// self-describing, checksummed record per commit. Appending is a single
// write(2) under O_APPEND, so two cairn processes extending it at once
// interleave whole batches rather than corrupt each other; a record seen
// twice is skipped on load. A truncated or damaged tail is dropped (those
// commits are just re-read from the object store) and the file is rewritten
// whole the next time it is extended.
type commitGraph struct {
	e    *Engine
	path string // "" keeps the graph in memory only (OpenBare, bare test engines)

	nodes   []cgNode
	parents []int32 // every node's parent ids, back to back; cgNode.pOff indexes it
	index   map[plumbing.Hash]int32

	// rewrite is set when load had to drop part of the file: the next persist
	// replaces the file instead of appending to the damaged one.
	rewrite bool

	// genFrom is the first node whose generation has not been written to the
	// commit_gen table yet (see flushGenerations).
	genFrom int
}

// cgNode is one commit. Parents are g.parents[pOff : pOff+pN].
type cgNode struct {
	hash plumbing.Hash
	tree plumbing.Hash
	time int64 // committer time, unix seconds
	gen  uint32
	pOff uint32
	pN   uint32
}

const (
	commitGraphFile  = "commit-graph"
	commitGraphMagic = "CAIRNCG1"
	// cgFixed is a record's size before its parent hashes and checksum:
	// hash, tree, time, gen, parent count.
	cgFixed = 20 + 20 + 8 + 4 + 2
)

var cgCRC = crc32.MakeTable(crc32.Castagnoli)

// lockGraph returns the engine's commit graph, loaded, with e.cgMu HELD. The
// caller unlocks it when its query is done. Holding the lock for a whole query
// is what lets the walks read the node arrays without a second thought about
// another goroutine growing them; queries are short.
func (e *Engine) lockGraph() *commitGraph {
	e.cgMu.Lock()
	if e.cg == nil {
		g := &commitGraph{e: e, index: map[plumbing.Hash]int32{}}
		// Only an engine that owns its directory (Open) keeps the file. A
		// server engine's directory is a hosted bare repository — not cairn's
		// to write into — and a bare test engine has no directory at all.
		if e.store != nil && e.dir != "" {
			g.path = filepath.Join(e.dir, commitGraphFile)
			g.load()
		}
		g.genFrom = len(g.nodes)
		e.cg = g
	}
	return e.cg
}

// load reads the file into memory. Anything wrong with it — missing, foreign
// header, a record that fails its checksum or names a parent that is not
// already known — ends the load at the last good record: the graph is a cache
// and what is missing is recomputed.
func (g *commitGraph) load() {
	data, err := os.ReadFile(g.path)
	if err != nil {
		return
	}
	if len(data) < len(commitGraphMagic) || string(data[:len(commitGraphMagic)]) != commitGraphMagic {
		g.rewrite = len(data) > 0
		return
	}
	data = data[len(commitGraphMagic):]
	// A record is at least cgFixed+4 bytes; sizing for that is a small
	// over-estimate that saves regrowing the arrays while loading.
	est := len(data)/(cgFixed+4+10) + 1
	g.nodes = make([]cgNode, 0, est)
	g.parents = make([]int32, 0, est+est/8)
	g.index = make(map[plumbing.Hash]int32, est)
	for len(data) > 0 {
		if len(data) < cgFixed+4 {
			g.rewrite = true
			return
		}
		np := int(binary.LittleEndian.Uint16(data[cgFixed-2:]))
		size := cgFixed + np*20 + 4
		if len(data) < size {
			g.rewrite = true
			return
		}
		rec := data[:size-4]
		if crc32.Checksum(rec, cgCRC) != binary.LittleEndian.Uint32(data[size-4:size]) {
			g.rewrite = true
			return
		}
		data = data[size:]

		var n cgNode
		copy(n.hash[:], rec[0:20])
		if _, dup := g.index[n.hash]; dup {
			continue // appended by two processes at once
		}
		copy(n.tree[:], rec[20:40])
		n.time = int64(binary.LittleEndian.Uint64(rec[40:48]))
		n.gen = binary.LittleEndian.Uint32(rec[48:52])
		n.pOff = uint32(len(g.parents))
		n.pN = uint32(np)
		var highest uint32
		ok := true
		for i := 0; i < np; i++ {
			var p plumbing.Hash
			copy(p[:], rec[cgFixed+i*20:])
			pi, known := g.index[p]
			if !known {
				ok = false
				break
			}
			if pg := g.nodes[pi].gen; pg > highest {
				highest = pg
			}
			g.parents = append(g.parents, pi)
		}
		if !ok || n.gen != highest+1 {
			// Not closed under ancestry, or a generation that does not follow
			// from the parents': this record cannot be trusted, nor anything
			// after it.
			g.parents = g.parents[:n.pOff]
			g.rewrite = true
			return
		}
		g.index[n.hash] = int32(len(g.nodes))
		g.nodes = append(g.nodes, n)
	}
}

// parentsOf returns the ids of id's parents, in commit order.
func (g *commitGraph) parentsOf(id int32) []int32 {
	n := &g.nodes[id]
	return g.parents[n.pOff : n.pOff+n.pN]
}

// id returns h's id, first adding h and any ancestors not yet in the graph
// (read from the object store) and persisting them.
func (g *commitGraph) id(h plumbing.Hash) (int32, error) {
	if i, ok := g.index[h]; ok {
		return i, nil
	}
	start := len(g.nodes)
	err := g.extend(h)
	// Whatever was added before a failure is still closed under ancestry and
	// still true, so it is kept either way.
	if perr := g.persist(start); err == nil {
		err = perr
	}
	if err != nil {
		return 0, err
	}
	return g.index[h], nil
}

// rawCommit is the part of a commit object the graph records.
type rawCommit struct {
	tree    plumbing.Hash
	parents []plumbing.Hash
	time    int64
}

// extend adds h and its unknown ancestors, parents first (iterative
// post-order: a commit is numbered once all its parents are).
func (g *commitGraph) extend(h plumbing.Hash) error {
	pending := map[plumbing.Hash]*rawCommit{}
	stack := []plumbing.Hash{h}
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		if _, ok := g.index[top]; ok {
			stack = stack[:len(stack)-1]
			continue
		}
		c, ok := pending[top]
		if !ok {
			var err error
			if c, err = g.e.readRawCommit(top); err != nil {
				return fmt.Errorf("commit %s: %w", top, err)
			}
			pending[top] = c
		}
		var highest uint32
		ready := true
		for _, p := range c.parents {
			pi, known := g.index[p]
			if !known {
				ready = false
				stack = append(stack, p)
				continue
			}
			if pg := g.nodes[pi].gen; pg > highest {
				highest = pg
			}
		}
		if !ready {
			continue
		}
		if len(g.nodes) >= math.MaxInt32 {
			return errors.New("commit graph is full")
		}
		if len(c.parents) > math.MaxUint16 {
			return fmt.Errorf("commit %s has %d parents", top, len(c.parents))
		}
		n := cgNode{hash: top, tree: c.tree, time: c.time, gen: highest + 1,
			pOff: uint32(len(g.parents)), pN: uint32(len(c.parents))}
		for _, p := range c.parents {
			g.parents = append(g.parents, g.index[p])
		}
		g.index[top] = int32(len(g.nodes))
		g.nodes = append(g.nodes, n)
		delete(pending, top)
		stack = stack[:len(stack)-1]
	}
	return nil
}

// readRawCommit reads commit h and picks out its tree, parents and committer
// time, without decoding the rest (identities, message, signature) — which is
// most of what decoding a commit costs, and none of what a graph needs.
func (e *Engine) readRawCommit(h plumbing.Hash) (*rawCommit, error) {
	obj, err := e.git.Storer.EncodedObject(plumbing.CommitObject, h)
	if err != nil {
		return nil, err
	}
	r, err := obj.Reader()
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		return nil, err
	}
	return parseRawCommit(data)
}

// parseRawCommit reads the header lines of a commit object. Headers end at
// the first empty line; a continuation line (a multi-line gpgsig or mergetag)
// starts with a space, so it can never be mistaken for a header of its own.
func parseRawCommit(data []byte) (*rawCommit, error) {
	c := &rawCommit{time: math.MinInt64}
	sawTree := false
	for len(data) > 0 {
		line := data
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line, data = data[:i], data[i+1:]
		} else {
			data = nil
		}
		if len(line) == 0 {
			break
		}
		switch {
		case bytes.HasPrefix(line, []byte("tree ")):
			h, ok := parseHexHash(line[5:])
			if !ok {
				return nil, errors.New("malformed tree header")
			}
			c.tree, sawTree = h, true
		case bytes.HasPrefix(line, []byte("parent ")):
			h, ok := parseHexHash(line[7:])
			if !ok {
				return nil, errors.New("malformed parent header")
			}
			c.parents = append(c.parents, h)
		case bytes.HasPrefix(line, []byte("committer ")):
			c.time = signatureTime(line[10:])
		}
	}
	if !sawTree {
		return nil, errors.New("commit has no tree header")
	}
	return c, nil
}

// parseHexHash decodes a 40-character hex object id.
func parseHexHash(b []byte) (plumbing.Hash, bool) {
	b = bytes.TrimSpace(b)
	if len(b) != 40 {
		return plumbing.ZeroHash, false
	}
	var h plumbing.Hash
	for i := 0; i < 20; i++ {
		hi, ok1 := unhex(b[2*i])
		lo, ok2 := unhex(b[2*i+1])
		if !ok1 || !ok2 {
			return plumbing.ZeroHash, false
		}
		h[i] = hi<<4 | lo
	}
	return h, true
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// signatureTime reads the unix seconds out of "Name <email> 1700000000 +0100".
// A signature with no readable time sorts before every real one, as go-git's
// zero time does.
func signatureTime(sig []byte) int64 {
	i := bytes.LastIndexByte(sig, '>')
	if i < 0 {
		return math.MinInt64
	}
	fields := bytes.Fields(sig[i+1:])
	if len(fields) == 0 {
		return math.MinInt64
	}
	t, err := strconv.ParseInt(string(fields[0]), 10, 64)
	if err != nil {
		return math.MinInt64
	}
	return t
}

// appendRecord encodes node i as a file record.
func (g *commitGraph) appendRecord(buf []byte, i int) []byte {
	n := &g.nodes[i]
	start := len(buf)
	buf = append(buf, n.hash[:]...)
	buf = append(buf, n.tree[:]...)
	buf = binary.LittleEndian.AppendUint64(buf, uint64(n.time))
	buf = binary.LittleEndian.AppendUint32(buf, n.gen)
	buf = binary.LittleEndian.AppendUint16(buf, uint16(n.pN))
	for _, p := range g.parents[n.pOff : n.pOff+n.pN] {
		buf = append(buf, g.nodes[p].hash[:]...)
	}
	return binary.LittleEndian.AppendUint32(buf, crc32.Checksum(buf[start:], cgCRC))
}

// persist appends the nodes added since start to the graph file. A failure
// only costs a recomputation next run, but it is still reported: a repository
// that cannot be written to is worth knowing about.
//
// It deliberately does not touch the catalogue. A graph is extended wherever
// a history question happens to be asked, including in the middle of a
// catalogue transaction (an import maps branches onto trunk inside one), and
// a second writer there would wait out SQLite's busy timeout against its own
// process. The commit_gen rows are written by flushGenerations instead.
func (g *commitGraph) persist(start int) error {
	if start >= len(g.nodes) {
		return nil
	}
	if g.path == "" {
		return nil
	}
	if g.rewrite {
		return g.rewriteFile()
	}
	buf := make([]byte, 0, (len(g.nodes)-start)*(cgFixed+4+24)+len(commitGraphMagic))
	f, err := os.OpenFile(g.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("change.commitGraph: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("change.commitGraph: %w", err)
	}
	if st.Size() == 0 {
		buf = append(buf, commitGraphMagic...)
	}
	for i := start; i < len(g.nodes); i++ {
		buf = g.appendRecord(buf, i)
	}
	// One write: under O_APPEND the whole batch lands contiguously even if
	// another process is appending too.
	_, err = f.Write(buf)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("change.commitGraph: %w", err)
	}
	return nil
}

// rewriteFile replaces the file with the whole in-memory graph, via a
// temporary file and a rename so a reader never sees half of it.
func (g *commitGraph) rewriteFile() error {
	buf := make([]byte, 0, len(g.nodes)*(cgFixed+4+24)+len(commitGraphMagic))
	buf = append(buf, commitGraphMagic...)
	for i := range g.nodes {
		buf = g.appendRecord(buf, i)
	}
	tmp, err := os.CreateTemp(filepath.Dir(g.path), commitGraphFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("change.commitGraph: %w", err)
	}
	_, err = tmp.Write(buf)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), g.path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("change.commitGraph: %w", err)
	}
	g.rewrite = false
	return nil
}

// flushGenerations writes the generations of nodes added since the last flush
// to the commit_gen table — the catalogue's own record of them, kept as it
// always was. It is called where the generation cache used to be written (the
// end of a generation-ordered query) and when the engine closes, never from
// inside a walk.
func (g *commitGraph) flushGenerations() error {
	if g.genFrom >= len(g.nodes) {
		return nil
	}
	if err := g.e.persistGenerations(g.nodes[g.genFrom:]); err != nil {
		return err
	}
	g.genFrom = len(g.nodes)
	return nil
}
