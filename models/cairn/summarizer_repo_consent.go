//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

// DataScope is what gets sent to the AI service for a private repo.
type DataScope string

const (
	DataScopeFull           DataScope = "full"
	DataScopeCommitMessages DataScope = "commit-messages"
	DataScopeMetadata       DataScope = "metadata"
)

func (s DataScope) IsValid() bool {
	switch s {
	case DataScopeFull, DataScopeCommitMessages, DataScopeMetadata:
		return true
	}
	return false
}

// SummarizerRepoConsent is per-repo opt-in for private repos.
// Only consulted for private repos; public repos run on org config alone.
type SummarizerRepoConsent struct {
	RepoID      int64     `xorm:"pk"`
	Enabled     bool      `xorm:"NOT NULL DEFAULT false"`
	DataScope   DataScope `xorm:"VARCHAR(32) NOT NULL DEFAULT 'metadata'"`
	CreatedUnix int64     `xorm:"created"`
	UpdatedUnix int64     `xorm:"updated"`
}

func (SummarizerRepoConsent) TableName() string { return "cairn_summarizer_repo_consent" }
