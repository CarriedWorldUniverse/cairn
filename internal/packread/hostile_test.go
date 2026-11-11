package packread

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"testing"
)

// packHeader returns "PACK" v2 count.
func packHeader(count uint32) []byte {
	b := []byte("PACK\x00\x00\x00\x02")
	return binary.BigEndian.AppendUint32(b, count)
}

// entryHeader encodes a pack entry header for type and a declared size.
func entryHeader(typ int, size uint64) []byte {
	b := byte(typ<<4) | byte(size&0x0f)
	size >>= 4
	var out []byte
	for size > 0 {
		out = append(out, b|0x80)
		b = byte(size & 0x7f)
		size >>= 7
	}
	return append(out, b)
}

// validPack is a well-formed one-blob pack (with a correct trailer).
func validPack(t testing.TB, content string) []byte {
	t.Helper()
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	_, _ = w.Write([]byte(content))
	_ = w.Close()
	p := append(packHeader(1), entryHeader(3, uint64(len(content)))...)
	p = append(p, z.Bytes()...)
	sum := sha1.Sum(p)
	return append(p, sum[:]...)
}

// TestBuildIndexHostileHeaders: the review's 32-42 byte packs used to crash the
// process with an unrecoverable allocation failure; each must now be declined.
func TestBuildIndexHostileHeaders(t *testing.T) {
	zeros := make([]byte, 20)
	cases := map[string][]byte{
		"huge count":            append(packHeader(0xfffffff0), zeros...),
		"huge size":             append(append(append(packHeader(1), entryHeader(3, 1<<46)...), 0x78, 0x9c), zeros...),
		"size just beyond 4GiB": append(append(append(packHeader(1), entryHeader(3, 1<<33)...), 0x78, 0x9c), zeros...),
		"huge delta size":       append(append(append(packHeader(1), entryHeader(6, 1<<46)...), 0x01, 0x78, 0x9c), zeros...),
	}
	for name, pack := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := buildIndex(pack)
			if !errors.Is(err, ErrUnsupportedPack) {
				t.Fatalf("buildIndex = %v, want ErrUnsupportedPack", err)
			}
		})
	}
}

func TestInflateRefusesImplausibleSize(t *testing.T) {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	_, _ = w.Write([]byte("hi"))
	_ = w.Close()
	if _, err := inflate(bytes.NewReader(z.Bytes()), 1<<46, int64(z.Len())); err == nil {
		t.Fatal("inflate accepted a 64 TiB declared size")
	}
	// within the hard cap but far beyond what the input could expand to
	if _, err := inflate(bytes.NewReader(z.Bytes()), 3<<30, int64(z.Len())); err == nil {
		t.Fatal("inflate accepted a size beyond deflate's maximum ratio")
	}
	if got, err := inflate(bytes.NewReader(z.Bytes()), 2, int64(z.Len())); err != nil || string(got) != "hi" {
		t.Fatalf("inflate valid = %q, %v", got, err)
	}
}

func TestApplyDeltaRefusesHugeResult(t *testing.T) {
	// base size 3, result size 2^40, one tiny insert.
	d := []byte{3}
	for n := uint64(1) << 40; ; n >>= 7 {
		if n < 0x80 {
			d = append(d, byte(n))
			break
		}
		d = append(d, byte(n)|0x80)
	}
	d = append(d, 1, 'x')
	if _, err := applyDelta([]byte("abc"), d); err == nil {
		t.Fatal("applyDelta accepted a 1 TiB result from a 5-byte delta")
	}
}

func FuzzIndexPack(f *testing.F) {
	zeros := make([]byte, 20)
	f.Add(validPack(f, "hello"))
	f.Add(validPack(f, ""))
	f.Add(append(packHeader(0xfffffff0), zeros...))
	f.Add(append(append(append(packHeader(1), entryHeader(3, 1<<46)...), 0x78, 0x9c), zeros...))
	f.Add(append(append(append(packHeader(1), entryHeader(6, 1<<46)...), 0x01, 0x78, 0x9c), zeros...))
	f.Add([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x01"))
	f.Fuzz(func(t *testing.T, data []byte) {
		// Must return (index or error) — never panic or exhaust memory.
		_, _, _ = buildIndex(data)
	})
}
