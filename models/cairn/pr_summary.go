//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

// PRSummary is a cached simplifier output for a PR at a particular content hash.
// New rows are created when the PR's content hash changes; old rows are kept
// for audit (no automatic cleanup in MVP).
type PRSummary struct {
	ID            int64  `xorm:"pk autoincr"`
	RepoID        int64  `xorm:"INDEX(repo_pr) UNIQUE(repo_pr_hash) NOT NULL"`
	PRNumber      int64  `xorm:"INDEX(repo_pr) UNIQUE(repo_pr_hash) NOT NULL"`
	ContentHash   string `xorm:"VARCHAR(64) UNIQUE(repo_pr_hash) NOT NULL"`
	SummaryMD     string `xorm:"TEXT NOT NULL"`
	ModelID       string `xorm:"VARCHAR(255) NOT NULL"`
	TokenCount    int    `xorm:"NOT NULL DEFAULT 0"`
	GeneratedUnix int64  `xorm:"'generated_unix' created"`
}

func (PRSummary) TableName() string { return "cairn_pr_summary" }
