package worktree

import (
	"path/filepath"
	"sync"
	"testing"
)

// TestConcurrentExpressDoesNotClobberState is the regression test for issue
// #81: two cairn processes sharing one working copy each expressed a branch,
// but the unlocked reload-modify-save of wc.json let one clobber the other's
// entry (a later commit then failed "branch not expressed" and pushed an empty
// branch). Two Repo handles on one root model the two processes — each has its
// own in-memory state and its own OS lock fd, so the cross-process wc.lock
// serializes them. Both lines must survive.
func TestConcurrentExpressDoesNotClobberState(t *testing.T) {
	root := t.TempDir()
	r1, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open r1: %v", err)
	}
	defer r1.Close()
	r2, err := Open(root, "t")
	if err != nil {
		t.Fatalf("open r2: %v", err)
	}
	defer r2.Close()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = r1.Express("alpha", "") }()
	go func() { defer wg.Done(); errs[1] = r2.Express("beta", "") }()
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent express %d errored: %v", i, e)
		}
	}

	// Persisted wc.json must hold BOTH lines (pre-fix, one clobbered the other).
	st, err := LoadState(filepath.Join(root, ".cairn", "wc.json"))
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	for _, b := range []string{"alpha", "beta"} {
		if _, ok := st.Expressed[b]; !ok {
			t.Fatalf("branch %q missing from wc.json after concurrent express (have %d entries) — #81 regression", b, len(st.Expressed))
		}
	}

	// A fresh Open (a third process) must also see both, and both must be
	// committable — i.e. not the "branch not expressed" failure #81 produced.
	r3, err := Open(root, "t")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer r3.Close()
	for _, b := range []string{"alpha", "beta"} {
		if _, ok := r3.st.Expressed[b]; !ok {
			t.Fatalf("reopened repo missing expressed branch %q", b)
		}
	}
}
