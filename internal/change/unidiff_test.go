package change

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/pmezard/go-difflib/difflib"
)

// unifiedDiff replaced go-difflib on the diff path for speed, on the promise
// that the output is the same. This is that promise, checked against
// go-difflib itself: small alphabets (so matches tie and the tie-breaking
// matters), repeated lines, files past the 200-line mark where difflib stops
// seeding matches on popular lines, files with and without a final newline,
// empty sides.
func TestUnifiedDiffMatchesDifflib(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	gen := func(n, alphabet int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("line %d\n", rng.Intn(alphabet))
		}
		return out
	}
	mutate := func(a []string, edits, alphabet int) []string {
		b := append([]string(nil), a...)
		for e := 0; e < edits; e++ {
			switch op := rng.Intn(4); {
			case op == 0 && len(b) > 0: // delete a run
				i := rng.Intn(len(b))
				n := min(1+rng.Intn(4), len(b)-i)
				b = append(b[:i], b[i+n:]...)
			case op == 1: // insert a run
				i := rng.Intn(len(b) + 1)
				b = append(b[:i], append(gen(1+rng.Intn(4), alphabet), b[i:]...)...)
			case op == 2 && len(b) > 0: // change a line
				b[rng.Intn(len(b))] = fmt.Sprintf("line %d\n", rng.Intn(alphabet))
			case op == 3 && len(b) > 1: // move a run
				i := rng.Intn(len(b))
				n := min(1+rng.Intn(6), len(b)-i)
				run := append([]string(nil), b[i:i+n]...)
				b = append(b[:i], b[i+n:]...)
				j := rng.Intn(len(b) + 1)
				b = append(b[:j], append(run, b[j:]...)...)
			}
		}
		return b
	}
	check := func(a, b []string, ctx int) {
		t.Helper()
		want, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A: a, B: b, FromFile: "old/f", ToFile: "new/f", Context: ctx,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := unifiedDiff(a, b, "old/f", "new/f", ctx); got != want {
			t.Fatalf("unifiedDiff differs from go-difflib (len(a)=%d len(b)=%d ctx=%d)\n--- want\n%s\n--- got\n%s\n--- a\n%s--- b\n%s",
				len(a), len(b), ctx, want, got, strings.Join(a, ""), strings.Join(b, ""))
		}
	}

	check(nil, nil, 3)
	check(nil, []string{"x\n"}, 3)
	check([]string{"x\n"}, nil, 3)
	check([]string{"x\n"}, []string{"x\n"}, 3)
	check([]string{"a\n", "b\n"}, []string{"a\n", "c\n", "a\n", "b\n"}, 3) // difflib's own "ab → acab"
	check(splitKeepNL([]byte("a\nb\nc")), splitKeepNL([]byte("a\nb\nc\n")), 3)
	check(splitKeepNL([]byte("a\nB\nc")), splitKeepNL([]byte("a\nb\nc")), 0)

	for iter := 0; iter < 1500; iter++ {
		// Mostly small files over a small alphabet; every so often a file
		// past 200 lines, some with an alphabet small enough that most
		// lines are "popular".
		n, alphabet := rng.Intn(40), 2+rng.Intn(12)
		switch iter % 10 {
		case 0:
			n, alphabet = 200+rng.Intn(400), 3+rng.Intn(40)
		case 1:
			n, alphabet = 200+rng.Intn(400), 100+rng.Intn(2000)
		}
		a := gen(n, alphabet)
		var b []string
		if rng.Intn(8) == 0 {
			b = gen(rng.Intn(max(1, 2*n)), alphabet) // unrelated
		} else {
			b = mutate(a, rng.Intn(12), alphabet)
		}
		check(a, b, []int{3, 3, 3, 0, 1, 5}[rng.Intn(6)])
	}
}

func BenchmarkUnifiedDiffLargeFile(b *testing.B) {
	rng := rand.New(rand.NewSource(2))
	old := make([]string, 20000)
	for i := range old {
		old[i] = fmt.Sprintf("\tstatement %d of %d\n", i, rng.Intn(5000))
	}
	new := append([]string(nil), old...)
	for e := 0; e < 40; e++ {
		new[rng.Intn(len(new))] = fmt.Sprintf("\tedited %d\n", e)
	}
	b.Run("cairn", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = unifiedDiff(old, new, "a/f", "b/f", 3)
		}
	})
	b.Run("go-difflib", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = difflib.GetUnifiedDiffString(difflib.UnifiedDiff{A: old, B: new, FromFile: "a/f", ToFile: "b/f", Context: 3})
		}
	})
}
