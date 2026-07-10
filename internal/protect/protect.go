// Package protect is cairn's MINIMAL branch protection: the single
// default-branch safety rule (no force-push, no delete on the default branch).
// The richer org-tree-axis rule engine (NEX-391 full) is deferred; this is the
// cheap floor so the core isn't a free-for-all.
package protect

import (
	"fmt"
	"strings"
)

const zeroSHA = "0000000000000000000000000000000000000000"

// Rule is the minimal per-repo protection: which branch is default, plus an
// opt-in list of required pull checks. RequiredChecks is additive and
// nil/empty by default, so existing "{}" / DefaultBranch-only rules parse
// unchanged and merges behave exactly as before this field existed.
type Rule struct {
	DefaultBranch  string   `json:"default_branch"`
	RequiredChecks []string `json:"required_checks,omitempty"`
}

// Update is one proposed ref change from a push.
type Update struct {
	Ref string // full ref, e.g. refs/heads/main
	Old string // old sha (zeroSHA on create)
	New string // new sha (zeroSHA on delete)
	// OldIsAncestorOfNew is true when Old is an ancestor of New, i.e. the update
	// is a fast-forward. Computed by the caller via `git merge-base --is-ancestor`.
	OldIsAncestorOfNew bool
}

// Allow returns nil if the update is permitted, else an error describing the
// rejection. The only rule: the default branch may not be force-pushed
// (non-fast-forward) or deleted. Non-default branches are unrestricted.
func Allow(rule Rule, u Update) error {
	defRef := "refs/heads/" + rule.DefaultBranch
	if u.Ref != defRef {
		return nil // only the default branch is protected
	}
	if u.New == zeroSHA {
		return fmt.Errorf("protect: refusing to delete the default branch %q", rule.DefaultBranch)
	}
	if u.Old == zeroSHA {
		return nil // creating the default branch is fine
	}
	if !u.OldIsAncestorOfNew {
		return fmt.Errorf("protect: non-fast-forward (force) push to the default branch %q is not allowed", rule.DefaultBranch)
	}
	return nil
}

// ParseUpdateLine parses a pre-receive stdin line "<old> <new> <ref>".
func ParseUpdateLine(line string) (Update, error) {
	f := strings.Fields(strings.TrimSpace(line))
	if len(f) != 3 {
		return Update{}, fmt.Errorf("protect: malformed update line %q", line)
	}
	return Update{Old: f[0], New: f[1], Ref: f[2]}, nil
}

// HookScript returns the pre-receive hook body for a repo. It pipes stdin
// (the ref updates) into the cairn-server pre-receive subcommand, which holds
// the rule logic. Installed at repo creation into <bare>/hooks/pre-receive.
func HookScript(binaryPath, repoID string) string {
	return "#!/bin/sh\nexec " + binaryPath + " pre-receive " + repoID + "\n"
}
