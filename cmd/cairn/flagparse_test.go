package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseArgsInterspersed locks the core behavior: flags are honored whether
// they appear before or after positional arguments, bool flags consume no value,
// "-flag value" and "-flag=value" both work, and "--" ends flag scanning.
func TestParseArgsInterspersed(t *testing.T) {
	newFS := func() (*flag.FlagSet, *string, *bool, *int) {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		m := fs.String("m", "", "")
		force := fs.Bool("force", false, "")
		n := fs.Int("n", 0, "")
		return fs, m, force, n
	}

	t.Run("flags after positional", func(t *testing.T) {
		fs, m, force, n := newFS()
		if err := parseArgs(fs, []string{"branch", "-m", "hi there", "--force", "-n", "5"}); err != nil {
			t.Fatal(err)
		}
		if fs.NArg() != 1 || fs.Arg(0) != "branch" {
			t.Fatalf("positionals = %v, want [branch]", fs.Args())
		}
		if *m != "hi there" || !*force || *n != 5 {
			t.Fatalf("m=%q force=%v n=%d, want 'hi there'/true/5", *m, *force, *n)
		}
	})

	t.Run("flags before positional", func(t *testing.T) {
		fs, m, _, _ := newFS()
		if err := parseArgs(fs, []string{"-m", "hi", "branch"}); err != nil {
			t.Fatal(err)
		}
		if fs.Arg(0) != "branch" || *m != "hi" {
			t.Fatalf("arg0=%q m=%q, want branch/hi", fs.Arg(0), *m)
		}
	})

	t.Run("equals form", func(t *testing.T) {
		fs, m, _, _ := newFS()
		if err := parseArgs(fs, []string{"branch", "-m=hi"}); err != nil {
			t.Fatal(err)
		}
		if *m != "hi" {
			t.Fatalf("m=%q, want hi", *m)
		}
	})

	t.Run("double dash ends flag scan", func(t *testing.T) {
		fs, m, _, _ := newFS()
		if err := parseArgs(fs, []string{"branch", "--", "-m", "x"}); err != nil {
			t.Fatal(err)
		}
		if *m != "" {
			t.Fatalf("m=%q, want empty (after --)", *m)
		}
		if got := fs.Args(); len(got) != 3 || got[0] != "branch" || got[1] != "-m" || got[2] != "x" {
			t.Fatalf("positionals = %v, want [branch -m x]", got)
		}
	})

	t.Run("multiple positionals with flag between", func(t *testing.T) {
		fs, m, _, _ := newFS()
		if err := parseArgs(fs, []string{"a", "-m", "msg", "b"}); err != nil {
			t.Fatal(err)
		}
		if fs.NArg() != 2 || fs.Arg(0) != "a" || fs.Arg(1) != "b" || *m != "msg" {
			t.Fatalf("args=%v m=%q, want [a b]/msg", fs.Args(), *m)
		}
	})
}

// TestCommitMessageStampedAfterBranch is the regression test for the reported
// bug: `cairn commit <branch> -m <msg>` (flag after the positional) must stamp
// the message onto the sealed commit, not silently default to "snapshot".
func TestCommitMessageStampedAfterBranch(t *testing.T) {
	root := t.TempDir()
	if err := run([]string{"init", root}); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureRun(t, "commit", "--repo", root, "main", "-m", "my real message")
	sha := strings.TrimSpace(out)
	if sha == "" {
		t.Fatal("commit printed no sha")
	}
	show := captureRun(t, "show", "--repo", root, sha)
	if !strings.Contains(show, "my real message") {
		t.Fatalf("sealed commit missing the -m message; show output:\n%s", show)
	}
	if strings.Contains(show, "\n    snapshot\n") {
		t.Fatalf("sealed commit fell back to 'snapshot' — message was dropped:\n%s", show)
	}
}
