package gitpatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Ann Author", "GIT_AUTHOR_EMAIL=ann@example.org",
		"GIT_COMMITTER_NAME=C", "GIT_COMMITTER_EMAIL=c@example.org",
		"GIT_AUTHOR_DATE=2021-03-04T05:06:07+02:00", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestApplyMatchesGit diff-tests the parser+applier against real git: for a
// spread of edits, the patch `git diff` writes, applied by Apply to the old
// content, must reproduce exactly the new content git has — including files
// with no trailing newline on either side, and hunks at both ends of a file.
func TestApplyMatchesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	var long strings.Builder
	for i := 1; i <= 60; i++ {
		long.WriteString("line " + strings.Repeat("x", i%7) + "\n")
	}
	cases := []struct{ name, before, after string }{
		{"middle", "a\nb\nc\nd\ne\n", "a\nb\nC\nd\ne\n"},
		{"append", "a\nb\n", "a\nb\nc\n"},
		{"prepend", "a\nb\n", "z\na\nb\n"},
		{"lose-eol", "a\nb\n", "a\nb"},
		{"gain-eol", "a\nb", "a\nb\n"},
		{"no-eol-both", "a\nb", "a\nB"},
		{"two-hunks", long.String(), strings.Replace(strings.Replace(long.String(), "line x\n", "LINE one\n", 1), "line xxxxxx\n", "", 3)},
		{"empty-to-content", "", "hello\n"},
		{"content-to-empty", "hello\nworld\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			git(t, dir, "init", "-q")
			write(t, dir, "f.txt", tc.before)
			git(t, dir, "add", "-A")
			git(t, dir, "commit", "-qm", "before")
			write(t, dir, "f.txt", tc.after)
			patch := git(t, dir, "diff")
			files, err := Parse([]byte(patch), 1)
			if err != nil {
				t.Fatalf("Parse: %v\n%s", err, patch)
			}
			if len(files) != 1 || files[0].Path() != "f.txt" {
				t.Fatalf("Parse = %+v, want one patch for f.txt\n%s", files, patch)
			}
			got, err := Apply([]byte(tc.before), files[0])
			if err != nil {
				t.Fatalf("Apply: %v\n%s", err, patch)
			}
			if string(got) != tc.after {
				t.Errorf("Apply = %q, want %q\n%s", got, tc.after, patch)
			}
			// And the reverse patch takes it back (git apply -R).
			back, err := Apply([]byte(tc.after), files[0].Reverse())
			if err != nil {
				t.Fatalf("Apply reverse: %v", err)
			}
			if string(back) != tc.before {
				t.Errorf("reverse Apply = %q, want %q", back, tc.before)
			}
		})
	}
}

// TestParseFileKinds: new, deleted, renamed and mode-changed files, as git
// writes them.
func TestParseFileKinds(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "gone.txt", "bye\n")
	write(t, dir, "old name.txt", "same\ncontent\nhere\n")
	write(t, dir, "tool.sh", "#!/bin/sh\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "before")
	git(t, dir, "rm", "-q", "gone.txt")
	git(t, dir, "mv", "old name.txt", "new name.txt")
	write(t, dir, "sub/added.txt", "hi\n")
	if err := os.Chmod(filepath.Join(dir, "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	files, err := Parse([]byte(git(t, dir, "diff", "--cached", "-M")), 1)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]File{}
	for _, f := range files {
		byPath[f.Path()] = f
	}
	if f, ok := byPath["gone.txt"]; !ok || !f.IsDelete || f.NewPath != "" {
		t.Errorf("gone.txt: %+v, want a deletion", f)
	}
	if f, ok := byPath["sub/added.txt"]; !ok || !f.IsNew || f.OldPath != "" || f.NewMode != "100644" {
		t.Errorf("sub/added.txt: %+v, want a new 100644 file", f)
	}
	if f, ok := byPath["new name.txt"]; !ok || !f.IsRename || f.OldPath != "old name.txt" {
		t.Errorf("rename: %+v, want old name.txt -> new name.txt", f)
	}
	if f, ok := byPath["tool.sh"]; !ok || f.OldMode != "100644" || f.NewMode != "100755" {
		t.Errorf("tool.sh: %+v, want a 100644 -> 100755 mode change", f)
	}
}

// TestApplyRejectsMismatch: context that does not match is an error, never a
// guess.
func TestApplyRejectsMismatch(t *testing.T) {
	patch := "--- a/f\n+++ b/f\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n"
	files, err := Parse([]byte(patch), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply([]byte("a\nX\nc\n"), files[0]); err == nil {
		t.Fatal("Apply over mismatching content succeeded")
	}
	// The same hunk still applies when the file merely shifted under it.
	got, err := Apply([]byte("new first line\na\nb\nc\n"), files[0])
	if err != nil || string(got) != "new first line\na\nB\nc\n" {
		t.Fatalf("offset Apply = %q, %v", got, err)
	}
}

// TestParseMboxMatchesGit: format-patch output round-trips author, date,
// subject, body and the diff — and a "-- " line inside a hunk (a removed line
// whose text is "- ") is hunk content, not the mail signature.
func TestParseMboxMatchesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "f.txt", "keep\n- \nend\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	write(t, dir, "f.txt", "keep\nend\n")
	git(t, dir, "commit", "-qam", "drop the dash line: a subject long enough that the mail header has to be folded onto a second line\n\nBody paragraph one.\n\nBody paragraph two.")
	write(t, dir, "g.txt", "g\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "add g")
	mbox := git(t, dir, "format-patch", "--stdout", "-2")
	mails, err := ParseMbox([]byte(mbox))
	if err != nil {
		t.Fatalf("ParseMbox: %v", err)
	}
	if len(mails) != 2 {
		t.Fatalf("got %d mails, want 2", len(mails))
	}
	m := mails[0]
	if m.AuthorName != "Ann Author" || m.AuthorEmail != "ann@example.org" {
		t.Errorf("author = %q <%q>", m.AuthorName, m.AuthorEmail)
	}
	if got := m.Date.Format("2006-01-02T15:04:05-07:00"); got != "2021-03-04T05:06:07+02:00" {
		t.Errorf("date = %s", got)
	}
	wantMsg := strings.TrimSpace(git(t, dir, "log", "-1", "--format=%B", "HEAD~1"))
	if m.Message() != wantMsg {
		t.Errorf("message = %q\nwant      %q", m.Message(), wantMsg)
	}
	files, err := Parse(m.Patch, 1)
	if err != nil || len(files) != 1 {
		t.Fatalf("Parse(mail patch) = %v, %v", files, err)
	}
	got, err := Apply([]byte("keep\n- \nend\n"), files[0])
	if err != nil || string(got) != "keep\nend\n" {
		t.Fatalf("Apply(mail patch) = %q, %v", got, err)
	}
	if mails[1].Subject != "add g" || mails[1].Body != "" {
		t.Errorf("second mail = %q / %q", mails[1].Subject, mails[1].Body)
	}
}
