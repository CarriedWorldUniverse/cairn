//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// plantRepo builds a throwaway cairn repo and returns its .cairn directory.
func plantRepo(t *testing.T, aliasCmd string) string {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "cfg")
	mustRun(t, "init", cfg)
	if aliasCmd != "" {
		mustRun(t, "config", "--repo", cfg, "alias.zzz", "!"+aliasCmd)
	}
	return filepath.Join(cfg, ".cairn")
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	cmd := exec.Command("cp", "-a", src, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cp: %v\n%s", err, out)
	}
}

// TestSecurityCloneRefusesPlantedCairnDir: a git repo that commits
// .cairn/cairn.db must not be clonable (finding 1: planted catalogue).
func TestSecurityCloneRefusesPlantedCairnDir(t *testing.T) {
	for _, meta := range []string{".cairn", ".CAIRN", ".cairn.", "CAIRN~1", ".git"[0:0] + ".g‌it"} {
		t.Run(meta, func(t *testing.T) {
			g := t.TempDir()
			if err := os.MkdirAll(filepath.Join(g, "src"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(g, "src", "a.txt"), []byte("a"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitRun(t, g, "init", "-q", "-b", "main")
			gitRun(t, g, "config", "user.email", "a@b")
			gitRun(t, g, "config", "user.name", "a")
			// git refuses ".git"-ish names itself; build the tree with
			// hash-object/mktree so a hostile tree reaches cairn.
			mk := func(in string, args ...string) string {
				c := exec.Command("git", args...)
				c.Dir = g
				c.Stdin = strings.NewReader(in)
				c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
				out, err := c.Output()
				if err != nil {
					t.Fatalf("git %v: %v", args, err)
				}
				return strings.TrimSpace(string(out))
			}
			b := mk("payload", "hash-object", "-w", "--stdin")
			inner := mk("100644 blob "+b+"\tcairn.db\n", "mktree")
			root := mk("040000 tree "+inner+"\t"+meta+"\n100644 blob "+b+"\tok.txt\n", "mktree", "--missing")
			c := mk("x", "commit-tree", root, "-m", "x")
			gitRun(t, g, "update-ref", "refs/heads/main", c)

			out := filepath.Join(t.TempDir(), "out")
			err := run([]string{"clone", g, out})
			if err == nil {
				t.Fatal("clone of a tree with a reserved metadata path succeeded")
			}
			if !strings.Contains(err.Error(), "invalid path") {
				t.Errorf("error should name the invalid path, got: %v", err)
			}
		})
	}
}

// TestSecurityApplyRefusesCairnPath: a patch carrying .cairn/wc.json is refused
// and nothing is written.
func TestSecurityApplyRefusesCairnPath(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "a\n")
	w.ok("main", "commit", "-m", "base")
	patch := filepath.Join(t.TempDir(), "evil.patch")
	body := "diff --git a/.cairn/wc.json b/.cairn/wc.json\nnew file mode 100644\n--- /dev/null\n+++ b/.cairn/wc.json\n@@ -0,0 +1 @@\n+{}\n"
	if err := os.WriteFile(patch, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if msg := w.fails("main", "apply", patch); !strings.Contains(msg, ".cairn") {
		t.Errorf("apply error = %q, want it to name .cairn", msg)
	}
	if w.exists("main", ".cairn/wc.json") {
		t.Fatal("apply wrote .cairn/wc.json into the line folder")
	}
}

// TestSecurityDiscoveryIgnoresNestedCairn: an inner .cairn inside an expressed
// line folder is content; discovery must return the OUTER repo, and a planted
// '!' alias must not run.
func TestSecurityDiscoveryIgnoresNestedCairn(t *testing.T) {
	w := newWC(t)
	w.write("main", "a.txt", "a\n")
	w.ok("main", "commit", "-m", "base")
	pwned := filepath.Join(t.TempDir(), "PWNED")
	copyDir(t, plantRepo(t, "touch "+pwned), filepath.Join(w.dir("main"), ".cairn"))
	sub := filepath.Join(w.dir("main"), "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, start := range []string{w.dir("main"), sub} {
		got, err := discoverRepoRoot(start)
		if err != nil || got != w.root {
			t.Fatalf("discoverRepoRoot(%s) = %q, %v; want outer %q", start, got, err, w.root)
		}
	}
	old, _ := os.Getwd()
	if err := os.Chdir(w.dir("main")); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	_ = run([]string{"zzz"})
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("planted alias executed")
	}
}

// TestSecurityDiscoverySymlinkedCairn: a .cairn symlink is not a repo.
func TestSecurityDiscoverySymlinkedCairn(t *testing.T) {
	real := plantRepo(t, "")
	d := t.TempDir()
	if err := os.Symlink(real, filepath.Join(d, ".cairn")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if got, err := discoverRepoRoot(d); err == nil && got == d {
		t.Fatalf("discoverRepoRoot accepted a symlinked .cairn at %s", d)
	}
	// a .cairn dir with no catalogue is not a repo either
	e := t.TempDir()
	if err := os.Mkdir(filepath.Join(e, ".cairn"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := discoverRepoRoot(e); err == nil && got == e {
		t.Fatal("discoverRepoRoot accepted a .cairn with no catalogue")
	}
}
