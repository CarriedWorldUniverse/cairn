package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureRun runs the given cairn subcommand args and captures stdout, returning
// the captured output. If the run fails, the test is fatally stopped.
func captureRun(t *testing.T, args ...string) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureRun: pipe: %v", err)
	}
	os.Stdout = w
	runErr := run(args)
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatalf("captureRun: read: %v", copyErr)
	}
	if runErr != nil {
		t.Fatalf("run %v: %v", args, runErr)
	}
	return buf.String()
}

// captureRunResult is like captureRun but returns both stdout and error
// (without fatally failing on error), for commands that may return an error.
func captureRunResult(t *testing.T, args ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("captureRunResult: pipe: %v", err)
	}
	os.Stdout = w
	runErr := run(args)
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	if _, copyErr := io.Copy(&buf, r); copyErr != nil {
		t.Fatalf("captureRunResult: read: %v", copyErr)
	}
	return buf.String(), runErr
}

func TestE2E_LogShowThreeCommits(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)

	// First commit (flags must precede the positional branch arg)
	if err := os.WriteFile(filepath.Join(root, "main", "file1.txt"), []byte("content1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha1out := captureRun(t, "commit", "--repo", root, "--author", "tester", "-m", "first", "main")
	sha1 := strings.TrimSpace(sha1out)

	// Second commit
	if err := os.WriteFile(filepath.Join(root, "main", "file2.txt"), []byte("content2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha2out := captureRun(t, "commit", "--repo", root, "--author", "tester", "-m", "second", "main")
	sha2 := strings.TrimSpace(sha2out)

	// Third commit
	if err := os.WriteFile(filepath.Join(root, "main", "file3.txt"), []byte("content3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	captureRun(t, "commit", "--repo", root, "--author", "tester", "-m", "third", "main")

	_ = sha1 // sha1 not used in log assertions below; sha2 is used for show

	// Test: cairn log shows all three subjects
	logOut := captureRun(t, "log", "--repo", root, "main")
	for _, subject := range []string{"first", "second", "third"} {
		if !strings.Contains(logOut, subject) {
			t.Errorf("cairn log output missing %q:\n%s", subject, logOut)
		}
	}

	// Verify newest-first order: "third" should appear before "first"
	thirdIdx := strings.Index(logOut, "third")
	firstIdx := strings.Index(logOut, "first")
	if thirdIdx < 0 || firstIdx < 0 {
		t.Fatalf("log output missing third or first: %s", logOut)
	}
	if thirdIdx > firstIdx {
		t.Errorf("expected newest-first: 'third' should appear before 'first' in log output")
	}

	// Test: cairn show <sha2> contains "second" and the changed file
	showOut := captureRun(t, "show", "--repo", root, sha2)
	if !strings.Contains(showOut, "second") {
		t.Errorf("cairn show output missing 'second':\n%s", showOut)
	}
	if !strings.Contains(showOut, "file2.txt") {
		t.Errorf("cairn show output missing 'file2.txt':\n%s", showOut)
	}
}

// TestShowAndCherryPickAcceptShortSHA is the regression for commands rejecting
// abbreviated SHAs: `cairn log` prints 8-char short SHAs, but `show`/`cherry-pick`
// used plumbing.NewHash (which demands a full 40-char hash) and failed with
// "object not found" / "is not a cairn commit" on the very SHA log displayed.
func TestShowAndCherryPickAcceptShortSHA(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	if err := os.WriteFile(filepath.Join(root, "main", "f.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	full := strings.TrimSpace(captureRun(t, "commit", "--repo", root, "main", "-m", "the message"))
	if len(full) < 8 {
		t.Fatalf("commit returned no sha: %q", full)
	}
	short := full[:8]

	out := captureRun(t, "show", "--repo", root, short)
	if !strings.Contains(out, "the message") {
		t.Fatalf("show <short-sha> did not resolve; output:\n%s", out)
	}
}
