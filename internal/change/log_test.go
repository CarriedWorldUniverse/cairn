package change

import (
	"strings"
	"testing"
)

// buildThreeCommits creates a root line "main" with three commits (messages
// "first", "second", "third") and returns their SHAs in order (oldest first).
func buildThreeCommits(t *testing.T, e *Engine) (sha1, sha2, sha3 string) {
	t.Helper()
	root, err := e.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName(main): %v", err)
	}
	ch, err := e.CreateChange(root.ID, "tester")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	r1, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("v1\n")}, "first")
	if err != nil {
		t.Fatalf("Commit 1: %v", err)
	}
	r2, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("v2\n")}, "second")
	if err != nil {
		t.Fatalf("Commit 2: %v", err)
	}
	r3, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte("v3\n"), "b.txt": []byte("new\n")}, "third")
	if err != nil {
		t.Fatalf("Commit 3: %v", err)
	}
	return r1.HeadCommit, r2.HeadCommit, r3.HeadCommit
}

func TestLog_ThreeCommitsNewestFirst(t *testing.T) {
	e := newTestEngine(t)
	_, _, sha3 := buildThreeCommits(t, e)

	entries, err := e.Log(sha3, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("Log returned %d entries, want 3", len(entries))
	}
	wantSubjects := []string{"third", "second", "first"}
	for i, want := range wantSubjects {
		got := entries[i].Subject
		if got != want {
			t.Errorf("entries[%d].Subject = %q, want %q", i, got, want)
		}
	}
}

func TestLog_NoChangeIDInSubjectOrMessage(t *testing.T) {
	e := newTestEngine(t)
	_, _, sha3 := buildThreeCommits(t, e)

	entries, err := e.Log(sha3, 0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	for _, ci := range entries {
		if strings.Contains(ci.Subject, "Change-Id") {
			t.Errorf("Subject %q contains Change-Id trailer", ci.Subject)
		}
		if strings.Contains(ci.Message, "Change-Id") {
			t.Errorf("Message %q contains Change-Id trailer", ci.Message)
		}
	}
}

func TestLog_LimitRespected(t *testing.T) {
	e := newTestEngine(t)
	_, _, sha3 := buildThreeCommits(t, e)

	entries, err := e.Log(sha3, 2)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Log with limit=2 returned %d entries, want 2", len(entries))
	}
	if entries[0].Subject != "third" {
		t.Errorf("entries[0].Subject = %q, want third", entries[0].Subject)
	}
	if entries[1].Subject != "second" {
		t.Errorf("entries[1].Subject = %q, want second", entries[1].Subject)
	}
}

func TestShow_ReturnsMetadataAndFileDiff(t *testing.T) {
	e := newTestEngine(t)
	_, _, sha3 := buildThreeCommits(t, e)

	ci, diffs, err := e.Show(sha3)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if ci.Subject != "third" {
		t.Errorf("Subject = %q, want third", ci.Subject)
	}
	if ci.SHA != sha3 {
		t.Errorf("SHA = %q, want %q", ci.SHA, sha3)
	}
	if len(diffs) == 0 {
		t.Fatal("Show returned no diffs for third commit (expected at least b.txt added)")
	}
	foundB := false
	for _, d := range diffs {
		if d.Path == "b.txt" {
			foundB = true
			if d.Status != Added {
				t.Errorf("b.txt status = %v, want Added", d.Status)
			}
		}
	}
	if !foundB {
		t.Error("b.txt not found in diffs from Show")
	}
}

func TestShow_FirstCommitDiffVsEmpty(t *testing.T) {
	e := newTestEngine(t)
	sha1, _, _ := buildThreeCommits(t, e)

	ci, diffs, err := e.Show(sha1)
	if err != nil {
		t.Fatalf("Show on first commit: %v", err)
	}
	if ci.Subject != "first" {
		t.Errorf("Subject = %q, want first", ci.Subject)
	}
	// First commit has no parent; diff vs empty tree => all files Added
	if len(diffs) == 0 {
		t.Fatal("Show on first commit returned no diffs")
	}
	for _, d := range diffs {
		if d.Status != Added {
			t.Errorf("first commit: %s status = %v, want Added", d.Path, d.Status)
		}
	}
}

// TestStripChangeIDBodyMention verifies that stripChangeID only removes the
// trailing Change-Id trailer and does NOT strip a "Change-Id:" mention that
// appears inside the message body.
func TestStripChangeIDBodyMention(t *testing.T) {
	input := "fix thing\n\nsee Change-Id: discussion\n\nChange-Id: zABC\n"
	want := "fix thing\n\nsee Change-Id: discussion"
	got := stripChangeID(input)
	if got != want {
		t.Errorf("stripChangeID(%q)\n   got  %q\n  want %q", input, got, want)
	}
}
