//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

import "time"

// LevelFlag is a bitmask of which summarization scopes the org has opted into.
type LevelFlag int

const (
	LevelPR     LevelFlag = 1 << 0
	LevelCommit LevelFlag = 1 << 1
	LevelFile   LevelFlag = 1 << 2
)

func (l LevelFlag) Has(target LevelFlag) bool { return l&target != 0 }

// SummarizerConfig is per-org configuration for the simplifier.
// Credentials are stored AES-GCM-encrypted; the encryption key is derived
// from the instance HMAC key.
//
// Provider names match bridle.ProviderID values: "claude-api", "openai-api",
// "bedrock", "ollama-local", "claude-code".
type SummarizerConfig struct {
	OwnerID           int64     `xorm:"pk"`
	Enabled           bool      `xorm:"NOT NULL DEFAULT false"`
	Provider          string    `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"`
	EndpointURL       string    `xorm:"VARCHAR(1024) NOT NULL DEFAULT ''"`
	ModelID           string    `xorm:"VARCHAR(255) NOT NULL DEFAULT ''"`
	CredentialsCipher []byte    `xorm:"BLOB"`
	LevelsEnabled     LevelFlag `xorm:"NOT NULL DEFAULT 1"`
	CreatedUnix       int64     `xorm:"created"`
	UpdatedUnix       int64     `xorm:"updated"`
}

func (SummarizerConfig) TableName() string { return "cairn_summarizer_config" }

func (c *SummarizerConfig) IsConfigured() bool {
	return c != nil && c.Enabled && c.EndpointURL != "" && len(c.CredentialsCipher) > 0
}

func (c *SummarizerConfig) UpdatedAt() time.Time { return time.Unix(c.UpdatedUnix, 0) }
