package change

// DiffWords marks which of two token lists a shortest edit script deletes
// from a (changedA) and inserts from b (changedB). When the lists are too far
// apart for the exact diff everything is marked changed.
func DiffWords(a, b []string) (changedA, changedB []bool) {
	ids := map[string]int32{}
	conv := func(w []string) []int32 {
		out := make([]int32, len(w))
		for i, s := range w {
			id, ok := ids[s]
			if !ok {
				id = int32(len(ids))
				ids[s] = id
			}
			out[i] = id
		}
		return out
	}
	ia, ib := conv(a), conv(b)
	changedA, changedB = make([]bool, len(a)), make([]bool, len(b))
	if !myersDiff(ia, ib, changedA, changedB) {
		for i := range changedA {
			changedA[i] = true
		}
		for j := range changedB {
			changedB[j] = true
		}
	}
	return changedA, changedB
}
