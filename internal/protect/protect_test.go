package protect

import "testing"

func TestDefaultBranchForcePushRejected(t *testing.T) {
	const zero = "0000000000000000000000000000000000000000"
	cases := []struct {
		name          string
		defaultBranch string
		ref           string
		old, new      string
		isAncestor    bool // whether old is an ancestor of new (fast-forward)
		wantAllow     bool
	}{
		{"ff to default ok", "main", "refs/heads/main", "aaa", "bbb", true, true},
		{"create default ok", "main", "refs/heads/main", zero, "bbb", false, true},
		{"force-push default rejected", "main", "refs/heads/main", "aaa", "bbb", false, false},
		{"force-push non-default ok", "main", "refs/heads/feature", "aaa", "bbb", false, true},
		{"delete default rejected", "main", "refs/heads/main", "aaa", zero, false, false},
	}
	for _, c := range cases {
		got := Allow(Rule{DefaultBranch: c.defaultBranch}, Update{
			Ref: c.ref, Old: c.old, New: c.new, OldIsAncestorOfNew: c.isAncestor,
		})
		if (got == nil) != c.wantAllow {
			t.Errorf("%s: Allow err=%v, wantAllow=%v", c.name, got, c.wantAllow)
		}
	}
}

func TestHookScriptReferencesBinary(t *testing.T) {
	got := HookScript("/usr/local/bin/cairn-server", "repo-123")
	if !contains(got, "/usr/local/bin/cairn-server pre-receive repo-123") {
		t.Fatalf("hook does not invoke the binary correctly:\n%s", got)
	}
	if !contains(got, "#!/bin/sh") {
		t.Fatalf("hook missing shebang:\n%s", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
