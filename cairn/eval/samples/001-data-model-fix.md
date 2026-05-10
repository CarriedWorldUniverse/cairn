Pull request: fix(cairn): pr_summary UNIQUE must be composite (repo, pr, hash)

Description:
xorm named UNIQUE indexes require every participating column to
carry the same UNIQUE(name) tag. As written, the cairn_pr_summary
unique index applied only to content_hash, which would have
rejected legitimate cross-PR duplicates while not enforcing the
composite uniqueness the spec asks for.

Adds the explicit test that the composite UNIQUE rejects duplicate
(repo_id, pr_number, content_hash) tuples but accepts the same
content hash across different PRs or repos.

Refs: docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md §3.9

Branch: cairn-simplifier-data-model -> cairn

Commits:
- fix(cairn): pr_summary UNIQUE must be composite (repo, pr, hash)

Files changed:
- models/cairn/pr_summary.go
- models/cairn/migrations/v501_create_summarizer_tables_test.go

Diff:
```diff
--- a/models/cairn/pr_summary.go
+++ b/models/cairn/pr_summary.go
@@ -8,9 +8,9 @@ package cairn
 // for audit (no automatic cleanup in MVP).
 type PRSummary struct {
 	ID            int64  `xorm:"pk autoincr"`
-	RepoID        int64  `xorm:"INDEX(repo_pr) NOT NULL"`
-	PRNumber      int64  `xorm:"INDEX(repo_pr) NOT NULL"`
-	ContentHash   string `xorm:"VARCHAR(64) UNIQUE(repo_pr_hash) NOT NULL"`
+	RepoID        int64  `xorm:"INDEX(repo_pr) UNIQUE(repo_pr_hash) NOT NULL"`
+	PRNumber      int64  `xorm:"INDEX(repo_pr) UNIQUE(repo_pr_hash) NOT NULL"`
+	ContentHash   string `xorm:"VARCHAR(64) UNIQUE(repo_pr_hash) NOT NULL"`
 	SummaryMD     string `xorm:"TEXT NOT NULL"`
 	ModelID       string `xorm:"VARCHAR(255) NOT NULL"`
 	TokenCount    int    `xorm:"NOT NULL DEFAULT 0"`
```
