package worktree

// Profiling companion to TestMeasureLockContention (#124): seeds the
// carried-world-scale tree ONCE into CAIRN_PROFILE_DIR (persistent across
// runs), then exercises just warm Status + a small Commit so a -cpuprofile
// run captures the steady-state hot path without the setup noise.
//
//	CAIRN_PROFILE_DIR=/tmp/cairnprof go test ./internal/worktree/ \
//	  -run TestProfileSteadyStateOps -v -timeout 30m -cpuprofile cpu.out
import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestProfileSteadyStateOps(t *testing.T) {
	root := os.Getenv("CAIRN_PROFILE_DIR")
	if root == "" {
		t.Skip("profiling harness; set CAIRN_PROFILE_DIR to run")
	}
	rng := rand.New(rand.NewSource(124))
	seeded := true
	if _, err := os.Stat(filepath.Join(root, ".cairn")); err != nil {
		seeded = false
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Open(root, "p")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	if !seeded {
		t.Log("seeding carried-world-scale tree (one-time)")
		writeTree(t, filepath.Join(root, "main"), rng)
		if _, err := r.Commit("main", "seed"); err != nil {
			t.Fatal(err)
		}
		if err := r.Express("lineB", "main"); err != nil {
			t.Fatal(err)
		}
	}
	// Steady state: one commit to reset cache conditions to "just committed",
	// then the profiled section.
	if err := os.WriteFile(filepath.Join(root, "main", "warmup.txt"), []byte("w\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("main", "warmup"); err != nil {
		t.Fatal(err)
	}

	t.Log("profiled section: 3x warm Status + 1 small Commit")
	for i := 0; i < 3; i++ {
		timeIt(t, fmt.Sprintf("warm Status %d", i+1), func() error {
			_, err := r.Status("main")
			return err
		})
	}
	for i := 0; i < 5; i++ {
		p := filepath.Join(root, "main", fmt.Sprintf("src/mod%02d/pkg%02d/f%04d.gd", i%20, (i/20)%10, i))
		if err := os.WriteFile(p, []byte(fmt.Sprintf("# profedit %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	timeIt(t, "small Commit (5 text files)", func() error {
		_, err := r.Commit("main", "prof small")
		return err
	})
}
