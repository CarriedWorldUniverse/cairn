// Package packread reads git objects straight out of a repository's object
// directory — packfiles through their .idx, and loose objects — without
// go-git.
//
// go-git remains cairn's object store: it writes every object, receives and
// builds packs, and is the fallback for anything this package declines. What
// this package replaces is the READ path of a command-line run, because
// go-git's has a fixed cost that no flag removes: before it can return one
// packed object it decodes the whole .idx into memory (checksumming it with a
// collision-detecting SHA-1) and then builds an offset→hash map of every
// object in the pack. On a 724k-object clone that is ~150ms per process —
// more than the entire budget of `cairn status`, paid again by `log -n 1`.
//
// A .idx is designed not to need any of that: it is a sorted table with a
// fan-out, made to be binary-searched in place. So that is what happens here.
// The .idx and the pack are memory-mapped (or, where they cannot be, read
// through the file handle), a lookup is a binary search over the mapped
// table, and an object is inflated from its offset, with any delta chain
// resolved against a small cache of recently built objects. Nothing is read
// that the command does not ask for, so opening a repository costs a
// directory listing.
//
// Everything is best-effort by construction: Get reports ok=false for an
// object it cannot find OR cannot read, for any reason, and the caller asks
// go-git, which either finds it or produces the authoritative error. This
// package never has to be right about a failure — only about a success, and
// those are checked against go-git object-for-object in the tests.
package packread

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5/plumbing"
)

// maxCacheBytes bounds the object cache; maxCachedObject keeps one huge blob
// from evicting everything else.
const (
	maxCacheBytes   = 96 << 20
	maxCachedObject = 8 << 20
	// maxDeltaDepth is far beyond what git writes (its default chain limit is
	// 50); it only stops a corrupt pack whose deltas form a cycle.
	maxDeltaDepth = 4096
)

var errCorrupt = errors.New("packread: corrupt object")

// Store reads the objects under one objects directory.
type Store struct {
	dir string // …/objects

	// mu guards packs/scanned/closed. Readers hold it shared for the length
	// of a read, so Close cannot unmap a pack from under one.
	mu      sync.RWMutex
	packs   []*pack
	scanned bool
	closed  bool

	cacheMu    sync.Mutex
	cache      map[cacheKey]cacheVal
	cacheOrder []cacheKey // insertion order, oldest first
	cacheHead  int
	cacheBytes int
}

type cacheKey struct {
	p    *pack // nil for a loose object
	off  int64
	hash plumbing.Hash // loose objects only
}

type cacheVal struct {
	typ  plumbing.ObjectType
	data []byte
}

// pack is one packfile and its index.
type pack struct {
	name string // path without extension

	idx     []byte // the whole .idx: mapped, or read into memory
	idxMap  bool   // idx is a mapping (must be unmapped)
	n       int    // objects in the pack
	shas    []byte // n × 20, sorted
	off32   []byte // n × 4
	off64   []byte // 8 × (number of large offsets)
	fanout  [256]uint32
	packLen int64

	openOnce sync.Once
	f        *os.File
	data     []byte // the mapped pack, or nil to read through f
	openErr  error
}

// Open returns a Store for the objects directory dir (…/objects). It touches
// nothing until the first lookup.
func Open(dir string) *Store {
	return &Store{dir: dir, cache: map[cacheKey]cacheVal{}}
}

// Invalidate makes the next lookup list the pack directory again, picking up
// packs written since (a fetch). Packs already open stay open.
func (s *Store) Invalidate() {
	s.mu.Lock()
	s.scanned = false
	s.mu.Unlock()
}

// Close releases every mapping and file handle. The Store answers ok=false to
// everything afterwards.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, p := range s.packs {
		p.close()
	}
	s.packs = nil
	s.cacheMu.Lock()
	s.cache, s.cacheOrder, s.cacheHead, s.cacheBytes = map[cacheKey]cacheVal{}, nil, 0, 0
	s.cacheMu.Unlock()
}

// rlock takes the shared lock with the pack list current, or reports false
// if the store is closed.
func (s *Store) rlock() bool {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return false
	}
	if s.scanned {
		return true
	}
	s.mu.RUnlock()
	s.mu.Lock()
	if !s.closed && !s.scanned {
		s.scan()
	}
	s.mu.Unlock()
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return false
	}
	return true
}

// scan lists objects/pack and opens the index of every pack not already
// known. A pack whose index cannot be used is simply left out: its objects
// are go-git's to serve.
func (s *Store) scan() {
	s.scanned = true
	entries, err := os.ReadDir(filepath.Join(s.dir, "pack"))
	if err != nil {
		return
	}
	known := make(map[string]bool, len(s.packs))
	for _, p := range s.packs {
		known[p.name] = true
	}
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasPrefix(name, "pack-") || !strings.HasSuffix(name, ".idx") {
			continue
		}
		base := filepath.Join(s.dir, "pack", strings.TrimSuffix(name, ".idx"))
		if known[base] {
			continue
		}
		if p := openPack(base); p != nil {
			s.packs = append(s.packs, p)
		}
	}
	// Largest first: the pack a clone delivered holds nearly everything, and
	// the small packs later fetches add are searched after it.
	sort.SliceStable(s.packs, func(i, j int) bool { return s.packs[i].n > s.packs[j].n })
}

// openPack maps base.idx and validates its shape. The pack itself is opened
// on first read.
func openPack(base string) *pack {
	st, err := os.Stat(base + ".pack")
	if err != nil {
		return nil
	}
	f, err := os.Open(base + ".idx")
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	p := &pack{name: base, packLen: st.Size()}
	if p.idx, _ = mapFile(f, fi.Size()); p.idx != nil {
		p.idxMap = true
	} else if p.idx, err = io.ReadAll(f); err != nil {
		return nil
	}
	if !p.parseIdx() {
		p.close()
		return nil
	}
	return p
}

// parseIdx locates the tables of a version-2 index. Version 1 (no magic; git
// stopped writing it in 2008) is declined.
func (p *pack) parseIdx() bool {
	const header = 8 + 256*4
	d := p.idx
	if len(d) < header+40 || !bytes.Equal(d[:4], []byte{0xff, 't', 'O', 'c'}) || binary.BigEndian.Uint32(d[4:8]) != 2 {
		return false
	}
	var prev uint32
	for i := range p.fanout {
		v := binary.BigEndian.Uint32(d[8+4*i:])
		if v < prev {
			return false
		}
		p.fanout[i], prev = v, v
	}
	n := int64(prev)
	if int64(len(d)) < header+n*28+40 {
		return false
	}
	p.n = int(n)
	p.shas = d[header : header+n*20]
	p.off32 = d[header+n*24 : header+n*28]
	p.off64 = d[header+n*28 : len(d)-40]
	return true
}

func (p *pack) close() {
	if p.idxMap {
		unmapFile(p.idx)
	}
	p.idx, p.shas, p.off32, p.off64 = nil, nil, nil, nil
	// Settle the once so a late open cannot race the close.
	p.openOnce.Do(func() { p.openErr = os.ErrClosed })
	if p.data != nil {
		unmapFile(p.data)
		p.data = nil
	}
	if p.f != nil {
		_ = p.f.Close()
		p.f = nil
	}
}

// find returns the pack offset of h, or -1.
func (p *pack) find(h plumbing.Hash) int64 {
	lo, hi := 0, int(p.fanout[h[0]])
	if h[0] > 0 {
		lo = int(p.fanout[h[0]-1])
	}
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		switch c := bytes.Compare(p.shas[mid*20:mid*20+20], h[:]); {
		case c == 0:
			return p.offsetAt(mid)
		case c < 0:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return -1
}

func (p *pack) offsetAt(i int) int64 {
	o := binary.BigEndian.Uint32(p.off32[i*4:])
	if o&0x80000000 == 0 {
		return int64(o)
	}
	j := int(o&0x7fffffff) * 8
	if j+8 > len(p.off64) {
		return -1
	}
	return int64(binary.BigEndian.Uint64(p.off64[j:]))
}

// open opens (and, where possible, maps) the packfile, once.
func (p *pack) open() error {
	p.openOnce.Do(func() {
		f, err := os.Open(p.name + ".pack")
		if err != nil {
			p.openErr = err
			return
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			p.openErr = err
			return
		}
		p.f, p.packLen = f, fi.Size()
		p.data, _ = mapFile(f, fi.Size())
	})
	return p.openErr
}

// objReader is what an object is parsed from: bytes, one at a time for the
// header and as a stream for zlib.
type objReader interface {
	io.Reader
	io.ByteReader
}

var bufPool = sync.Pool{New: func() any { return bufio.NewReaderSize(nil, 32<<10) }}

// reader returns a reader positioned at off, and a function to release it.
func (p *pack) reader(off int64) (objReader, func(), error) {
	if off < 0 || off >= p.packLen {
		return nil, nil, errCorrupt
	}
	if p.data != nil {
		return bytes.NewReader(p.data[off:]), func() {}, nil
	}
	br := bufPool.Get().(*bufio.Reader)
	br.Reset(io.NewSectionReader(p.f, off, p.packLen-off))
	return br, func() { br.Reset(nil); bufPool.Put(br) }, nil
}

const (
	typeOFSDelta = 6
	typeREFDelta = 7
)

// readHeader parses a pack entry header: the type and the inflated size.
func readHeader(r io.ByteReader) (typ int, size int64, err error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	typ = int(b>>4) & 7
	size = int64(b & 0x0f)
	for shift := uint(4); b&0x80 != 0; shift += 7 {
		if shift > 60 {
			return 0, 0, errCorrupt
		}
		if b, err = r.ReadByte(); err != nil {
			return 0, 0, err
		}
		size |= int64(b&0x7f) << shift
	}
	return typ, size, nil
}

// readOFS parses an OFS_DELTA's distance back to its base.
func readOFS(r io.ByteReader) (int64, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	off := int64(b & 0x7f)
	for b&0x80 != 0 {
		if off > 1<<55 {
			return 0, errCorrupt
		}
		if b, err = r.ReadByte(); err != nil {
			return 0, err
		}
		off = (off+1)<<7 | int64(b&0x7f)
	}
	return off, nil
}

var zlibPool sync.Pool

// inflate reads a zlib stream of exactly size inflated bytes from r,
// including its trailing checksum.
func inflate(r io.Reader, size int64) ([]byte, error) {
	if size < 0 || int64(int(size)) != size {
		return nil, errCorrupt
	}
	var zr io.ReadCloser
	if v := zlibPool.Get(); v != nil {
		zr = v.(io.ReadCloser)
		if err := zr.(zlib.Resetter).Reset(r, nil); err != nil {
			return nil, err
		}
	} else {
		var err error
		if zr, err = zlib.NewReader(r); err != nil {
			return nil, err
		}
	}
	out := make([]byte, size)
	if _, err := io.ReadFull(zr, out); err != nil {
		return nil, err
	}
	// One more read reaches the end of the stream, which is where zlib
	// verifies the checksum — and proves the object is not LONGER than its
	// header said.
	var one [1]byte
	if n, err := zr.Read(one[:]); n != 0 || err != io.EOF {
		return nil, errCorrupt
	}
	zlibPool.Put(zr)
	return out, nil
}

// Get returns the type and content of object h. ok is false when h is not
// here or could not be read; the content must not be modified.
func (s *Store) Get(h plumbing.Hash) (typ plumbing.ObjectType, data []byte, ok bool) {
	if !s.rlock() {
		return 0, nil, false
	}
	defer s.mu.RUnlock()
	typ, data, err := s.get(h, 0)
	if err != nil {
		return 0, nil, false
	}
	return typ, data, true
}

// Has reports whether h is in a pack or loose. false means "not that this
// package can see", not "absent".
func (s *Store) Has(h plumbing.Hash) bool {
	if !s.rlock() {
		return false
	}
	defer s.mu.RUnlock()
	for _, p := range s.packs {
		if p.find(h) >= 0 {
			return true
		}
	}
	_, err := os.Stat(s.loosePath(h))
	return err == nil
}

var errNotFound = errors.New("packread: object not found")

func (s *Store) get(h plumbing.Hash, depth int) (plumbing.ObjectType, []byte, error) {
	for _, p := range s.packs {
		if off := p.find(h); off >= 0 {
			return s.readPacked(p, off, depth)
		}
	}
	return s.readLoose(h)
}

// link is one delta on the way down a chain: its inflated instructions and
// where it lives, so the object it builds can be cached under that offset.
type link struct {
	delta []byte
	off   int64
}

// readPacked returns the object at off in p, resolving its delta chain:
// down to the first object that is whole (or cached), then back up applying
// each delta. Every object built on the way up is cached — the objects of a
// pack that share a chain are the ones a walk reads next.
func (s *Store) readPacked(p *pack, off int64, depth int) (plumbing.ObjectType, []byte, error) {
	if err := p.open(); err != nil {
		return 0, nil, err
	}
	var (
		chain []link
		typ   plumbing.ObjectType
		data  []byte
	)
	for cur := off; ; {
		if v, ok := s.cacheGet(cacheKey{p: p, off: cur}); ok {
			typ, data = v.typ, v.data
			break
		}
		if depth+len(chain) > maxDeltaDepth {
			return 0, nil, errCorrupt
		}
		r, release, err := p.reader(cur)
		if err != nil {
			return 0, nil, err
		}
		t, size, err := readHeader(r)
		if err != nil {
			release()
			return 0, nil, err
		}
		switch t {
		case int(plumbing.CommitObject), int(plumbing.TreeObject), int(plumbing.BlobObject), int(plumbing.TagObject):
			data, err = inflate(r, size)
			release()
			if err != nil {
				return 0, nil, err
			}
			typ = plumbing.ObjectType(t)
			s.cachePut(cacheKey{p: p, off: cur}, typ, data)
		case typeOFSDelta:
			back, err := readOFS(r)
			if err != nil || back <= 0 || back > cur {
				release()
				return 0, nil, errCorrupt
			}
			delta, err := inflate(r, size)
			release()
			if err != nil {
				return 0, nil, err
			}
			chain = append(chain, link{delta, cur})
			cur -= back
			continue
		case typeREFDelta:
			var base plumbing.Hash
			if _, err := io.ReadFull(r, base[:]); err != nil {
				release()
				return 0, nil, err
			}
			delta, err := inflate(r, size)
			release()
			if err != nil {
				return 0, nil, err
			}
			chain = append(chain, link{delta, cur})
			// The base is named, not located: it may be in another pack, or
			// loose (a thin pack completed on arrival).
			if typ, data, err = s.get(base, depth+len(chain)); err != nil {
				return 0, nil, err
			}
		default:
			release()
			return 0, nil, errCorrupt
		}
		break
	}
	for i := len(chain) - 1; i >= 0; i-- {
		built, err := applyDelta(data, chain[i].delta)
		if err != nil {
			return 0, nil, err
		}
		data = built
		s.cachePut(cacheKey{p: p, off: chain[i].off}, typ, data)
	}
	return typ, data, nil
}

func (s *Store) loosePath(h plumbing.Hash) string {
	x := hex.EncodeToString(h[:])
	return filepath.Join(s.dir, x[:2], x[2:])
}

// readLoose reads objects/xx/yyyy…: a zlib stream of "<type> <size>\x00"
// followed by the content.
func (s *Store) readLoose(h plumbing.Hash) (plumbing.ObjectType, []byte, error) {
	key := cacheKey{hash: h}
	if v, ok := s.cacheGet(key); ok {
		return v.typ, v.data, nil
	}
	raw, err := os.ReadFile(s.loosePath(h))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil, errNotFound
		}
		return 0, nil, err
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = zr.Close() }()
	br := bufio.NewReader(zr)
	head, err := br.ReadString(0)
	if err != nil {
		return 0, nil, err
	}
	name, sizeStr, found := strings.Cut(strings.TrimSuffix(head, "\x00"), " ")
	if !found {
		return 0, nil, errCorrupt
	}
	typ, err := plumbing.ParseObjectType(name)
	if err != nil {
		return 0, nil, err
	}
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil || size < 0 || int64(int(size)) != size {
		return 0, nil, errCorrupt
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(br, data); err != nil {
		return 0, nil, err
	}
	if _, err := br.ReadByte(); err != io.EOF {
		return 0, nil, errCorrupt
	}
	s.cachePut(key, typ, data)
	return typ, data, nil
}

func (s *Store) cacheGet(k cacheKey) (cacheVal, bool) {
	s.cacheMu.Lock()
	v, ok := s.cache[k]
	s.cacheMu.Unlock()
	return v, ok
}

// cachePut remembers an object, evicting the oldest entries once the cache
// is over its budget.
func (s *Store) cachePut(k cacheKey, typ plumbing.ObjectType, data []byte) {
	if len(data) > maxCachedObject {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if _, ok := s.cache[k]; ok {
		return
	}
	s.cache[k] = cacheVal{typ, data}
	s.cacheOrder = append(s.cacheOrder, k)
	s.cacheBytes += len(data)
	for s.cacheBytes > maxCacheBytes && s.cacheHead < len(s.cacheOrder) {
		old := s.cacheOrder[s.cacheHead]
		s.cacheHead++
		if v, ok := s.cache[old]; ok {
			s.cacheBytes -= len(v.data)
			delete(s.cache, old)
		}
	}
	// Drop the consumed front of the order list once it is mostly dead.
	if s.cacheHead > 1024 && s.cacheHead > len(s.cacheOrder)/2 {
		s.cacheOrder = append([]cacheKey(nil), s.cacheOrder[s.cacheHead:]...)
		s.cacheHead = 0
	}
}

// deltaSize reads one of the two little-endian base-128 sizes a delta starts
// with.
func deltaSize(d []byte) (size int, rest []byte, ok bool) {
	for shift := uint(0); len(d) > 0; shift += 7 {
		if shift > 56 {
			return 0, nil, false
		}
		b := d[0]
		d = d[1:]
		size |= int(b&0x7f) << shift
		if b&0x80 == 0 {
			return size, d, size >= 0
		}
	}
	return 0, nil, false
}

// applyDelta builds an object from its base and a git delta: the base's size,
// the result's size, then instructions that either copy a range of the base
// or insert literal bytes. The result is allocated once at its stated size
// and every instruction is bounds-checked against both ends.
func applyDelta(base, delta []byte) ([]byte, error) {
	baseSize, d, ok := deltaSize(delta)
	if !ok || baseSize != len(base) {
		return nil, errCorrupt
	}
	size, d, ok := deltaSize(d)
	if !ok {
		return nil, errCorrupt
	}
	out := make([]byte, 0, size)
	for len(d) > 0 {
		cmd := d[0]
		d = d[1:]
		switch {
		case cmd&0x80 != 0: // copy from base
			var off, n int
			for i := uint(0); i < 4; i++ {
				if cmd&(1<<i) != 0 {
					if len(d) == 0 {
						return nil, errCorrupt
					}
					off |= int(d[0]) << (8 * i)
					d = d[1:]
				}
			}
			for i := uint(0); i < 3; i++ {
				if cmd&(0x10<<i) != 0 {
					if len(d) == 0 {
						return nil, errCorrupt
					}
					n |= int(d[0]) << (8 * i)
					d = d[1:]
				}
			}
			if n == 0 {
				n = 0x10000
			}
			if off > len(base) || n > len(base)-off || n > size-len(out) {
				return nil, errCorrupt
			}
			out = append(out, base[off:off+n]...)
		case cmd != 0: // insert the next cmd bytes
			n := int(cmd)
			if n > len(d) || n > size-len(out) {
				return nil, errCorrupt
			}
			out = append(out, d[:n]...)
			d = d[n:]
		default:
			return nil, errCorrupt // 0 is reserved
		}
	}
	if len(out) != size {
		return nil, errCorrupt
	}
	return out, nil
}
