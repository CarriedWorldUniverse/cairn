package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// agentPage runs `cairn help <topic>` and returns its stdout.
func agentPage(t *testing.T, topic string) string {
	t.Helper()
	return captureRun(t, "help", topic)
}

func TestHelpAgentPage(t *testing.T) {
	for _, topic := range []string{"agent", "agents", "llm"} {
		out := agentPage(t, topic)
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if !strings.Contains(lines[0], buildVersion) || strings.Contains(lines[0], "{{") {
			t.Errorf("help %s: first line %q should carry the version %q", topic, lines[0], buildVersion)
		}
		if len(lines) > 140 {
			t.Errorf("help %s: %d lines, want <= 140 (agents pay per line)", topic, len(lines))
		}
		if strings.Contains(out, "\x1b") {
			t.Errorf("help %s: page must be plain text, found an escape code", topic)
		}
	}
}

// The page is never paged, even though plain `help` is.
func TestHelpAgentNeverPaged(t *testing.T) {
	for _, topic := range []string{"agent", "agents", "llm"} {
		args := []string{"help", topic}
		if !wantsAgentHelp(args) {
			t.Fatalf("wantsAgentHelp(%v) = false", args)
		}
		// tty=true, a pager that would visibly break the output if started.
		t.Setenv("CAIRN_PAGER", "false")
		stop := startPager(args, pagerForce, true)
		if pagerActive {
			stop()
			t.Fatalf("help %s started a pager", topic)
		}
		stop()
	}
	if wantsAgentHelp([]string{"help"}) || wantsAgentHelp([]string{"help", "log"}) {
		t.Error("only help agent|agents|llm is exempt from the pager")
	}
}

func TestHelpDiscoveryLine(t *testing.T) {
	const want = "LLM/agent users: run 'cairn help agent' for a compact guide written for you."
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		out := strings.TrimRight(captureRun(t, args...), "\n")
		if !strings.HasSuffix(out, want) {
			t.Errorf("cairn %v output should end with the discovery line, ends %q", args, out[max(0, len(out)-100):])
		}
	}
	// No subcommand: usage on stdout (plus an error), same ending.
	out, err := captureRunResult(t)
	if err == nil || !strings.HasSuffix(strings.TrimRight(out, "\n"), want) {
		t.Errorf("bare cairn: want usage ending with the discovery line and an error; err=%v", err)
	}
	// The discovery line must not leak into any verb's section.
	for _, l := range verbUsage("log") {
		if strings.Contains(l, "LLM/agent") {
			t.Errorf("verb help picked up the discovery line: %q", l)
		}
	}
	// An unknown topic still errors.
	if _, err := captureRunResult(t, "help", "nonesuch"); err == nil {
		t.Error("help with an unknown topic must still error")
	}
}

// usageVerbs parses the verb names out of the `usage` subcommand table (the
// two-space-indented lines above "git spellings"; "a | b | c" lines list several).
func usageVerbs() []string {
	seen := map[string]bool{}
	var out []string
	for _, ln := range strings.Split(usage, "\n") {
		if strings.HasPrefix(ln, "git spellings") {
			break
		}
		if !strings.HasPrefix(ln, "  ") || strings.HasPrefix(ln, "   ") {
			continue
		}
		f := strings.Fields(ln)
		names := []string{f[0]}
		for i := 1; i+1 < len(f) && f[i] == "|"; i += 2 {
			names = append(names, f[i+1])
		}
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// agentHelpOmitted lists usage-table verbs the agent page deliberately does not
// mention: plumbing, one-off administration, and release/privacy machinery an
// agent does not reach for in the daily loop (`cairn help <verb>` covers them).
// Adding a verb to the usage table fails TestHelpAgentCoversVerbs until it is
// either written into help_agent.txt or listed here with a reason.
var agentHelpOmitted = map[string]string{
	"setup":         "interactive identity prompt; the page gives the config commands instead",
	"logout":        "credential housekeeping",
	"auth":          "credential housekeeping",
	"count-objects": "object-store plumbing",
	"check-ignore":  "rare ignore-rule debugging",
	"symbolic-ref":  "git plumbing, same as git",
	"hash-object":   "git plumbing, same as git",
	"name-rev":      "git plumbing, same as git",
	"reparent":      "topology repair after a git import",
	"private":       "push-privacy machinery",
	"embargo":       "push-privacy machinery",
	"disclose":      "push-privacy machinery",
	"version":       "release versioning",
	"release":       "release publishing",
	"update":        "self-update of the binary",
}

func TestHelpAgentCoversVerbs(t *testing.T) {
	page := agentPage(t, "agent")
	verbs := usageVerbs()
	if len(verbs) < 40 {
		t.Fatalf("parsed only %d verbs from usage; the parser is broken: %v", len(verbs), verbs)
	}
	for _, v := range verbs {
		re := regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(v) + `($|[^A-Za-z0-9_-])`)
		_, omitted := agentHelpOmitted[v]
		switch {
		case re.MatchString(page) && omitted:
			t.Errorf("verb %q is on the page but also in agentHelpOmitted; drop it from the allowlist", v)
		case !re.MatchString(page) && !omitted:
			t.Errorf("verb %q is in the usage table but neither on the agent page nor in agentHelpOmitted", v)
		}
	}
	for v := range agentHelpOmitted {
		found := false
		for _, u := range verbs {
			found = found || u == v
		}
		if !found {
			t.Errorf("agentHelpOmitted lists %q, which is not a usage-table verb", v)
		}
	}
}

// --- spot checks of claims the page makes -------------------------------------
// Already proven elsewhere (not repeated here):
//   - conflicts exit 2, resolve: TestE2E_ConflictResolveViaCLI (e2e_test.go), and
//     the main() mapping of errConflicts to exit 2.
//   - commit with no TTY and no -m is not blocked on an editor: stdinIsTerminal gate.

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// "commit with nothing to seal exits 1; --allow-empty seals anyway."
func TestHelpAgentClaim_EmptyCommit(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	writeFile(t, filepath.Join(root, "main", "a.txt"), "a\n")
	mustRun(t, "commit", "--repo", root, "-m", "first", "main")
	err := run([]string{"commit", "--repo", root, "-m", "again", "main"})
	if err == nil || errors.Is(err, errConflicts) {
		t.Fatalf("empty commit: want a plain error (exit 1), got %v", err)
	}
	mustRun(t, "commit", "--repo", root, "--allow-empty", "-m", "again", "main")
}

// "`cairn add` is a no-op": it succeeds and changes nothing.
func TestHelpAgentClaim_AddIsNoop(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	writeFile(t, filepath.Join(root, "main", "a.txt"), "a\n")
	before := captureRun(t, "status", "--repo", root, "--porcelain")
	mustRun(t, "add", "--repo", root, "-A")
	if after := captureRun(t, "status", "--repo", root, "--porcelain"); after != before {
		t.Fatalf("add changed status: %q -> %q", before, after)
	}
	if !strings.Contains(before, "a.txt") {
		t.Fatalf("an unstaged new file is already in the working change; porcelain = %q", before)
	}
}

// "fold/abandon/unexpress refuse un-sealed work without --force" and
// "fold of a line that is not a child of the current line is refused".
func TestHelpAgentClaim_ForceAndNonChildFold(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	writeFile(t, filepath.Join(root, "main", "a.txt"), "a\n")
	mustRun(t, "commit", "--repo", root, "-m", "base", "main")
	mustRun(t, "express", "--repo", root, "feat")
	writeFile(t, filepath.Join(root, "feat", "f.txt"), "f\n")
	for _, verb := range []string{"fold", "abandon", "unexpress"} {
		if err := run([]string{verb, "--repo", root, "feat"}); err == nil {
			t.Fatalf("%s of a line with un-sealed work succeeded without --force", verb)
		}
	}
	mustRun(t, "commit", "--repo", root, "-m", "f", "feat")
	mustRun(t, "express", "--repo", root, "other") // sibling of feat, child of main
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(filepath.Join(root, "feat")); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"merge", "other"}); err == nil || !strings.Contains(err.Error(), "not a child") {
		t.Fatalf("merge of a non-child line: want a 'not a child' refusal, got %v", err)
	}
}

// "At the repo ROOT `commit` fails 'branch required'; inside a line it acts on that line."
func TestHelpAgentClaim_BranchDefaultsToFolder(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	writeFile(t, filepath.Join(root, "main", "a.txt"), "a\n")
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"commit", "-m", "x"}); err == nil || !strings.Contains(err.Error(), "branch required") {
		t.Fatalf("commit at the repo root: want 'branch required', got %v", err)
	}
	if err := os.Chdir(filepath.Join(root, "main")); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "-m", "x")
}

// "Without -m and without a TTY the message silently becomes 'snapshot'."
func TestHelpAgentClaim_NoMessageNoTTY(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	writeFile(t, filepath.Join(root, "main", "a.txt"), "a\n")
	mustRun(t, "commit", "--repo", root, "main")
	if out := captureRun(t, "log", "--repo", root, "--format=%s", "-n1"); strings.TrimSpace(out) != "snapshot" {
		t.Fatalf("commit without -m: subject = %q, want \"snapshot\"", out)
	}
}

// "undo of an undo redoes it."
func TestHelpAgentClaim_UndoOfUndoRedoes(t *testing.T) {
	root := t.TempDir()
	mustRun(t, "init", root)
	writeFile(t, filepath.Join(root, "main", "a.txt"), "a\n")
	mustRun(t, "commit", "--repo", root, "-m", "base", "main")
	writeFile(t, filepath.Join(root, "main", "a.txt"), "edited\n")
	mustRun(t, "reset", "--repo", root, "--hard")
	has := func(want string) {
		t.Helper()
		b, _ := os.ReadFile(filepath.Join(root, "main", "a.txt"))
		if string(b) != want {
			t.Fatalf("a.txt = %q, want %q", b, want)
		}
	}
	has("a\n")
	mustRun(t, "undo", "--repo", root)
	has("edited\n")
	mustRun(t, "undo", "--repo", root)
	has("a\n")
}
