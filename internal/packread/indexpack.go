package packread

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/go-git/go-git/v5/plumbing"
)

// IndexPack builds the version-2 index of the packfile at packPath and
// writes it to idxPath, returning the pack's checksum (the name it should be
// stored under). It is `git index-pack`: the index it writes is, byte for
// byte, the one git writes for the same pack.
//
// A pack received over the wire has no index; building one means working out
// the id of every object in it, and for the four objects in five that are
// stored as deltas that means reconstructing them first. go-git does this on
// one core, hashing with a collision-detecting SHA-1 — 90 of the 98 seconds
// of cloning a 480MB repository, with fifteen cores idle. Here the one part
// that is inherently serial (walking the pack to find where each entry ends,
// which only inflating it can tell) hands the whole objects it meets to a
// pool for hashing as it goes, and the delta chains — independent trees, one
// per base object — are then resolved in parallel. The hash is still the
// collision-detecting one.
//
// It declines (ErrUnsupportedPack) anything it cannot index completely —
// most notably a thin pack, whose deltas name bases that are not in it. The
// caller then falls back to go-git, which is the judge of what is an error.
func IndexPack(packPath, idxPath string) (plumbing.Hash, error) {
	f, err := os.Open(packPath)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	data, _ := mapFile(f, fi.Size())
	if data != nil {
		defer unmapFile(data)
	} else if data, err = io.ReadAll(f); err != nil {
		return plumbing.ZeroHash, err
	}
	idx, checksum, err := buildIndex(data)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if err := os.WriteFile(idxPath, idx, 0o600); err != nil {
		return plumbing.ZeroHash, err
	}
	return checksum, nil
}

// ErrUnsupportedPack means the pack is not one IndexPack can index on its
// own; nothing was written.
var ErrUnsupportedPack = errors.New("packread: pack cannot be indexed here")

// packEntry is one object of a pack being indexed.
type packEntry struct {
	off, end int64         // the entry's bytes in the pack
	dataOff  int64         // where its zlib stream starts
	size     int64         // inflated size (of the delta, for a delta)
	typ      int           // as stored: an object type or a delta kind
	baseOff  int64         // OFS_DELTA: the base entry's offset
	baseRef  plumbing.Hash // REF_DELTA: the base object's id
	crc      uint32
	sha      plumbing.Hash
	claimed  atomic.Bool // a worker has taken this delta to resolve
	done     atomic.Bool // sha is set
}

func buildIndex(data []byte) (idx []byte, checksum plumbing.Hash, err error) {
	if len(data) < 12+20 || string(data[:4]) != "PACK" || binary.BigEndian.Uint32(data[4:8]) != 2 {
		return nil, plumbing.ZeroHash, ErrUnsupportedPack
	}
	count := int(binary.BigEndian.Uint32(data[8:12]))
	if count == 0 {
		return nil, plumbing.ZeroHash, ErrUnsupportedPack
	}
	body := data[:len(data)-20]
	copy(checksum[:], data[len(data)-20:])

	// The trailer covers the whole pack; verified off to the side.
	sumDone := make(chan bool, 1)
	go func() {
		sum := sha1.Sum(body)
		sumDone <- bytes.Equal(sum[:], checksum[:])
	}()

	entries := make([]packEntry, count)
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}

	// Pass 1 — serial: find every entry. Whole objects are handed to the
	// pool to be hashed while the walk carries on.
	type hashJob struct {
		i    int
		data []byte
	}
	jobs := make(chan hashJob, workers*4)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				e := &entries[j.i]
				e.sha = plumbing.ComputeHash(plumbing.ObjectType(e.typ), j.data)
				e.done.Store(true)
			}
		}()
	}
	pos := int64(12)
	var walkErr error
	zr := newInflater()
	for i := 0; i < count; i++ {
		e := &entries[i]
		e.off = pos
		r := bytes.NewReader(body[pos:])
		typ, size, err := readHeader(r)
		if err != nil {
			walkErr = ErrUnsupportedPack
			break
		}
		e.typ, e.size, e.baseOff = typ, size, -1
		switch typ {
		case int(plumbing.CommitObject), int(plumbing.TreeObject), int(plumbing.BlobObject), int(plumbing.TagObject):
		case typeOFSDelta:
			back, err := readOFS(r)
			if err != nil || back <= 0 || back > pos {
				walkErr = ErrUnsupportedPack
			}
			e.baseOff = pos - back
		case typeREFDelta:
			if _, err := io.ReadFull(r, e.baseRef[:]); err != nil {
				walkErr = ErrUnsupportedPack
			}
		default:
			walkErr = ErrUnsupportedPack
		}
		if walkErr != nil {
			break
		}
		e.dataOff = int64(len(body)) - int64(r.Len())
		whole := typ != typeOFSDelta && typ != typeREFDelta
		content, err := zr.inflate(r, size, whole)
		if err != nil {
			walkErr = ErrUnsupportedPack
			break
		}
		pos = int64(len(body)) - int64(r.Len())
		e.end = pos
		e.crc = crc32.ChecksumIEEE(body[e.off:e.end])
		if whole {
			jobs <- hashJob{i, content}
		}
	}
	close(jobs)
	wg.Wait()
	if walkErr == nil && pos != int64(len(body)) {
		walkErr = ErrUnsupportedPack // bytes between the last object and the trailer
	}
	if walkErr != nil {
		<-sumDone
		return nil, plumbing.ZeroHash, walkErr
	}

	// Pass 2 — parallel: resolve the deltas. Each whole object is the root
	// of a tree of deltas built on it (and on each other); the trees are
	// independent, so each worker takes a root and walks its tree.
	byOffset := make(map[int64]int, count)
	for i := range entries {
		byOffset[entries[i].off] = i
	}
	ofsKids := map[int][]int{}           // base entry → its OFS deltas
	refKids := map[plumbing.Hash][]int{} // base id → its REF deltas
	var roots []int
	deltas := 0
	for i := range entries {
		e := &entries[i]
		switch e.typ {
		case typeOFSDelta:
			base, ok := byOffset[e.baseOff]
			if !ok {
				<-sumDone
				return nil, plumbing.ZeroHash, ErrUnsupportedPack
			}
			ofsKids[base] = append(ofsKids[base], i)
			deltas++
		case typeREFDelta:
			refKids[e.baseRef] = append(refKids[e.baseRef], i)
			deltas++
		default:
			roots = append(roots, i)
		}
	}
	if deltas > 0 {
		var (
			next    atomic.Int64
			failed  atomic.Bool
			resolve func(zr *inflater, i int, typ plumbing.ObjectType, content []byte, depth int)
		)
		resolve = func(zr *inflater, i int, typ plumbing.ObjectType, content []byte, depth int) {
			if failed.Load() {
				return
			}
			if depth > maxDeltaDepth {
				failed.Store(true)
				return
			}
			kids := ofsKids[i]
			if rk := refKids[entries[i].sha]; len(rk) > 0 {
				kids = append(append([]int(nil), kids...), rk...)
			}
			for _, k := range kids {
				ke := &entries[k]
				if !ke.claimed.CompareAndSwap(false, true) {
					continue // a REF delta whose base is in the pack twice
				}
				delta, err := zr.inflate(bytes.NewReader(body[ke.dataOff:ke.end]), ke.size, true)
				if err != nil {
					failed.Store(true)
					return
				}
				built, err := applyDelta(content, delta)
				if err != nil {
					failed.Store(true)
					return
				}
				ke.sha = plumbing.ComputeHash(typ, built)
				ke.done.Store(true)
				resolve(zr, k, typ, built, depth+1)
			}
		}
		var wg2 sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg2.Add(1)
			go func() {
				defer wg2.Done()
				zr := newInflater()
				for {
					n := int(next.Add(1)) - 1
					if n >= len(roots) || failed.Load() {
						return
					}
					i := roots[n]
					if len(ofsKids[i]) == 0 && len(refKids[entries[i].sha]) == 0 {
						continue
					}
					e := &entries[i]
					content, err := zr.inflate(bytes.NewReader(body[e.dataOff:e.end]), e.size, true)
					if err != nil {
						failed.Store(true)
						return
					}
					resolve(zr, i, plumbing.ObjectType(e.typ), content, 0)
				}
			}()
		}
		wg2.Wait()
		if failed.Load() {
			<-sumDone
			return nil, plumbing.ZeroHash, ErrUnsupportedPack
		}
	}
	for i := range entries {
		if !entries[i].done.Load() {
			// A delta whose base never turned up: a thin pack.
			<-sumDone
			return nil, plumbing.ZeroHash, ErrUnsupportedPack
		}
	}
	if !<-sumDone {
		return nil, plumbing.ZeroHash, fmt.Errorf("%w: checksum mismatch", ErrUnsupportedPack)
	}

	// The index: fan-out, ids in order, CRCs, offsets, the two checksums.
	order := make([]int, count)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool {
		x, y := &entries[order[a]], &entries[order[b]]
		if c := bytes.Compare(x.sha[:], y.sha[:]); c != 0 {
			return c < 0
		}
		return x.off < y.off
	})
	idx = make([]byte, 0, 8+256*4+count*28+40)
	idx = append(idx, 0xff, 't', 'O', 'c', 0, 0, 0, 2)
	var fanout [256]uint32
	for i := range entries {
		fanout[entries[i].sha[0]]++
	}
	var running uint32
	for _, n := range fanout {
		running += n
		idx = binary.BigEndian.AppendUint32(idx, running)
	}
	for _, i := range order {
		idx = append(idx, entries[i].sha[:]...)
	}
	for _, i := range order {
		idx = binary.BigEndian.AppendUint32(idx, entries[i].crc)
	}
	var large []int64
	for _, i := range order {
		off := entries[i].off
		if off > 0x7fffffff {
			idx = binary.BigEndian.AppendUint32(idx, 0x80000000|uint32(len(large)))
			large = append(large, off)
		} else {
			idx = binary.BigEndian.AppendUint32(idx, uint32(off))
		}
	}
	for _, off := range large {
		idx = binary.BigEndian.AppendUint64(idx, uint64(off))
	}
	idx = append(idx, checksum[:]...)
	sum := sha1.Sum(idx)
	idx = append(idx, sum[:]...)
	return idx, checksum, nil
}

// inflater inflates zlib streams with one reused decompressor and, for
// streams whose output is not wanted, one reused scratch buffer.
type inflater struct {
	zr      io.ReadCloser
	scratch []byte
}

func newInflater() *inflater { return &inflater{scratch: make([]byte, 32<<10)} }

// inflate reads one zlib stream of exactly size inflated bytes from r,
// through to its checksum. With keep it returns the bytes (a fresh slice);
// without, it only verifies the stream and returns nil.
func (z *inflater) inflate(r io.Reader, size int64, keep bool) ([]byte, error) {
	if size < 0 || int64(int(size)) != size {
		return nil, errCorrupt
	}
	if z.zr == nil {
		zr, err := zlib.NewReader(r)
		if err != nil {
			return nil, err
		}
		z.zr = zr
	} else if err := z.zr.(zlib.Resetter).Reset(r, nil); err != nil {
		return nil, err
	}
	var out []byte
	if keep {
		out = make([]byte, size)
		if _, err := io.ReadFull(z.zr, out); err != nil {
			return nil, err
		}
	} else {
		for left := size; left > 0; {
			n, err := z.zr.Read(z.scratch[:min(int64(len(z.scratch)), left)])
			left -= int64(n)
			if err != nil && (err != io.EOF || left > 0) {
				return nil, err
			}
			if n == 0 && err == nil {
				return nil, io.ErrNoProgress
			}
		}
	}
	var one [1]byte
	if n, err := z.zr.Read(one[:]); n != 0 || err != io.EOF {
		return nil, errCorrupt
	}
	return out, nil
}
