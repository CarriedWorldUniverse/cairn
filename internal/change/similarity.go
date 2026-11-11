package change

import "sort"

// git's rename-detection similarity (diffcore-delta.c / diffcore-rename.c),
// as closely as practical: each file is cut into spans that end at a newline
// or after 64 bytes, every span is hashed, and the score is the number of
// bytes in spans the two files share, over the size of the larger file.

const (
	maxScore    = 60000 // git's MAX_SCORE: 100%
	renameScore = 30000 // git's default minimum: 50%, inclusive
	spanHash    = 107927
)

// spanHashes returns hash -> total bytes of the spans with that hash. In a
// text file a CR before an LF is ignored.
func spanHashes(buf []byte, text bool) map[uint32]int {
	out := map[uint32]int{}
	var accum1, accum2 uint32
	n := 0
	flush := func() {
		out[(accum1+accum2*0x61)%spanHash] += n
		n = 0
		accum1, accum2 = 0, 0
	}
	for i := 0; i < len(buf); i++ {
		c := uint32(buf[i])
		old1 := accum1
		if text && c == '\r' && i+1 < len(buf) && buf[i+1] == '\n' {
			continue
		}
		accum1 = (accum1 << 7) ^ (accum2 >> 25)
		accum2 = (accum2 << 7) ^ (old1 >> 25)
		accum1 += c
		n++
		if n < 64 && c != '\n' {
			continue
		}
		flush()
	}
	if n > 0 {
		flush()
	}
	return out
}

// similarity scores how much of dst was copied from src, 0..maxScore, as git
// does for rename detection (0 for sizes git refuses to compare).
func similarity(src, dst []byte) int {
	maxSize, baseSize := len(src), len(dst)
	if baseSize > maxSize {
		maxSize, baseSize = baseSize, maxSize
	}
	if maxSize == 0 {
		return 0
	}
	// git does not consider edits that change the size so drastically.
	if maxSize*(maxScore-renameScore) < (maxSize-baseSize)*maxScore {
		return 0
	}
	text := !isBinary(src) && !isBinary(dst)
	sh, dh := spanHashes(src, text), spanHashes(dst, text)
	keys := make([]uint32, 0, len(sh))
	for k := range sh {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	copied := 0
	for _, k := range keys {
		if dc, ok := dh[k]; ok {
			copied += min(sh[k], dc)
		}
	}
	return copied * maxScore / maxSize
}
