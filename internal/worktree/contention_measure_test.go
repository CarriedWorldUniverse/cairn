package worktree

// Lock-contention measurement for the #98 per-line-lock decision (design
// comment on the issue: "build per-line locks ONLY if a post-Phase-B
// measurement shows local-phase contention that bites"). NOT part of the
// suite — gated on CAIRN_MEASURE=1; run with:
//
//	CAIRN_MEASURE=1 go test ./internal/worktree/ -run TestMeasureLockContention -v -timeout 30m
//
// It builds a clone at carried-world scale (~2,700 files / ~800MB, binary-
// dominated, 3 expressed lines) and reports how long wc.lock is held by each
// local op — i.e. how long a second agent on ANOTHER line would wait.

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
)

const (
	measureSmallFiles = 2500
	measureSmallSize  = 1 << 10 // 1 KiB
	measureBin2M      = 200
	measureBin10M     = 40
)

func writeTree(t *testing.T, dir string, rng *rand.Rand) {
	t.Helper()
	buf2m := make([]byte, 2<<20)
	buf10m := make([]byte, 10<<20)
	for i := 0; i < measureSmallFiles; i++ {
		p := filepath.Join(dir, fmt.Sprintf("src/mod%02d/pkg%02d/f%04d.gd", i%20, (i/20)%10, i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(fmt.Sprintf("# file %d\nvar x = %d\n", i, i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < measureBin2M; i++ {
		rng.Read(buf2m)
		p := filepath.Join(dir, fmt.Sprintf("assets/tex/t%03d.bin", i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, buf2m, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < measureBin10M; i++ {
		rng.Read(buf10m)
		p := filepath.Join(dir, fmt.Sprintf("assets/vox/v%03d.bin", i))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, buf10m, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func timeIt(t *testing.T, label string, f func() error) time.Duration {
	t.Helper()
	start := time.Now()
	if err := f(); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	d := time.Since(start)
	t.Logf("%-52s %10s", label, d.Round(time.Millisecond))
	return d
}

func TestMeasureLockContention(t *testing.T) {
	if os.Getenv("CAIRN_MEASURE") != "1" {
		t.Skip("measurement harness; set CAIRN_MEASURE=1 to run")
	}
	rng := rand.New(rand.NewSource(98))
	root := t.TempDir()
	r, err := Open(root, "a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	t.Log("== setup: carried-world-scale tree (2740 files, ~820MB) ==")
	writeTree(t, filepath.Join(root, "main"), rng)
	timeIt(t, "seed commit (cold scan+hash of full tree)", func() error {
		_, err := r.Commit("main", "seed")
		return err
	})
	timeIt(t, "express lineB (full materialize)", func() error { return r.Express("lineB", "main") })
	timeIt(t, "express lineC (full materialize)", func() error { return r.Express("lineC", "main") })

	t.Log("== steady-state single-agent op costs (== wc.lock hold) ==")
	timeIt(t, "Status main (warm cache)", func() error {
		_, err := r.Status("main")
		return err
	})
	// Small commit: 5 text files + 1 binary touched — the everyday op.
	for run := 1; run <= 3; run++ {
		for i := 0; i < 5; i++ {
			p := filepath.Join(root, "main", fmt.Sprintf("src/mod%02d/pkg%02d/f%04d.gd", i%20, (i/20)%10, i))
			if err := os.WriteFile(p, []byte(fmt.Sprintf("# edit r%d\nvar x = %d\n", run, i)), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		buf := make([]byte, 2<<20)
		rng.Read(buf)
		if err := os.WriteFile(filepath.Join(root, "main", "assets/tex/t000.bin"), buf, 0o644); err != nil {
			t.Fatal(err)
		}
		timeIt(t, fmt.Sprintf("Commit main, small change (run %d)", run), func() error {
			_, err := r.Commit("main", fmt.Sprintf("small %d", run))
			return err
		})
	}

	t.Log("== whole-tree Pull local phase (the design's known candidate) ==")
	bare := t.TempDir()
	if err := os.RemoveAll(bare); err != nil {
		t.Fatal(err)
	}
	mkBare(t, bare)
	if err := r.AddRemote("origin", bare, "git"); err != nil {
		t.Fatal(err)
	}
	timeIt(t, "initial Push (baseline, incl. local file remote)", func() error { return r.Push("origin", false) })

	// Second clone plays "machine A": touches ~200 small files + 10 binaries
	// and pushes; first clone then Pulls — its wc.lock phase reconciles the
	// line AND re-materializes every expressed folder.
	cloneDir := t.TempDir()
	if err := os.RemoveAll(cloneDir); err != nil {
		t.Fatal(err)
	}
	var r2 *Repo
	timeIt(t, "Clone (machine A, baseline)", func() error {
		var cerr error
		r2, cerr = Clone(bare, cloneDir, "a2", io.Discard)
		return cerr
	})
	t.Cleanup(func() { _ = r2.Close() })
	def := "main"
	for i := 0; i < 200; i++ {
		p := filepath.Join(cloneDir, def, fmt.Sprintf("src/mod%02d/pkg%02d/f%04d.gd", i%20, (i/20)%10, i))
		if err := os.WriteFile(p, []byte(fmt.Sprintf("# remote edit\nvar x = %d\n", i+7)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	buf := make([]byte, 2<<20)
	for i := 1; i <= 10; i++ {
		rng.Read(buf)
		if err := os.WriteFile(filepath.Join(cloneDir, def, fmt.Sprintf("assets/tex/t%03d.bin", i)), buf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r2.Commit(def, "remote change 200 files"); err != nil {
		t.Fatal(err)
	}
	timeIt(t, "machine A Push of 210-file change", func() error { return r2.Push("origin", false) })
	timeIt(t, "Pull of 210-file change (3 expressed folders)", func() error {
		_, err := r.Pull("origin")
		return err
	})

	t.Log("== cross-agent contention: B waits behind A's holds ==")
	// Agent A (handle r) loops small commits on main; agent B (fresh handle,
	// same clone) measures Status + Commit latency on lineB. B's excess
	// latency over its solo cost IS the per-line-lock benefit ceiling.
	rB, err := Open(root, "b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rB.Close() })
	soloStatus := timeIt(t, "B Status lineB solo (warm)", func() error {
		_, err := rB.Status("lineB")
		return err
	})
	if err := os.WriteFile(filepath.Join(root, "lineB", "bwork.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	soloCommit := timeIt(t, "B Commit lineB solo", func() error {
		_, err := rB.Commit("lineB", "b solo")
		return err
	})

	stop := make(chan struct{})
	aDone := make(chan struct{})
	go func() {
		defer close(aDone)
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			i++
			p := filepath.Join(root, "main", fmt.Sprintf("src/mod00/pkg00/f%04d.gd", i%5))
			_ = os.WriteFile(p, []byte(fmt.Sprintf("# churn %d\n", i)), 0o644)
			rng.Read(buf)
			_ = os.WriteFile(filepath.Join(root, "main", "assets/tex/t000.bin"), buf, 0o644)
			if _, err := r.Commit("main", fmt.Sprintf("churn %d", i)); err != nil {
				t.Logf("A churn commit error (non-fatal): %v", err)
				return
			}
		}
	}()
	time.Sleep(200 * time.Millisecond) // let A get going
	var statusWaits, commitWaits []time.Duration
	for i := 0; i < 8; i++ {
		start := time.Now()
		if _, err := rB.Status("lineB"); err != nil {
			t.Fatalf("B status under contention: %v", err)
		}
		statusWaits = append(statusWaits, time.Since(start))
		if err := os.WriteFile(filepath.Join(root, "lineB", "bwork.txt"), []byte(fmt.Sprintf("b%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		if _, err := rB.Commit("lineB", fmt.Sprintf("b under contention %d", i)); err != nil {
			t.Fatalf("B commit under contention: %v", err)
		}
		commitWaits = append(commitWaits, time.Since(start))
	}
	close(stop)
	<-aDone

	report := func(label string, solo time.Duration, xs []time.Duration) {
		var max, sum time.Duration
		for _, d := range xs {
			sum += d
			if d > max {
				max = d
			}
		}
		mean := sum / time.Duration(len(xs))
		t.Logf("%-38s solo=%-9s mean=%-9s max=%-9s excess(max-solo)=%s",
			label, solo.Round(time.Millisecond), mean.Round(time.Millisecond),
			max.Round(time.Millisecond), (max - solo).Round(time.Millisecond))
	}
	t.Log("== contention results (B on lineB while A churn-commits main) ==")
	report("B Status lineB under contention", soloStatus, statusWaits)
	report("B Commit lineB under contention", soloCommit, commitWaits)
}

// mkBare creates a bare git repo the way the existing tests do.
func mkBare(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatal(err)
	}
}
