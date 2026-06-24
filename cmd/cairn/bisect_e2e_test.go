package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runOutErr runs run(args) capturing BOTH stdout and stderr, returning them and
// the run error (the caller decides whether the error is acceptable).
func runOutErr(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, perr := os.Pipe()
	if perr != nil {
		t.Fatalf("os.Pipe: %v", perr)
	}
	rErr, wErr, perr := os.Pipe()
	if perr != nil {
		t.Fatalf("os.Pipe: %v", perr)
	}
	os.Stdout, os.Stderr = wOut, wErr
	runErr := run(args)
	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	var so, se strings.Builder
	if _, e := io.Copy(&so, rOut); e != nil {
		t.Fatalf("read stdout: %v", e)
	}
	if _, e := io.Copy(&se, rErr); e != nil {
		t.Fatalf("read stderr: %v", e)
	}
	return so.String(), se.String(), runErr
}

// commitShaCLI seals branch and returns the printed head sha (stdout of commit).
func commitShaCLI(t *testing.T, root, branch string) string {
	t.Helper()
	out, _, err := runOutErr(t, "commit", "--repo", root, branch)
	if err != nil {
		t.Fatalf("commit %s: %v", branch, err)
	}
	return strings.TrimSpace(out)
}

// TestBisectManualE2E builds 6 commits on feat where flag.txt flips ok->bad at
// commit 4, then drives a manual bisect: read flag.txt off disk to decide
// good/bad, mark, and confirm convergence on s4. Reset restores the working tip.
func TestBisectManualE2E(t *testing.T) {
	skipOnWindows(t)
	root := filepath.Join(t.TempDir(), "repo")
	mustRun(t, "init", root)
	mustRun(t, "express", "--repo", root, "feat")

	flagPath := filepath.Join(root, "feat", "flag.txt")
	distinctPath := filepath.Join(root, "feat", "n.txt")

	shas := make([]string, 0, 6)
	for i := 1; i <= 6; i++ {
		val := "ok\n"
		if i >= 4 { // regression introduced at commit 4
			val = "bad\n"
		}
		if err := os.WriteFile(flagPath, []byte(val), 0o644); err != nil {
			t.Fatal(err)
		}
		// distinct per-commit file so every tree differs (and every commit is sealed)
		if err := os.WriteFile(distinctPath, []byte(strings.Repeat("x", i)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		shas = append(shas, commitShaCLI(t, root, "feat"))
	}
	s1, s4, s6 := shas[0], shas[3], shas[5]

	// Start the bisect: s1 known-good, s6 known-bad.
	_, startErr, err := runOutErr(t, "bisect", "start", "--repo", root, "--good", s1, "--bad", s6, "feat")
	if err != nil {
		t.Fatalf("bisect start: %v (stderr=%q)", err, startErr)
	}

	// Drive the loop: read flag.txt off disk (it reflects the materialized
	// midpoint), mark good/bad, until convergence (status goes inactive).
	var firstBad string
	for i := 0; i < 10; i++ {
		// Inactive? then the previous mark converged.
		statusOut, _, serr := runOutErr(t, "bisect", "status", "--repo", root)
		if serr != nil {
			t.Fatalf("bisect status: %v", serr)
		}
		_ = statusOut
		active, aerr := bisectActiveCLI(t, root)
		if aerr != nil {
			t.Fatal(aerr)
		}
		if !active {
			break
		}
		content, rerr := os.ReadFile(flagPath)
		if rerr != nil {
			t.Fatalf("read flag.txt: %v", rerr)
		}
		verdict := "bad"
		if strings.TrimSpace(string(content)) == "ok" {
			verdict = "good"
		}
		_, markErr, merr := runOutErr(t, "bisect", verdict, "--repo", root)
		if merr != nil {
			t.Fatalf("bisect %s: %v (stderr=%q)", verdict, merr, markErr)
		}
		if idx := strings.Index(markErr, "first bad commit:"); idx >= 0 {
			rest := strings.TrimSpace(markErr[idx+len("first bad commit:"):])
			firstBad = strings.Fields(rest)[0]
			break
		}
	}
	if firstBad == "" {
		t.Fatal("bisect never reported a first bad commit")
	}
	if firstBad != s4 {
		t.Fatalf("first bad = %s, want s4 = %s", firstBad, s4)
	}

	// Reset restores the working tip (s6's "bad\n") to disk.
	_, resetErr, err := runOutErr(t, "bisect", "reset", "--repo", root)
	if err != nil {
		t.Fatalf("bisect reset: %v (stderr=%q)", err, resetErr)
	}
	got, err := os.ReadFile(flagPath)
	if err != nil {
		t.Fatalf("read flag.txt after reset: %v", err)
	}
	if strings.TrimSpace(string(got)) != "bad" {
		t.Fatalf("flag.txt after reset = %q, want tip content bad", got)
	}
}

// bisectActiveCLI reports whether a bisect is active by parsing `bisect status`
// stderr ("no bisect in progress" => inactive).
func bisectActiveCLI(t *testing.T, root string) (bool, error) {
	t.Helper()
	stdout, stderr, err := runOutErr(t, "bisect", "status", "--repo", root)
	if err != nil {
		return false, err
	}
	if strings.Contains(stderr, "no bisect in progress") {
		return false, nil
	}
	return strings.Contains(stdout, "testing"), nil
}

// TestBisectStartDirtyRefused asserts a dirty (un-sealed) branch refuses to start.
func TestBisectStartDirtyRefused(t *testing.T) {
	skipOnWindows(t)
	root := filepath.Join(t.TempDir(), "repo")
	mustRun(t, "init", root)
	mustRun(t, "express", "--repo", root, "feat")

	if err := os.WriteFile(filepath.Join(root, "feat", "a.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s1 := commitShaCLI(t, root, "feat")
	if err := os.WriteFile(filepath.Join(root, "feat", "b.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := commitShaCLI(t, root, "feat")

	// Leave an un-sealed edit on disk.
	if err := os.WriteFile(filepath.Join(root, "feat", "c.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := runOutErr(t, "bisect", "start", "--repo", root, "--good", s1, "--bad", s2, "feat")
	if err == nil {
		t.Fatal("bisect start on dirty branch: want error, got nil")
	}
	if !strings.Contains(err.Error()+stderr, "stash or commit") {
		t.Fatalf("dirty refusal message = %v / %q, want 'stash or commit'", err, stderr)
	}
}
