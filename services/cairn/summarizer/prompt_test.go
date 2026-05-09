//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"strings"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

func TestSelectFields_FullIncludesDiff(t *testing.T) {
	full := PRContext{Title: "T", Body: "B", Diff: "DIFF", FilePaths: []string{"a.go"}, CommitMessages: []string{"c1"}}
	got := SelectFields(cairnmodels.DataScopeFull, full)
	if got.Diff == "" {
		t.Error("full scope should include diff")
	}
	if len(got.CommitMessages) != 1 || len(got.FilePaths) != 1 {
		t.Error("full scope should include commits and files")
	}
}

func TestSelectFields_CommitMessagesExcludesDiff(t *testing.T) {
	full := PRContext{Title: "T", Diff: "DIFF", CommitMessages: []string{"c1"}, FilePaths: []string{"a.go"}}
	got := SelectFields(cairnmodels.DataScopeCommitMessages, full)
	if got.Diff != "" {
		t.Error("commit-messages scope must not include diff")
	}
	if len(got.CommitMessages) != 1 {
		t.Error("commit-messages scope must include commit messages")
	}
	if len(got.FilePaths) != 0 {
		t.Error("commit-messages scope must not include file paths")
	}
}

func TestSelectFields_MetadataExcludesContent(t *testing.T) {
	full := PRContext{Title: "T", Diff: "DIFF", CommitMessages: []string{"c1"}, FilePaths: []string{"a.go"}}
	got := SelectFields(cairnmodels.DataScopeMetadata, full)
	if got.Diff != "" || len(got.CommitMessages) != 0 {
		t.Error("metadata scope must not include diff or commit messages")
	}
	if len(got.FilePaths) != 1 {
		t.Error("metadata scope must include file paths")
	}
}

func TestSelectFields_CommitMessagesExcludesBranchNames(t *testing.T) {
	full := PRContext{Title: "T", BaseBranch: "main", HeadBranch: "feat", CommitMessages: []string{"c"}}
	got := SelectFields(cairnmodels.DataScopeCommitMessages, full)
	if got.BaseBranch != "" || got.HeadBranch != "" {
		t.Errorf("commit-messages must not leak branch names: got base=%q head=%q", got.BaseBranch, got.HeadBranch)
	}
}

func TestSelectFields_MetadataExcludesBranchNames(t *testing.T) {
	full := PRContext{Title: "T", BaseBranch: "main", HeadBranch: "feat", FilePaths: []string{"a.go"}}
	got := SelectFields(cairnmodels.DataScopeMetadata, full)
	if got.BaseBranch != "" || got.HeadBranch != "" {
		t.Errorf("metadata must not leak branch names: got base=%q head=%q", got.BaseBranch, got.HeadBranch)
	}
}

func TestSelectFields_UnknownScopeDegradesToMetadata(t *testing.T) {
	full := PRContext{Title: "T", Diff: "DIFF", CommitMessages: []string{"c1"}, FilePaths: []string{"a.go"}}
	got := SelectFields(cairnmodels.DataScope("bogus"), full)
	if got.Diff != "" || len(got.CommitMessages) != 0 {
		t.Error("unknown scope must not include diff or commits")
	}
	if len(got.FilePaths) != 1 {
		t.Error("unknown scope should degrade to metadata (paths only)")
	}
}

func TestBuildUserPrompt_OmitsEmptySections(t *testing.T) {
	out := BuildUserPrompt(PRContext{Title: "T"})
	if strings.Contains(out, "Diff:") || strings.Contains(out, "Commits:") || strings.Contains(out, "Files changed:") || strings.Contains(out, "Description:") || strings.Contains(out, "Branch:") {
		t.Errorf("empty sections should be omitted: %s", out)
	}
	if !strings.Contains(out, "Pull request: T") {
		t.Errorf("title section missing: %s", out)
	}
}

func TestBuildUserPrompt_IncludesAllPopulatedSections(t *testing.T) {
	c := PRContext{
		Title:          "T",
		Body:           "body text",
		BaseBranch:     "main",
		HeadBranch:     "feat",
		CommitMessages: []string{"c1", "c2"},
		FilePaths:      []string{"a.go", "b.go"},
		Diff:           "@@ diff",
	}
	out := BuildUserPrompt(c)
	for _, want := range []string{"Pull request: T", "Description:", "body text", "Branch: feat -> main", "Commits:", "- c1", "- c2", "Files changed:", "- a.go", "- b.go", "Diff:", "@@ diff"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}
