# Cairn Simplifier — Implementation Plan (Plan 5)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the per-org-configurable AI summarization feature that turns AI-authored PRs into scrutable summaries for human reviewers.

**Architecture:** Server-side worker that calls a per-org-configured AI provider via **bridle**. The org picks a bridle provider (claudecode / openai-api / claude-api / bedrock / ollama-local) and supplies model + credentials; cairn constructs a one-shot bridle harness per summarization (no tools, single user message) and stores the result. Summaries cached per PR-content-hash; rendered into the PR HTML page, the `?format=md` PR view, and an API endpoint. The simplifier posts as the system actor `cairn` (not as an agent identity). Public repos auto-enable when the org has a service configured; private repos require explicit per-repo opt-in with a data-scope picker.

**Tech Stack:**
- Go 1.25+, xorm ORM, SQLite (Forgejo substrate)
- `services/cairn/` pattern (atomic.Pointer global, connection-per-operation)
- `cairntest.NewEngine` for in-memory test DBs (extended for new tables)
- `routers/api/cairn/v1/` for API handlers (matches Plan 2a registration API style)
- `routers/web/cairn/` for HTML rendering (matches Plan 4 markdown style)
- `crypto/aes` GCM for credential encryption-at-rest, key derived from instance HMAC key
- Event listener pattern via Forgejo's `notification` package (PR open/sync events)
- **`github.com/nexus-cw/bridle`** for the AI provider abstraction — bridle handles per-provider details (claude-api, openai-api, bedrock, ollama-local, claudecode) and exposes a unified `Provider.RunTurn` surface. Cairn does not write its own HTTP client; it constructs a bridle harness with one provider and runs a single no-tools turn per summarization. Reuses the team's tested AI substrate; consistent with how other aspects make AI calls.
  - **Note on import path:** bridle's module is currently `github.com/nexus-cw/bridle`; the org migration to `CarriedWorldUniverse` is pending for that repo. Implementer verifies the actual path from `~/Source/bridle/go.mod` at Task 3 start; if migrated, use the new path.
  - **Provider readiness:** bridle's harness + `claudecode` provider have passing tests; `claude`/`openai`/`bedrock`/`ollama` providers are scaffolded but unverified. MVP needs `claudecode` (Jacinta's actual deploy uses Claude via her subscription) and `openai` (most common self-hoster path). Implementer surfaces NEEDS_CONTEXT if any required provider is non-functional.

**Spec reference:** [`docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md`](../specs/2026-05-10-cairn-ai-native-amendment.md) §3.

**Build invariants** (preserved from prior Cairn plans):
- Cairn-original code carries the AGPL header (`// Package cairn — ... // Cairn-specific code; AGPLv3. See LICENSING.md.`).
- Forgejo upstream patches are minimal (3-line short-circuits in handlers, bracketed with comments for future rebase).
- Migrations are additive, never alter existing Forgejo tables.
- Tests use `cairntest.NewEngine(t)`; no external service dependencies (mock HTTP servers for AI client).
- `errors.Is` for sentinel matching; pkg/errors not used.
- No defensive comments, no over-validation, no defensive nil-guards beyond Forgejo's existing patterns.

---

## File structure

**New files:**

```
models/cairn/
├── summarizer_config.go              ← per-org config (provider, endpoint, encrypted creds, levels)
├── summarizer_repo_consent.go        ← per-repo consent for private repos
├── pr_summary.go                     ← cached summary keyed by content hash
└── migrations/
    └── v501_create_summarizer_tables.go

services/cairn/summarizer/
├── bridle_adapter.go                 ← constructs a bridle.Provider from SummarizerConfig
├── prompt.go                         ← embedded standardized prompt + context builder
├── service.go                        ← orchestrator (Generate, GetCached, Regenerate)
├── crypto.go                         ← credential encryption helpers (AES-GCM)
├── events.go                         ← Forgejo event listener + debounce
└── global.go                         ← atomic.Pointer global access

routers/api/cairn/v1/
└── summarizer.go                     ← API handlers (org/repo config, summary, regen)

routers/web/cairn/
├── summarizer_admin.go               ← org admin settings rendering
├── summarizer_repo.go                ← repo settings rendering (private only)
├── summarizer_pr_block.go            ← PR-page summary block helper
└── templates/summarizer/
    ├── pr-summary-block.tmpl         ← summary-at-top-of-PR HTML template
    └── pr-summary.md.tmpl            ← markdown form for ?format=md
```

**Modified Forgejo upstream files (~30 lines total):**

- `routers/init.go` — register API routes for summarizer; wire event listener at startup
- `routers/web/repo/pull.go` — render summary block at top of PR page (3-line short-circuit)
- `modules/setting/cairn.go` — add `Cairn.SummarizerEnabled` (global gate, default true)
- `routers/web/cairn/markdown.go` — inline summary into `RenderPullRequest` markdown view
- `routers/web/cairn/wellknown.go` — advertise `features.simplifier_enabled` in manifest
- `templates/repo/issue/view_content/pull_header.tmpl` (or equivalent) — single-line `{{template ...}}` include for the summary block

---

## Task 1: Data model + migration

**Files:**
- Create: `models/cairn/summarizer_config.go`
- Create: `models/cairn/summarizer_repo_consent.go`
- Create: `models/cairn/pr_summary.go`
- Create: `models/cairn/migrations/v501_create_summarizer_tables.go`
- Modify: `models/cairn/cairntest/engine.go` (add the new migration)
- Test: `models/cairn/migrations/v501_create_summarizer_tables_test.go`

- [ ] **Step 1: Write the failing migration test**

```go
// models/cairn/migrations/v501_create_summarizer_tables_test.go
package migrations_test

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

func TestV501CreateSummarizerTables(t *testing.T) {
	eng := cairntest.NewEngine(t)

	for _, table := range []string{"cairn_summarizer_config", "cairn_summarizer_repo_consent", "cairn_pr_summary"} {
		exists, err := eng.IsTableExist(table)
		if err != nil {
			t.Fatalf("IsTableExist(%q): %v", table, err)
		}
		if !exists {
			t.Errorf("table %q not created", table)
		}
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

```bash
go test ./models/cairn/migrations/... -run TestV501
```

Expected: FAIL — tables don't exist (or migration fn doesn't exist).

- [ ] **Step 3: Write the model structs**

```go
// models/cairn/summarizer_config.go
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
// "bedrock", "ollama-local", "claudecode".
type SummarizerConfig struct {
	OwnerID            int64     `xorm:"pk"`
	Enabled            bool      `xorm:"NOT NULL DEFAULT false"`
	Provider           string    `xorm:"VARCHAR(64) NOT NULL DEFAULT ''"` // bridle.ProviderID as string
	EndpointURL        string    `xorm:"VARCHAR(1024) NOT NULL DEFAULT ''"` // optional; for openai-compat self-hosted, ollama
	ModelID            string    `xorm:"VARCHAR(255) NOT NULL DEFAULT ''"`
	CredentialsCipher  []byte    `xorm:"BLOB"`                              // API key for claude/openai; AWS profile name for bedrock; binary path for claudecode
	LevelsEnabled      LevelFlag `xorm:"NOT NULL DEFAULT 1"`                 // PR by default
	CreatedUnix        int64     `xorm:"created"`
	UpdatedUnix        int64     `xorm:"updated"`
}

func (SummarizerConfig) TableName() string { return "cairn_summarizer_config" }

func (c *SummarizerConfig) IsConfigured() bool {
	return c != nil && c.Enabled && c.EndpointURL != "" && len(c.CredentialsCipher) > 0
}

func (c *SummarizerConfig) UpdatedAt() time.Time { return time.Unix(c.UpdatedUnix, 0) }
```

```go
// models/cairn/summarizer_repo_consent.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

// DataScope is what gets sent to the AI service for a private repo.
type DataScope string

const (
	DataScopeFull           DataScope = "full"             // title + body + full diff + commit messages
	DataScopeCommitMessages DataScope = "commit-messages"  // title + body + commit messages, no diff
	DataScopeMetadata       DataScope = "metadata"         // title + body + file paths only
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
```

```go
// models/cairn/pr_summary.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

// PRSummary is a cached simplifier output for a PR at a particular content hash.
// New rows are created when the PR's content hash changes; old rows are kept
// for audit (no automatic cleanup in MVP).
type PRSummary struct {
	ID           int64  `xorm:"pk autoincr"`
	RepoID       int64  `xorm:"INDEX(repo_pr) NOT NULL"`
	PRNumber     int64  `xorm:"INDEX(repo_pr) NOT NULL"`
	ContentHash  string `xorm:"VARCHAR(64) UNIQUE(repo_pr_hash) NOT NULL"`
	SummaryMD    string `xorm:"TEXT NOT NULL"`
	ModelID      string `xorm:"VARCHAR(255) NOT NULL"`
	TokenCount   int    `xorm:"NOT NULL DEFAULT 0"`
	GeneratedUnix int64 `xorm:"created"`
}

func (PRSummary) TableName() string { return "cairn_pr_summary" }
```

- [ ] **Step 4: Write the migration function**

```go
// models/cairn/migrations/v501_create_summarizer_tables.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package migrations

import (
	"xorm.io/xorm"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// V501CreateSummarizerTables adds the simplifier tables. Additive only;
// no Forgejo schema is touched.
func V501CreateSummarizerTables(x *xorm.Engine) error {
	if err := x.Table("cairn_summarizer_config").Sync2(new(cairnmodels.SummarizerConfig)); err != nil {
		return err
	}
	if err := x.Table("cairn_summarizer_repo_consent").Sync2(new(cairnmodels.SummarizerRepoConsent)); err != nil {
		return err
	}
	if err := x.Table("cairn_pr_summary").Sync2(new(cairnmodels.PRSummary)); err != nil {
		return err
	}
	return nil
}
```

- [ ] **Step 5: Wire the migration into the test harness**

```go
// models/cairn/cairntest/engine.go — append after V500
import cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"

// ... in NewEngine:
if err := cairnmigrations.V500CreateAgentTables(eng); err != nil {
    t.Fatalf("V500: %v", err)
}
if err := cairnmigrations.V501CreateSummarizerTables(eng); err != nil {
    t.Fatalf("V501: %v", err)
}
```

(Read the existing engine.go first; the V500 wiring exists. Add V501 next to it.)

- [ ] **Step 6: Run test, verify it passes**

```bash
go test ./models/cairn/migrations/... -run TestV501
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git checkout -b cairn-simplifier-data-model
git add models/cairn/summarizer_config.go models/cairn/summarizer_repo_consent.go models/cairn/pr_summary.go models/cairn/migrations/v501_create_summarizer_tables.go models/cairn/migrations/v501_create_summarizer_tables_test.go models/cairn/cairntest/engine.go
git commit -m "feat(cairn): simplifier data model + migration

Adds three Cairn-original tables for the AI-native simplifier feature:

- cairn_summarizer_config: per-org config (endpoint, encrypted creds,
  level bitmask). Default off; admin opts in. Credentials encrypted
  at rest by AES-GCM with key derived from the instance HMAC key
  (encryption logic lands in a later task).
- cairn_summarizer_repo_consent: per-repo opt-in for private repos
  with data-scope picker (full / commit-messages / metadata).
- cairn_pr_summary: cached output keyed by repo_id + pr_number +
  content_hash. New rows on content change; no automatic cleanup
  in MVP.

Migration V501 is additive; no Forgejo tables are touched.

Refs: docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md §3.9"
```

---

## Task 2: Credential encryption (AES-GCM)

**Files:**
- Create: `services/cairn/summarizer/crypto.go`
- Test: `services/cairn/summarizer/crypto_test.go`

The simplifier stores AI-service credentials at rest. They're encrypted with AES-GCM using a key derived from the instance HMAC key (same root of trust as Cairn's fingerprint HMAC, so no new key-management surface).

- [ ] **Step 1: Write the failing roundtrip test**

```go
// services/cairn/summarizer/crypto_test.go
package summarizer

import (
	"bytes"
	"testing"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	hmacKey := bytes.Repeat([]byte{0xab}, 32)
	plaintext := []byte("sk-test-credential-1234567890")

	cipher, err := EncryptCredential(hmacKey, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if bytes.Contains(cipher, plaintext) {
		t.Fatal("plaintext appears in ciphertext")
	}

	out, err := DecryptCredential(hmacKey, cipher)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if !bytes.Equal(out, plaintext) {
		t.Fatalf("roundtrip mismatch: got %q want %q", out, plaintext)
	}
}

func TestDecryptRejectsTamperedCiphertext(t *testing.T) {
	hmacKey := bytes.Repeat([]byte{0xab}, 32)
	cipher, _ := EncryptCredential(hmacKey, []byte("secret"))
	cipher[len(cipher)-1] ^= 0x01 // flip a bit
	if _, err := DecryptCredential(hmacKey, cipher); err == nil {
		t.Fatal("expected decryption to fail on tampered ciphertext")
	}
}

func TestDecryptRejectsWrongKey(t *testing.T) {
	cipher, _ := EncryptCredential(bytes.Repeat([]byte{0xab}, 32), []byte("secret"))
	if _, err := DecryptCredential(bytes.Repeat([]byte{0xcd}, 32), cipher); err == nil {
		t.Fatal("expected decryption to fail under wrong key")
	}
}
```

- [ ] **Step 2: Run, verify failure**

```bash
go test ./services/cairn/summarizer/... -run TestEncrypt
```

Expected: build error (functions don't exist).

- [ ] **Step 3: Implement `EncryptCredential` / `DecryptCredential`**

```go
// services/cairn/summarizer/crypto.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
)

// derivedAESKey returns a 32-byte AES key derived from the instance HMAC key.
// HKDF-equivalent for a single output: SHA-256(hmacKey || "cairn-summarizer-v1").
func derivedAESKey(hmacKey []byte) []byte {
	h := sha256.New()
	h.Write(hmacKey)
	h.Write([]byte("cairn-summarizer-v1"))
	return h.Sum(nil) // 32 bytes
}

// EncryptCredential AES-256-GCM-encrypts plaintext with a key derived from hmacKey.
// Output format: nonce(12) || ciphertext.
func EncryptCredential(hmacKey, plaintext []byte) ([]byte, error) {
	if len(hmacKey) < 16 {
		return nil, errors.New("hmac key too short")
	}
	block, err := aes.NewCipher(derivedAESKey(hmacKey))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := gcm.Seal(nonce, nonce, plaintext, nil)
	return out, nil
}

// DecryptCredential reverses EncryptCredential.
var ErrInvalidCiphertext = errors.New("summarizer: invalid ciphertext")

func DecryptCredential(hmacKey, ciphertext []byte) ([]byte, error) {
	if len(hmacKey) < 16 {
		return nil, errors.New("hmac key too short")
	}
	block, err := aes.NewCipher(derivedAESKey(hmacKey))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, ErrInvalidCiphertext
	}
	nonce, body := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, body, nil)
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return plaintext, nil
}
```

- [ ] **Step 4: Run all three tests, verify pass**

```bash
go test ./services/cairn/summarizer/... -run 'TestEncrypt|TestDecrypt'
```

Expected: PASS, PASS, PASS.

- [ ] **Step 5: Commit**

```bash
git add services/cairn/summarizer/crypto.go services/cairn/summarizer/crypto_test.go
git commit -m "feat(cairn): credential encryption for simplifier (AES-GCM)

Stores per-org AI-service credentials at rest using AES-256-GCM with
a key derived from the instance HMAC key (SHA-256(hmacKey ||
'cairn-summarizer-v1')). Reuses the existing root of trust; no new
key-management surface.

Tests cover roundtrip, tampered-ciphertext detection, and
wrong-key rejection.

Refs: docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md §3.9"
```

---

## Task 3: Bridle adapter + standardized prompt

**Files:**
- Create: `services/cairn/summarizer/bridle_adapter.go`
- Create: `services/cairn/summarizer/prompt.go`
- Test: `services/cairn/summarizer/bridle_adapter_test.go`
- Modify: `go.mod` (add bridle as dependency)

Bridle is the provider abstraction. Cairn's adapter constructs a `bridle.Provider` from a `SummarizerConfig`, runs a single no-tools turn (one user message, zero tool calls expected), and extracts the final text.

**Step 0 — verify bridle's actual import path** before committing the dependency:

```bash
head -3 ~/Source/bridle/go.mod
# Use the path shown there. As of plan-write time it's
# github.com/nexus-cw/bridle; if migrated to CarriedWorldUniverse
# update the imports below.
```

- [ ] **Step 1: Write the failing adapter test using bridle's `fake` package**

bridle ships a `fake` provider for tests. Use it to verify that the adapter constructs a working harness, runs one turn with a single user message, and returns the final text.

```go
// services/cairn/summarizer/bridle_adapter_test.go
package summarizer

import (
	"context"
	"testing"

	"github.com/nexus-cw/bridle"
	"github.com/nexus-cw/bridle/fake"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

func TestSummarizer_RunsOneTurnViaBridle(t *testing.T) {
	// fake.NewProvider returns a Provider that emits scripted responses.
	// (Implementer: read ~/Source/bridle/fake/* for the actual constructor
	// signature; pattern below is illustrative.)
	provider := fake.NewProvider(fake.Script{FinalText: "summary out", Tokens: 17})

	s, err := NewSummarizerWithProvider(provider, "fake-model")
	if err != nil {
		t.Fatalf("NewSummarizer: %v", err)
	}

	resp, err := s.Complete(context.Background(), "system", "user")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Content != "summary out" {
		t.Errorf("content = %q", resp.Content)
	}
	if resp.TokenCount != 17 {
		t.Errorf("tokens = %d", resp.TokenCount)
	}
}

func TestBuildBridleProviderFromConfig_DispatchesByProvider(t *testing.T) {
	// For each supported provider name, BuildBridleProviderFromConfig
	// returns a non-nil provider (or the documented "not yet supported" sentinel).
	cases := []struct {
		name    string
		wantErr bool
	}{
		{"claudecode", false}, // primary MVP path
		{"openai-api", false}, // primary MVP path
		{"unknown", true},
	}
	for _, tc := range cases {
		cfg := &cairnmodels.SummarizerConfig{
			Provider:    tc.name,
			ModelID:     "x",
			EndpointURL: "https://example",
		}
		_, err := BuildBridleProviderFromConfig(cfg, []byte("api-key"))
		if tc.wantErr && err == nil {
			t.Errorf("provider=%s: expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("provider=%s: unexpected error: %v", tc.name, err)
		}
	}
}
```

- [ ] **Step 2: Run, verify failure**

```bash
go test ./services/cairn/summarizer/... -run 'TestSummarizer_RunsOneTurn|TestBuildBridleProvider'
```

Expected: build error (functions don't exist; bridle not in go.mod).

- [ ] **Step 3: Add bridle to `go.mod`**

```bash
cd ~/Source/cairn
go get github.com/nexus-cw/bridle@latest
# (or the migrated path if bridle has moved to CarriedWorldUniverse by now)
```

- [ ] **Step 4: Implement the adapter**

```go
// services/cairn/summarizer/bridle_adapter.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"context"
	"errors"
	"fmt"

	"github.com/nexus-cw/bridle"
	"github.com/nexus-cw/bridle/provider/claudecode"
	"github.com/nexus-cw/bridle/provider/openai"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// AIResponse is what the simplifier needs from one AI turn.
type AIResponse struct {
	Content    string
	ModelID    string
	TokenCount int
}

// AIClient is the interface the service layer depends on. Implemented by
// *Summarizer in production; tests inject a mockClient.
type AIClient interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (*AIResponse, error)
}

// Summarizer wraps a bridle harness configured for one no-tools turn.
// Implements AIClient.
type Summarizer struct {
	harness *bridle.Harness
	model   string
}

func NewSummarizerWithProvider(p bridle.Provider, model string) (*Summarizer, error) {
	if p == nil {
		return nil, errors.New("summarizer: nil provider")
	}
	h, err := bridle.NewHarness(bridle.HarnessConfig{Provider: p})
	if err != nil {
		return nil, fmt.Errorf("summarizer: harness: %w", err)
	}
	return &Summarizer{harness: h, model: model}, nil
}

// Complete runs one bridle turn with a single user message and no tools,
// returning the final text + usage.
func (s *Summarizer) Complete(ctx context.Context, systemPrompt, userPrompt string) (*AIResponse, error) {
	req := bridle.TurnRequest{
		AspectID:     "cairn-simplifier",
		SystemPrompt: systemPrompt,
		Model:        s.model,
		Messages:     []bridle.ProviderMessage{{Role: "user", Content: userPrompt}},
		Tools:        nil,
		MaxSteps:     1,
	}
	result, err := s.harness.RunTurn(ctx, req, /* ToolRunner */ nil, /* EventSink */ nil)
	if err != nil {
		return nil, fmt.Errorf("summarizer: bridle: %w", err)
	}
	return &AIResponse{
		Content:    result.FinalText,
		ModelID:    s.model,
		TokenCount: result.Usage.InputTokens + result.Usage.OutputTokens,
	}, nil
}

// BuildBridleProviderFromConfig dispatches on Config.Provider to construct
// the right bridle.Provider implementation, threading the decrypted API key
// (or whatever credential the provider needs) through.
//
// Supported providers in MVP:
//   - "claudecode"  — runs Claude via Claude Code subprocess (Jacinta's path)
//   - "openai-api"  — OpenAI-compatible chat completions (self-hoster path)
//
// Other bridle providers (claude-api native, bedrock, ollama-local) will be
// wired post-MVP as they become production-ready in bridle.
func BuildBridleProviderFromConfig(cfg *cairnmodels.SummarizerConfig, apiKey []byte) (bridle.Provider, error) {
	switch cfg.Provider {
	case "claudecode":
		// claudecode provider config: see ~/Source/bridle/provider/claudecode
		// for the actual constructor signature; adapt as needed.
		return claudecode.NewProvider(claudecode.Config{
			BinaryPath: cfg.EndpointURL, // optional: path to `claude` binary; default if empty
			Model:      cfg.ModelID,
		}), nil
	case "openai-api":
		return openai.NewProvider(openai.Config{
			Endpoint: cfg.EndpointURL,
			APIKey:   string(apiKey),
			Model:    cfg.ModelID,
		}), nil
	case "":
		return nil, errors.New("summarizer: no provider configured")
	default:
		return nil, fmt.Errorf("summarizer: unsupported provider %q (MVP supports: claudecode, openai-api)", cfg.Provider)
	}
}
```

(Implementer: the constructor signatures shown for `claudecode.NewProvider` / `openai.NewProvider` are illustrative — read the actual provider packages and adapt. The adapter's *interface* — dispatch on config.Provider, return a `bridle.Provider` — is fixed; the config-struct shapes per provider may differ.)

- [ ] **Step 4: Implement standardized prompt + context builder**

```go
// services/cairn/summarizer/prompt.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"fmt"
	"strings"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// SystemPrompt is the standardized prompt the simplifier ships with.
// Per-org override is a v1.x feature; MVP ships one tunable unit.
const SystemPrompt = `You are Cairn, the simplifier.

Your job is to rewrite a pull request's content into a short, plain-language
summary that helps a human reviewer decide whether to drill in. You do NOT
review, judge, grade, or recommend anything. You compress.

Output format:
- Start with one sentence describing what the PR changes.
- Then 2-4 bullet points: the major moving parts, in plain language.
- Keep it under 200 words. Skip generic phrases like "this commit".
- If the PR is trivial (typo fix, version bump), say so in one sentence
  and stop.

Do not output code blocks unless they're directly necessary to the
explanation. Do not add caveats, disclaimers, or commentary about your
own limitations.`

// PRContext is the input to the prompt builder. The orchestrator fills
// this in based on the per-repo data scope.
type PRContext struct {
	Title          string
	Body           string
	BaseBranch     string
	HeadBranch     string
	CommitMessages []string
	FilePaths      []string
	Diff           string // empty if data scope excludes diff
}

// BuildUserPrompt formats the PRContext into the user message sent to
// the AI. Sections are clearly labelled so the AI can ignore missing ones.
func BuildUserPrompt(c PRContext) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pull request: %s\n", c.Title)
	if c.Body != "" {
		fmt.Fprintf(&b, "\nDescription:\n%s\n", strings.TrimSpace(c.Body))
	}
	if c.BaseBranch != "" || c.HeadBranch != "" {
		fmt.Fprintf(&b, "\nBranch: %s -> %s\n", c.HeadBranch, c.BaseBranch)
	}
	if len(c.CommitMessages) > 0 {
		b.WriteString("\nCommits:\n")
		for _, msg := range c.CommitMessages {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(msg))
		}
	}
	if len(c.FilePaths) > 0 {
		b.WriteString("\nFiles changed:\n")
		for _, p := range c.FilePaths {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	if c.Diff != "" {
		fmt.Fprintf(&b, "\nDiff:\n```diff\n%s\n```\n", c.Diff)
	}
	return b.String()
}

// SelectFields returns a PRContext populated according to the data scope.
// Source is the full PR data; the function strips fields not allowed by scope.
func SelectFields(scope cairnmodels.DataScope, full PRContext) PRContext {
	out := PRContext{Title: full.Title, Body: full.Body, BaseBranch: full.BaseBranch, HeadBranch: full.HeadBranch}
	switch scope {
	case cairnmodels.DataScopeFull:
		return full
	case cairnmodels.DataScopeCommitMessages:
		out.CommitMessages = full.CommitMessages
		return out
	case cairnmodels.DataScopeMetadata:
		out.FilePaths = full.FilePaths
		return out
	}
	// Unknown scope: degrade to metadata.
	out.FilePaths = full.FilePaths
	return out
}
```

- [ ] **Step 5: Run client tests, verify pass**

```bash
go test ./services/cairn/summarizer/... -run 'TestSummarizer_RunsOneTurn|TestBuildBridleProvider'
```

Expected: PASS, PASS.

- [ ] **Step 6: Add a prompt-builder test**

```go
// services/cairn/summarizer/prompt_test.go
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
}

func TestSelectFields_CommitMessagesExcludesDiff(t *testing.T) {
	full := PRContext{Title: "T", Diff: "DIFF", CommitMessages: []string{"c1"}}
	got := SelectFields(cairnmodels.DataScopeCommitMessages, full)
	if got.Diff != "" {
		t.Error("commit-messages scope must not include diff")
	}
	if len(got.CommitMessages) != 1 {
		t.Error("commit-messages scope must include commit messages")
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

func TestBuildUserPrompt_OmitsEmptySections(t *testing.T) {
	out := BuildUserPrompt(PRContext{Title: "T"})
	if strings.Contains(out, "Diff:") || strings.Contains(out, "Commits:") {
		t.Errorf("empty sections should be omitted: %s", out)
	}
}
```

Run: `go test ./services/cairn/summarizer/... -run 'TestSelect|TestBuild'` — expect PASS.

- [ ] **Step 7: Commit**

```bash
git add services/cairn/summarizer/bridle_adapter.go services/cairn/summarizer/prompt.go services/cairn/summarizer/bridle_adapter_test.go services/cairn/summarizer/prompt_test.go go.mod go.sum
git commit -m "feat(cairn): simplifier bridle adapter + standardized prompt

Wraps a bridle harness for one no-tools turn per summarization.
BuildBridleProviderFromConfig dispatches on Config.Provider to
construct the right bridle.Provider (claudecode + openai-api in MVP;
claude-api / bedrock / ollama-local follow as bridle providers
become production-ready). Cairn does not write its own HTTP client;
all AI calls go through bridle.

Standardized prompt is constant SystemPrompt; per-org override is a
v1.x feature. PRContext + BuildUserPrompt format the input. SelectFields
strips fields not allowed by the per-repo data scope (full /
commit-messages / metadata).

Refs: docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md §3.4"
```

---

## Task 4: Service orchestration + cache

**Files:**
- Create: `services/cairn/summarizer/service.go`
- Create: `services/cairn/summarizer/global.go`
- Test: `services/cairn/summarizer/service_test.go`

`Service` is the public entry point. Methods:
- `EnsureSummary(ctx, repo, pr) (*PRSummary, error)` — checks cache, generates if missing/stale, returns the summary
- `RegenerateSummary(ctx, repo, pr) (*PRSummary, error)` — forces regeneration regardless of cache
- `GetCachedSummary(ctx, repoID, prNumber) (*PRSummary, error)` — read-only cache lookup, returns sentinel `ErrNoSummary` if absent

The service is engine-injectable for testability (production wires the real xorm engine + AI client; tests wire in-memory engine + mock client).

- [ ] **Step 1: Write the failing service test**

```go
// services/cairn/summarizer/service_test.go
package summarizer

import (
	"context"
	"errors"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

type mockClient struct {
	calls    int
	response string
}

func (m *mockClient) Complete(_ context.Context, _, _ string) (*AIResponse, error) {
	m.calls++
	return &AIResponse{Content: m.response, ModelID: "mock", TokenCount: 10}, nil
}

func TestEnsureSummary_GeneratesOnFirstCall(t *testing.T) {
	eng := cairntest.NewEngine(t)
	cli := &mockClient{response: "first summary"}
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		return cli, &cairnmodels.SummarizerConfig{Enabled: true, EndpointURL: "x"}, nil
	})

	ctx := PRContext{Title: "Test PR", FilePaths: []string{"a.go"}}
	got, err := svc.EnsureSummary(context.Background(), 100, 1, 200, ctx, cairnmodels.DataScopeMetadata)
	if err != nil {
		t.Fatalf("EnsureSummary: %v", err)
	}
	if got.SummaryMD != "first summary" {
		t.Errorf("summary = %q", got.SummaryMD)
	}
	if cli.calls != 1 {
		t.Errorf("client calls = %d, want 1", cli.calls)
	}
}

func TestEnsureSummary_CachesByContentHash(t *testing.T) {
	eng := cairntest.NewEngine(t)
	cli := &mockClient{response: "cached"}
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		return cli, &cairnmodels.SummarizerConfig{Enabled: true, EndpointURL: "x"}, nil
	})

	ctx := PRContext{Title: "Same PR", FilePaths: []string{"a.go"}}
	_, _ = svc.EnsureSummary(context.Background(), 100, 1, 200, ctx, cairnmodels.DataScopeMetadata)
	_, _ = svc.EnsureSummary(context.Background(), 100, 1, 200, ctx, cairnmodels.DataScopeMetadata)
	if cli.calls != 1 {
		t.Errorf("client calls = %d, want 1 (second call should hit cache)", cli.calls)
	}
}

func TestEnsureSummary_RegeneratesOnContentChange(t *testing.T) {
	eng := cairntest.NewEngine(t)
	cli := &mockClient{response: "v"}
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		return cli, &cairnmodels.SummarizerConfig{Enabled: true, EndpointURL: "x"}, nil
	})

	_, _ = svc.EnsureSummary(context.Background(), 100, 1, 200, PRContext{Title: "v1"}, cairnmodels.DataScopeMetadata)
	_, _ = svc.EnsureSummary(context.Background(), 100, 1, 200, PRContext{Title: "v2"}, cairnmodels.DataScopeMetadata)
	if cli.calls != 2 {
		t.Errorf("client calls = %d, want 2 (content changed)", cli.calls)
	}
}

func TestGetCachedSummary_ReturnsErrNoSummaryIfMissing(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := NewService(eng, nil)
	_, err := svc.GetCachedSummary(context.Background(), 999, 999)
	if !errors.Is(err, ErrNoSummary) {
		t.Errorf("err = %v, want ErrNoSummary", err)
	}
}

func TestEnsureSummary_NoConfigReturnsErr(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		return nil, &cairnmodels.SummarizerConfig{Enabled: false}, nil
	})
	_, err := svc.EnsureSummary(context.Background(), 100, 1, 200, PRContext{Title: "x"}, cairnmodels.DataScopeMetadata)
	if !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}
```

- [ ] **Step 2: Run, verify failure**

```bash
go test ./services/cairn/summarizer/... -run TestEnsure
```

Expected: build error (Service doesn't exist).

- [ ] **Step 3: Implement service**

```go
// services/cairn/summarizer/service.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"xorm.io/xorm"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

var (
	ErrNoSummary     = errors.New("summarizer: no cached summary")
	ErrNotConfigured = errors.New("summarizer: org has no AI service configured")
)

// ConfigResolver looks up the AI client + config for an owner. Production
// wires this to read SummarizerConfig from the engine, decrypt credentials,
// and construct a bridle-backed Summarizer. Tests inject a mockClient.
type ConfigResolver func(ownerID int64) (AIClient, *cairnmodels.SummarizerConfig, error)

type Service struct {
	engine   *xorm.Engine
	resolver ConfigResolver
}

func NewService(engine *xorm.Engine, resolver ConfigResolver) *Service {
	return &Service{engine: engine, resolver: resolver}
}

// HashPRContext returns a deterministic content hash. Stable input produces
// stable hash; cache keys off this.
func HashPRContext(ctx PRContext) string {
	h := sha256.New()
	fmt.Fprintf(h, "T:%s\nB:%s\nBB:%s\nHB:%s\n", ctx.Title, ctx.Body, ctx.BaseBranch, ctx.HeadBranch)
	for _, m := range ctx.CommitMessages {
		fmt.Fprintf(h, "C:%s\n", m)
	}
	for _, p := range ctx.FilePaths {
		fmt.Fprintf(h, "F:%s\n", p)
	}
	fmt.Fprintf(h, "D:%s\n", ctx.Diff)
	return hex.EncodeToString(h.Sum(nil))
}

// EnsureSummary returns the cached summary if present at the given content
// hash, or generates+stores a new one.
func (s *Service) EnsureSummary(ctx context.Context, repoID, prNumber, ownerID int64, prCtx PRContext, scope cairnmodels.DataScope) (*cairnmodels.PRSummary, error) {
	scoped := SelectFields(scope, prCtx)
	hash := HashPRContext(scoped)

	cached := &cairnmodels.PRSummary{}
	has, err := s.engine.Where("repo_id = ? AND pr_number = ? AND content_hash = ?", repoID, prNumber, hash).Get(cached)
	if err != nil {
		return nil, fmt.Errorf("summarizer: cache lookup: %w", err)
	}
	if has {
		return cached, nil
	}

	if s.resolver == nil {
		return nil, ErrNotConfigured
	}
	client, cfg, err := s.resolver(ownerID)
	if err != nil {
		return nil, fmt.Errorf("summarizer: resolve config: %w", err)
	}
	if cfg == nil || !cfg.Enabled || client == nil {
		return nil, ErrNotConfigured
	}

	resp, err := client.Complete(ctx, SystemPrompt, BuildUserPrompt(scoped))
	if err != nil {
		return nil, fmt.Errorf("summarizer: ai call: %w", err)
	}

	row := &cairnmodels.PRSummary{
		RepoID:      repoID,
		PRNumber:    prNumber,
		ContentHash: hash,
		SummaryMD:   resp.Content,
		ModelID:     resp.ModelID,
		TokenCount:  resp.TokenCount,
	}
	if _, err := s.engine.Insert(row); err != nil {
		return nil, fmt.Errorf("summarizer: insert: %w", err)
	}
	return row, nil
}

// RegenerateSummary forces a new generation regardless of cache. The new
// row is inserted as a fresh PRSummary; old rows are kept for audit.
func (s *Service) RegenerateSummary(ctx context.Context, repoID, prNumber, ownerID int64, prCtx PRContext, scope cairnmodels.DataScope) (*cairnmodels.PRSummary, error) {
	if s.resolver == nil {
		return nil, ErrNotConfigured
	}
	client, cfg, err := s.resolver(ownerID)
	if err != nil {
		return nil, fmt.Errorf("summarizer: resolve config: %w", err)
	}
	if cfg == nil || !cfg.Enabled || client == nil {
		return nil, ErrNotConfigured
	}
	scoped := SelectFields(scope, prCtx)
	hash := HashPRContext(scoped)

	resp, err := client.Complete(ctx, SystemPrompt, BuildUserPrompt(scoped))
	if err != nil {
		return nil, fmt.Errorf("summarizer: ai call: %w", err)
	}
	row := &cairnmodels.PRSummary{
		RepoID:      repoID,
		PRNumber:    prNumber,
		ContentHash: hash,
		SummaryMD:   resp.Content,
		ModelID:     resp.ModelID,
		TokenCount:  resp.TokenCount,
	}
	if _, err := s.engine.Insert(row); err != nil {
		return nil, fmt.Errorf("summarizer: insert: %w", err)
	}
	return row, nil
}

// GetCachedSummary returns the most-recent cached summary for a PR (any
// content hash). Useful for read-only views that show whatever's there.
func (s *Service) GetCachedSummary(ctx context.Context, repoID, prNumber int64) (*cairnmodels.PRSummary, error) {
	row := &cairnmodels.PRSummary{}
	has, err := s.engine.Where("repo_id = ? AND pr_number = ?", repoID, prNumber).Desc("generated_unix").Get(row)
	if err != nil {
		return nil, fmt.Errorf("summarizer: cache lookup: %w", err)
	}
	if !has {
		return nil, ErrNoSummary
	}
	return row, nil
}
```

- [ ] **Step 4: Implement global access**

```go
// services/cairn/summarizer/global.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import "sync/atomic"

var globalService atomic.Pointer[Service]

func SetGlobal(s *Service) { globalService.Store(s) }

func Global() *Service { return globalService.Load() }
```

- [ ] **Step 5: Run all service tests, verify pass**

```bash
go test ./services/cairn/summarizer/... -run TestEnsure
go test ./services/cairn/summarizer/... -run TestGet
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add services/cairn/summarizer/service.go services/cairn/summarizer/global.go services/cairn/summarizer/service_test.go
git commit -m "feat(cairn): simplifier service orchestration + cache

Service.EnsureSummary checks cache by repo+pr+content_hash, returns
cached if present, otherwise calls the AI client and stores the
result. RegenerateSummary forces a new call. GetCachedSummary is
read-only.

Configuration is injected via ConfigResolver — production wires to
SummarizerConfig + bridle Summarizer; tests inject mocks. Errors are sentinel
(ErrNoSummary, ErrNotConfigured) for callers using errors.Is.

Global access via atomic.Pointer for lock-free reads from hooks and
event listeners (matches services/cairn/identity pattern).

Refs: docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md §3.7"
```

---

## Task 5: Production ConfigResolver + initialization

**Files:**
- Create: `services/cairn/summarizer/init.go`
- Modify: `routers/init.go` (call init at startup)
- Modify: `modules/setting/cairn.go` (add `SummarizerEnabled` flag)
- Test: `services/cairn/summarizer/init_test.go`

The production `ConfigResolver` reads `SummarizerConfig` from the engine, decrypts credentials, calls `BuildBridleProviderFromConfig` to construct the right bridle provider, and wraps it in a `Summarizer`. Set up at startup; gated by `Cairn.SummarizerEnabled` and `Cairn.Enabled`.

- [ ] **Step 1: Add the `SummarizerEnabled` setting**

Read `modules/setting/cairn.go` to find where `Cairn.Enabled` etc. live. Add:

```go
// modules/setting/cairn.go — within the Cairn struct
SummarizerEnabled bool
```

In `loadCairnFrom`:

```go
sec := rootCfg.Section("cairn")
// ... existing entries ...
Cairn.SummarizerEnabled = sec.Key("SUMMARIZER_ENABLED").MustBool(true)
```

- [ ] **Step 2: Implement production resolver**

```go
// services/cairn/summarizer/init.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"fmt"
	"os"

	"xorm.io/xorm"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// Init constructs the production Service and registers it as global.
// Called from routers/init.go at startup. hmacKeyPath is the path to the
// instance HMAC key (same path used by the rest of Cairn — re-read here so
// summarizer doesn't depend on the identity service's accessor).
func Init(engine *xorm.Engine, hmacKeyPath string) error {
	hmacKey, err := os.ReadFile(hmacKeyPath)
	if err != nil {
		return fmt.Errorf("summarizer: read hmac key: %w", err)
	}

	resolver := func(ownerID int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		cfg := &cairnmodels.SummarizerConfig{}
		has, err := engine.Where("owner_id = ?", ownerID).Get(cfg)
		if err != nil {
			return nil, nil, fmt.Errorf("summarizer: load config: %w", err)
		}
		if !has || !cfg.Enabled {
			return nil, cfg, nil
		}
		apiKey, err := DecryptCredential(hmacKey, cfg.CredentialsCipher)
		if err != nil {
			return nil, cfg, fmt.Errorf("summarizer: decrypt: %w", err)
		}
		provider, err := BuildBridleProviderFromConfig(cfg, apiKey)
		if err != nil {
			return nil, cfg, err
		}
		client, err := NewSummarizerWithProvider(provider, cfg.ModelID)
		if err != nil {
			return nil, cfg, err
		}
		return client, cfg, nil
	}

	SetGlobal(NewService(engine, resolver))
	return nil
}
```

- [ ] **Step 3: Wire into Forgejo's startup**

Read `routers/init.go` to find the Cairn init block. After the existing identity service init, add:

```go
if setting.Cairn.Enabled && setting.Cairn.SummarizerEnabled {
    if err := summarizer.Init(db.GetEngine(ctx).(*xorm.Engine), setting.Cairn.HMACKeyPath); err != nil {
        log.Error("summarizer init: %v", err)
    }
}
```

- [ ] **Step 4: Test resolver round-trip**

```go
// services/cairn/summarizer/init_test.go
package summarizer

import (
	"os"
	"path/filepath"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

func TestInit_ResolverDecryptsCredential(t *testing.T) {
	eng := cairntest.NewEngine(t)
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "hmac.key")
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!")
	if err := os.WriteFile(keyPath, hmacKey, 0o600); err != nil {
		t.Fatal(err)
	}

	cipher, err := EncryptCredential(hmacKey, []byte("sk-real-key"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &cairnmodels.SummarizerConfig{
		OwnerID:           42,
		Enabled:           true,
		EndpointURL:       "https://api.example.com/v1/chat/completions",
		ModelID:           "gpt-test",
		CredentialsCipher: cipher,
		LevelsEnabled:     cairnmodels.LevelPR,
	}
	if _, err := eng.Insert(cfg); err != nil {
		t.Fatal(err)
	}
	if err := Init(eng, keyPath); err != nil {
		t.Fatalf("Init: %v", err)
	}
	svc := Global()
	if svc == nil {
		t.Fatal("Global() returned nil after Init")
	}
	client, loaded, err := svc.resolver(42)
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	if !loaded.Enabled {
		t.Error("loaded config should be enabled")
	}
	if client == nil {
		t.Error("resolver returned nil client for enabled config")
	}
}
```

Run: `go test ./services/cairn/summarizer/... -run TestInit` — expect PASS.

- [ ] **Step 5: Commit**

```bash
git add services/cairn/summarizer/init.go services/cairn/summarizer/init_test.go modules/setting/cairn.go routers/init.go
git commit -m "feat(cairn): summarizer production init + setting

Init reads the instance HMAC key, sets up the production
ConfigResolver (reads SummarizerConfig from engine, decrypts
credentials with AES-GCM, constructs a bridle Summarizer via
BuildBridleProviderFromConfig), and registers
the Service via atomic.Pointer global.

setting.Cairn.SummarizerEnabled gates the whole feature; defaults
to true (the per-org enable inside SummarizerConfig is the real
opt-in, this just lets self-hosters disable the entire path).

routers/init.go wires the init alongside the existing identity
service init.

Refs: docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md §3.3"
```

---

## Task 6: API endpoints (org config + repo consent + summary access)

**Files:**
- Create: `routers/api/cairn/v1/summarizer.go`
- Modify: `routers/init.go` (route registration in `cairnRoutes`)
- Test: `routers/api/cairn/v1/summarizer_test.go`

API surface (per spec §3.8):
- `GET /api/cairn/v1/orgs/{owner}/summarizer` — admin: read config (credentials redacted)
- `PUT /api/cairn/v1/orgs/{owner}/summarizer` — admin: update config (encrypts incoming creds)
- `GET /api/cairn/v1/repos/{owner}/{repo}/summarizer` — repo admin (private only)
- `PUT /api/cairn/v1/repos/{owner}/{repo}/summarizer` — repo admin (private only)
- `GET /api/cairn/v1/repos/{owner}/{repo}/pulls/{n}/summary` — fetch cached summary
- `POST /api/cairn/v1/repos/{owner}/{repo}/pulls/{n}/summary/regenerate` — manual trigger

- [ ] **Step 1: Test the GET-config endpoint redacts credentials**

```go
// routers/api/cairn/v1/summarizer_test.go (new file)
package v1_test

// Test outline (skeleton — implementer fills in Forgejo's standard
// httptest setup matching the existing patterns in
// routers/api/cairn/v1/agents_test.go):
//
// - Insert a SummarizerConfig with credentials_cipher = some bytes.
// - GET /api/cairn/v1/orgs/{owner}/summarizer.
// - Decode JSON response.
// - Assert response.endpoint_url == cfg.endpoint_url.
// - Assert response.credentials_set == true (boolean flag, not the bytes).
// - Assert no field in the response equals or contains the cipher bytes.
//
// Reuse the test scaffolding from agents_test.go; the auth middleware
// pattern is identical.
```

(Implementer reads `routers/api/cairn/v1/agents_test.go` for the pattern; same shape.)

- [ ] **Step 2: Implement handlers**

```go
// routers/api/cairn/v1/summarizer.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package v1

import (
	"net/http"
	"strconv"

	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/services/cairn/summarizer"
	"github.com/CarriedWorldUniverse/cairn/services/context"
	// xorm engine accessor
	"github.com/CarriedWorldUniverse/cairn/models/db"
)

const maxConfigBody = 4096

type configRequest struct {
	Enabled       bool   `json:"enabled"`
	Provider      string `json:"provider"`     // bridle ProviderID: claudecode | openai-api | claude-api | bedrock | ollama-local
	EndpointURL   string `json:"endpoint_url"` // optional; required for openai-api self-hosted, ollama
	ModelID       string `json:"model_id"`
	APIKey        string `json:"api_key"`      // write-only; never returned
	LevelsEnabled int    `json:"levels_enabled"`
}

type configResponse struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider"`
	EndpointURL    string `json:"endpoint_url"`
	ModelID        string `json:"model_id"`
	CredentialsSet bool   `json:"credentials_set"`
	LevelsEnabled  int    `json:"levels_enabled"`
}

// GetSummarizerConfig — admin: read per-org config.
// Auth: caller must be admin of the owner (org or self for user-owned).
func GetSummarizerConfig(ctx *context.APIContext) {
	owner := ctx.ContextUser // resolved by Forgejo middleware
	if owner == nil {
		ctx.Error(http.StatusNotFound, "owner not found", nil)
		return
	}
	if !ctx.Doer.IsAdmin && ctx.Doer.ID != owner.ID {
		ctx.Error(http.StatusForbidden, "admin required", nil)
		return
	}
	cfg := &cairnmodels.SummarizerConfig{}
	has, err := db.GetEngine(ctx).Where("owner_id = ?", owner.ID).Get(cfg)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "load config", err)
		return
	}
	if !has {
		ctx.JSON(http.StatusOK, configResponse{}) // empty / unconfigured
		return
	}
	ctx.JSON(http.StatusOK, configResponse{
		Enabled:        cfg.Enabled,
		Provider:       cfg.Provider,
		EndpointURL:    cfg.EndpointURL,
		ModelID:        cfg.ModelID,
		CredentialsSet: len(cfg.CredentialsCipher) > 0,
		LevelsEnabled:  int(cfg.LevelsEnabled),
	})
}

// PutSummarizerConfig — admin: upsert per-org config. Encrypts incoming API key.
func PutSummarizerConfig(ctx *context.APIContext) {
	owner := ctx.ContextUser
	if owner == nil {
		ctx.Error(http.StatusNotFound, "owner not found", nil)
		return
	}
	if !ctx.Doer.IsAdmin && ctx.Doer.ID != owner.ID {
		ctx.Error(http.StatusForbidden, "admin required", nil)
		return
	}
	var req configRequest
	if err := readJSON(ctx, &req, maxConfigBody); err != nil {
		ctx.Error(http.StatusBadRequest, "decode body", err)
		return
	}

	hmacKey, err := readHMACKey()
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "read hmac", err)
		return
	}

	existing := &cairnmodels.SummarizerConfig{}
	has, err := db.GetEngine(ctx).Where("owner_id = ?", owner.ID).Get(existing)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "load config", err)
		return
	}

	cfg := &cairnmodels.SummarizerConfig{
		OwnerID:       owner.ID,
		Enabled:       req.Enabled,
		Provider:      req.Provider,
		EndpointURL:   req.EndpointURL,
		ModelID:       req.ModelID,
		LevelsEnabled: cairnmodels.LevelFlag(req.LevelsEnabled),
	}
	if req.APIKey != "" {
		cipher, err := summarizer.EncryptCredential(hmacKey, []byte(req.APIKey))
		if err != nil {
			ctx.Error(http.StatusInternalServerError, "encrypt", err)
			return
		}
		cfg.CredentialsCipher = cipher
	} else if has {
		cfg.CredentialsCipher = existing.CredentialsCipher // preserve
	}

	if has {
		if _, err := db.GetEngine(ctx).Where("owner_id = ?", owner.ID).Update(cfg); err != nil {
			ctx.Error(http.StatusInternalServerError, "update", err)
			return
		}
	} else {
		if _, err := db.GetEngine(ctx).Insert(cfg); err != nil {
			ctx.Error(http.StatusInternalServerError, "insert", err)
			return
		}
	}

	ctx.JSON(http.StatusOK, configResponse{
		Enabled:        cfg.Enabled,
		Provider:       cfg.Provider,
		EndpointURL:    cfg.EndpointURL,
		ModelID:        cfg.ModelID,
		CredentialsSet: len(cfg.CredentialsCipher) > 0,
		LevelsEnabled:  int(cfg.LevelsEnabled),
	})
}

// GetRepoConsent — repo admin (private repos only): read consent.
func GetRepoConsent(ctx *context.APIContext) {
	repo := ctx.Repo.Repository
	if repo == nil {
		ctx.Error(http.StatusNotFound, "repo not found", nil)
		return
	}
	if !repo.IsPrivate {
		ctx.Error(http.StatusBadRequest, "consent only applies to private repos", nil)
		return
	}
	if !ctx.Repo.Permission.IsAdmin() {
		ctx.Error(http.StatusForbidden, "repo admin required", nil)
		return
	}
	consent := &cairnmodels.SummarizerRepoConsent{}
	has, err := db.GetEngine(ctx).Where("repo_id = ?", repo.ID).Get(consent)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "load consent", err)
		return
	}
	if !has {
		ctx.JSON(http.StatusOK, map[string]any{"enabled": false, "data_scope": cairnmodels.DataScopeMetadata})
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"enabled": consent.Enabled, "data_scope": consent.DataScope})
}

type consentRequest struct {
	Enabled   bool                  `json:"enabled"`
	DataScope cairnmodels.DataScope `json:"data_scope"`
}

// PutRepoConsent — repo admin (private repos only): upsert consent.
func PutRepoConsent(ctx *context.APIContext) {
	repo := ctx.Repo.Repository
	if repo == nil {
		ctx.Error(http.StatusNotFound, "repo not found", nil)
		return
	}
	if !repo.IsPrivate {
		ctx.Error(http.StatusBadRequest, "consent only applies to private repos", nil)
		return
	}
	if !ctx.Repo.Permission.IsAdmin() {
		ctx.Error(http.StatusForbidden, "repo admin required", nil)
		return
	}
	var req consentRequest
	if err := readJSON(ctx, &req, 1024); err != nil {
		ctx.Error(http.StatusBadRequest, "decode body", err)
		return
	}
	if req.Enabled && !req.DataScope.IsValid() {
		ctx.Error(http.StatusBadRequest, "invalid data_scope", nil)
		return
	}
	consent := &cairnmodels.SummarizerRepoConsent{
		RepoID:    repo.ID,
		Enabled:   req.Enabled,
		DataScope: req.DataScope,
	}
	existing := &cairnmodels.SummarizerRepoConsent{}
	has, err := db.GetEngine(ctx).Where("repo_id = ?", repo.ID).Get(existing)
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "load consent", err)
		return
	}
	if has {
		_, err = db.GetEngine(ctx).Where("repo_id = ?", repo.ID).Update(consent)
	} else {
		_, err = db.GetEngine(ctx).Insert(consent)
	}
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "save consent", err)
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"enabled": consent.Enabled, "data_scope": consent.DataScope})
}

// GetSummary — anyone with read on the repo: fetch the cached summary.
// Returns 404 if no summary has been generated yet.
func GetSummary(ctx *context.APIContext) {
	repo := ctx.Repo.Repository
	if repo == nil {
		ctx.Error(http.StatusNotFound, "repo not found", nil)
		return
	}
	prNumberStr := ctx.Params(":index")
	prNumber, err := strconv.ParseInt(prNumberStr, 10, 64)
	if err != nil {
		ctx.Error(http.StatusBadRequest, "invalid pr number", err)
		return
	}
	svc := summarizer.Global()
	if svc == nil {
		ctx.Error(http.StatusServiceUnavailable, "simplifier disabled", nil)
		return
	}
	row, err := svc.GetCachedSummary(ctx, repo.ID, prNumber)
	if errors.Is(err, summarizer.ErrNoSummary) {
		ctx.Error(http.StatusNotFound, "no summary", nil)
		return
	}
	if err != nil {
		ctx.Error(http.StatusInternalServerError, "lookup", err)
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{
		"summary_md":   row.SummaryMD,
		"model_id":     row.ModelID,
		"generated_at": row.GeneratedUnix,
	})
}

// PostRegenerate — repo admin: force regeneration. Asynchronous; returns 202.
// (MVP: synchronous-blocking is also acceptable; queue is a polish item.)
func PostRegenerate(ctx *context.APIContext) {
	repo := ctx.Repo.Repository
	if repo == nil {
		ctx.Error(http.StatusNotFound, "repo not found", nil)
		return
	}
	if !ctx.Repo.Permission.CanWrite(/* PR permission */) { // implementer: use existing permission accessor pattern from agents.go
		ctx.Error(http.StatusForbidden, "write permission required", nil)
		return
	}
	prNumberStr := ctx.Params(":index")
	prNumber, err := strconv.ParseInt(prNumberStr, 10, 64)
	if err != nil {
		ctx.Error(http.StatusBadRequest, "invalid pr number", err)
		return
	}

	// Synchronous regenerate for MVP: build PRContext from Forgejo data,
	// call svc.RegenerateSummary. (Building PRContext from Forgejo's
	// loaded PR is its own helper — see Task 8.)
	//
	// Wire the helper here once Task 8 ships; for MVP this endpoint
	// returns 501 Not Implemented and the PR-page button just shows
	// "regenerate (coming soon)" until the wiring lands. That's
	// acceptable because content-change auto-regen (via the event
	// listener in Task 7) covers the primary path.
	ctx.Error(http.StatusNotImplemented, "manual regeneration arrives in Task 8", nil)
}

// readJSON / readHMACKey are existing utility helpers in this package
// (matching the pattern used by routers/api/cairn/v1/agents.go).
// readHMACKey reads from setting.Cairn.HMACKeyPath.
```

(Implementer reads `routers/api/cairn/v1/agents.go` for the auth middleware patterns, `readJSON`-style helpers, and the permission accessor names; this stub leaves implementation-detail-by-pattern as comments to copy from agents.go rather than re-pasting boilerplate.)

- [ ] **Step 3: Register routes in `routers/init.go::cairnRoutes`**

Read the existing route registration block (where Plan 2a's agent endpoints are mounted). Append:

```go
m.Group("/orgs/{owner}", func() {
    m.Get("/summarizer", v1.GetSummarizerConfig)
    m.Put("/summarizer", v1.PutSummarizerConfig)
})
m.Group("/repos/{owner}/{repo}", func() {
    m.Get("/summarizer", v1.GetRepoConsent)
    m.Put("/summarizer", v1.PutRepoConsent)
    m.Group("/pulls/{index}/summary", func() {
        m.Get("", v1.GetSummary)
        m.Post("/regenerate", v1.PostRegenerate)
    })
})
```

- [ ] **Step 4: Run handler tests**

```bash
go test ./routers/api/cairn/... -run Summarizer
```

Expected: PASS for the redaction-of-credentials test (and any others the implementer adds).

- [ ] **Step 5: Commit**

```bash
git add routers/api/cairn/v1/summarizer.go routers/api/cairn/v1/summarizer_test.go routers/init.go
git commit -m "feat(cairn): simplifier API endpoints

GET/PUT /api/cairn/v1/orgs/{owner}/summarizer — admin org config.
GET/PUT /api/cairn/v1/repos/{owner}/{repo}/summarizer — repo admin
private-repo consent.
GET /api/cairn/v1/repos/.../pulls/{n}/summary — fetch cached summary.
POST /api/cairn/v1/repos/.../pulls/{n}/summary/regenerate — manual
trigger (returns 501 until Task 8 wires the PRContext builder).

Credentials are encrypted on PUT, never returned on GET (only a
'credentials_set' bool indicates presence).

Refs: docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md §3.8"
```

---

## Task 7: Forgejo PR-event listener + auto-summarize

**Files:**
- Create: `services/cairn/summarizer/events.go`
- Modify: `services/cairn/summarizer/init.go` (register listener)
- Test: `services/cairn/summarizer/events_test.go`

Listen for PR opened / synchronized events. When the org has a config and (for private repos) the repo has consent, build a PRContext and call `EnsureSummary`. Asynchronous: dispatched to a worker goroutine so the event handler returns fast.

The listener is the engine for "auto-summarize on PR change."

- [ ] **Step 1: Implement event listener**

```go
// services/cairn/summarizer/events.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package summarizer

import (
	"context"
	"sync"
	"time"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	issues_model "github.com/CarriedWorldUniverse/cairn/models/issues"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
)

// Job is one summarization request.
type Job struct {
	RepoID   int64
	PRNumber int64
	OwnerID  int64
	Context  PRContext
	Scope    cairnmodels.DataScope
}

// queue is a debounced per-(repo, pr) job queue. Multiple rapid pushes to
// the same PR coalesce into one summarization run.
type queue struct {
	mu      sync.Mutex
	pending map[string]*Job
	debounce time.Duration
	svc     *Service
}

func newQueue(svc *Service, debounce time.Duration) *queue {
	return &queue{pending: make(map[string]*Job), debounce: debounce, svc: svc}
}

func (q *queue) enqueue(j Job) {
	key := jobKey(j.RepoID, j.PRNumber)
	q.mu.Lock()
	q.pending[key] = &j
	q.mu.Unlock()
	go q.runAfter(key, q.debounce)
}

func (q *queue) runAfter(key string, d time.Duration) {
	time.Sleep(d)
	q.mu.Lock()
	job := q.pending[key]
	delete(q.pending, key)
	q.mu.Unlock()
	if job == nil {
		return // superseded by a later job
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := q.svc.EnsureSummary(ctx, job.RepoID, job.PRNumber, job.OwnerID, job.Context, job.Scope); err != nil {
		log.Error("summarizer auto-run for repo=%d pr=%d: %v", job.RepoID, job.PRNumber, err)
	}
}

func jobKey(repoID, prNumber int64) string {
	return string(rune(repoID)) + ":" + string(rune(prNumber))
}

// BuildPRContextFromForgejo loads PR data into the simplifier's input shape.
// Reads PR title/body, commit messages, file paths, and (for full scope)
// the diff. Heavy work; do not call from the request goroutine.
func BuildPRContextFromForgejo(ctx context.Context, repo *repo_model.Repository, pr *issues_model.PullRequest, issue *issues_model.Issue, scope cairnmodels.DataScope) (PRContext, error) {
	out := PRContext{Title: issue.Title, Body: issue.Content, BaseBranch: pr.BaseBranch, HeadBranch: pr.HeadBranch}
	// Implementer: load commits via repo.GitRepo.GetMergeBase + RevList,
	// load file paths via DiffNameOnly, load diff via git.GetRawDiff with
	// a 512 KB cap (re-use the limitWriter pattern from Plan 4).
	//
	// Match scope: full -> include diff; commit-messages -> just messages;
	// metadata -> just paths. SelectFields enforces this on the way out.
	return out, nil
}
```

(Step abbreviated for plan readability — implementer fills the BuildPRContextFromForgejo body using the same git APIs that Plan 4's `routers/web/cairn/forgejo/bind.go::CommitDataFromForgejo` uses. Apply the 512 KB diff cap pattern.)

- [ ] **Step 2: Register listener in `Init`**

In `services/cairn/summarizer/init.go::Init`, after `SetGlobal(...)`:

```go
q := newQueue(Global(), 5*time.Second)
notification.RegisterNotifier(&prNotifier{queue: q})
```

Where `prNotifier` is a minimal Forgejo notifier (matching the pattern in `services/notify/notifier`):

```go
// In events.go:
type prNotifier struct {
    notification.NullNotifier
    queue *queue
}

func (n *prNotifier) NewPullRequest(ctx context.Context, pr *issues_model.PullRequest, _ []*user_model.User) {
    n.handle(ctx, pr)
}

func (n *prNotifier) PullRequestSynchronized(ctx context.Context, _ *user_model.User, pr *issues_model.PullRequest) {
    n.handle(ctx, pr)
}

func (n *prNotifier) handle(ctx context.Context, pr *issues_model.PullRequest) {
    // Load issue, owner, scope; respect public/private + consent gates;
    // build PRContext; queue.enqueue(...). Errors logged, never propagated
    // (notifications run async; failure here must not block the PR action).
}
```

(Implementer reads `services/notify/notifier/null.go` for the embed pattern.)

- [ ] **Step 3: Test debounce**

```go
// services/cairn/summarizer/events_test.go
package summarizer

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

func TestQueueDebounces(t *testing.T) {
	eng := cairntest.NewEngine(t)
	var calls int32
	cli := &mockClient{response: "x"}
	svc := NewService(eng, func(_ int64) (AIClient, *cairnmodels.SummarizerConfig, error) {
		atomic.AddInt32(&calls, 1)
		return cli, &cairnmodels.SummarizerConfig{Enabled: true, EndpointURL: "x"}, nil
	})
	q := newQueue(svc, 100*time.Millisecond)

	for i := 0; i < 5; i++ {
		q.enqueue(Job{RepoID: 1, PRNumber: 1, OwnerID: 1, Context: PRContext{Title: "v"}, Scope: cairnmodels.DataScopeMetadata})
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // allow last debounce to fire
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("resolver calls = %d, want 1 (rapid enqueues should debounce)", got)
	}
	_ = context.Background()
}
```

Run: `go test ./services/cairn/summarizer/... -run TestQueueDebounces` — expect PASS.

- [ ] **Step 4: Commit**

```bash
git add services/cairn/summarizer/events.go services/cairn/summarizer/events_test.go services/cairn/summarizer/init.go
git commit -m "feat(cairn): summarizer auto-runs on PR open + sync

prNotifier hooks into Forgejo's notification system, listening for
NewPullRequest and PullRequestSynchronized. The queue debounces
rapid synchronizations (5s) so multiple pushes coalesce into one
summarization run.

BuildPRContextFromForgejo loads PR title/body/commits/files/diff
with the 512 KB cap pattern from Plan 4. SelectFields applies the
data-scope policy on the way out.

Errors in the auto-run path are logged, never propagated — the
notifier runs async and must not affect the PR action.

Refs: docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md §3.7"
```

---

## Task 8: PR-page summary block + manual regenerate

**Files:**
- Create: `routers/web/cairn/summarizer_pr_block.go`
- Create: `routers/web/cairn/templates/summarizer/pr-summary-block.tmpl`
- Modify: `routers/web/repo/pull.go` (3-line short-circuit injecting summary block)
- Modify: `templates/repo/issue/view_content/pull_header.tmpl` (single-line template include)
- Modify: `routers/api/cairn/v1/summarizer.go::PostRegenerate` (wire BuildPRContextFromForgejo from Task 7)
- Test: `routers/web/cairn/summarizer_pr_block_test.go`

Render an HTML summary block at the top of the PR view. Includes:
- The summary markdown (rendered to HTML)
- Generated-at timestamp + model ID (small, muted)
- "Regenerate" button (calls `POST /api/cairn/v1/repos/.../summary/regenerate`)

If no summary exists yet (e.g., first generation in flight): show "Summary generating…" placeholder.
If the org has no service configured: render nothing (don't surface platform internals to non-admins).

- [ ] **Step 1: Implement the helper that fetches + renders for a PR view**

```go
// routers/web/cairn/summarizer_pr_block.go
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

import (
	"bytes"
	_ "embed"
	"text/template"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/services/cairn/summarizer"
)

//go:embed templates/summarizer/pr-summary-block.tmpl
var prSummaryBlockTpl string

var prSummaryBlock = template.Must(template.New("pr-summary-block").Parse(prSummaryBlockTpl))

type PRSummaryBlockData struct {
	Available    bool   // whether to render at all
	State        string // "ready" | "generating" | "unavailable"
	SummaryHTML  string // rendered from markdown
	ModelID      string
	GeneratedAt  int64
	RegenerateURL string // empty if user can't regen
}

// RenderPRSummaryBlock returns the HTML for the PR-summary block, or "" if
// nothing should render (org has no service configured).
func RenderPRSummaryBlock(repoID, prNumber int64, canRegenerate bool, regenURL string) string {
	svc := summarizer.Global()
	if svc == nil {
		return ""
	}
	row, err := svc.GetCachedSummary(nil, repoID, prNumber)
	data := PRSummaryBlockData{Available: true, RegenerateURL: regenURL}
	if !canRegenerate {
		data.RegenerateURL = ""
	}
	switch {
	case errors.Is(err, summarizer.ErrNoSummary):
		data.State = "generating"
	case err != nil:
		data.State = "unavailable"
	default:
		data.State = "ready"
		data.SummaryHTML = renderMarkdownToHTML(row.SummaryMD) // reuse Plan 4's markdown rendering
		data.ModelID = row.ModelID
		data.GeneratedAt = row.GeneratedUnix
	}
	var buf bytes.Buffer
	_ = prSummaryBlock.Execute(&buf, data)
	return buf.String()
}
```

```html
<!-- routers/web/cairn/templates/summarizer/pr-summary-block.tmpl -->
{{- if .Available -}}
<div class="cairn-pr-summary cairn-summary-{{.State}}">
  <div class="cairn-summary-header">
    <span class="cairn-summary-by">by cairn</span>
    {{- if eq .State "ready" -}}
      <span class="cairn-summary-meta">{{.ModelID}} · generated {{.GeneratedAt}}</span>
      {{- if .RegenerateURL -}}
        <button class="cairn-summary-regen" data-url="{{.RegenerateURL}}">regenerate</button>
      {{- end -}}
    {{- end -}}
  </div>
  {{- if eq .State "ready" -}}
    <div class="cairn-summary-body">{{.SummaryHTML | safeHTML}}</div>
  {{- else if eq .State "generating" -}}
    <div class="cairn-summary-body cairn-summary-pending">summary generating…</div>
  {{- else -}}
    <div class="cairn-summary-body cairn-summary-error">summary unavailable</div>
  {{- end -}}
</div>
{{- end -}}
```

- [ ] **Step 2: Register a template helper in Forgejo's template func map**

Modify `modules/templates/helper.go` to register `cairnPRSummaryBlock`:

```go
"cairnPRSummaryBlock": func(repoID, prNumber int64, canRegen bool, regenURL string) template.HTML {
    return template.HTML(cairnweb.RenderPRSummaryBlock(repoID, prNumber, canRegen, regenURL))
},
```

(Read the existing helper.go for the pattern Plan 4 used to register `cairnAgentAuthorBadge`.)

- [ ] **Step 3: Modify the PR header template**

Add to `templates/repo/issue/view_content/pull_header.tmpl` (or whatever the actual PR header template is — implementer locates it):

```html
<!-- Cairn: AI-summary block at top of PR -->
{{cairnPRSummaryBlock .Repository.ID .Issue.Index (.IsRepoAdmin) (printf "/api/cairn/v1/repos/%s/%s/pulls/%d/summary/regenerate" .Repository.OwnerName .Repository.Name .Issue.Index)}}
```

- [ ] **Step 4: Wire `PostRegenerate` to the actual builder**

In `routers/api/cairn/v1/summarizer.go::PostRegenerate`, replace the 501 stub:

```go
issue, err := issues_model.GetIssueByIndex(ctx, repo.ID, prNumber)
if err != nil {
    ctx.Error(http.StatusNotFound, "issue", err)
    return
}
if !issue.IsPull {
    ctx.Error(http.StatusNotFound, "not a PR", nil)
    return
}
if err := issue.LoadPullRequest(ctx); err != nil {
    ctx.Error(http.StatusInternalServerError, "load pr", err)
    return
}
scope := cairnmodels.DataScopeFull
if repo.IsPrivate {
    consent := &cairnmodels.SummarizerRepoConsent{}
    if has, _ := db.GetEngine(ctx).Where("repo_id = ?", repo.ID).Get(consent); !has || !consent.Enabled {
        ctx.Error(http.StatusBadRequest, "private repo summarization not enabled", nil)
        return
    }
    scope = consent.DataScope
}
prCtx, err := summarizer.BuildPRContextFromForgejo(ctx, repo, issue.PullRequest, issue, scope)
if err != nil {
    ctx.Error(http.StatusInternalServerError, "build context", err)
    return
}
svc := summarizer.Global()
if svc == nil {
    ctx.Error(http.StatusServiceUnavailable, "simplifier disabled", nil)
    return
}
row, err := svc.RegenerateSummary(ctx, repo.ID, prNumber, repo.OwnerID, prCtx, scope)
if err != nil {
    ctx.Error(http.StatusInternalServerError, "regenerate", err)
    return
}
ctx.JSON(http.StatusOK, map[string]any{"summary_md": row.SummaryMD, "generated_at": row.GeneratedUnix})
```

- [ ] **Step 5: Smoke test the full flow**

Implementer adds a smoke test (or live verification) that:
- Creates a PR via Forgejo's test scaffolding
- Has a SummarizerConfig with a `httptest.NewServer` AI service mock
- Triggers the PR-open event
- After debounce, asserts `PRSummary` row exists for the PR
- Asserts the rendered PR HTML contains the summary text

Run: `go test ./routers/web/cairn/... -run TestPRSummary`.

- [ ] **Step 6: Commit**

```bash
git add routers/web/cairn/summarizer_pr_block.go routers/web/cairn/templates/summarizer/pr-summary-block.tmpl routers/web/cairn/summarizer_pr_block_test.go modules/templates/helper.go templates/repo/issue/view_content/pull_header.tmpl routers/web/repo/pull.go routers/api/cairn/v1/summarizer.go
git commit -m "feat(cairn): PR-page summary block + manual regenerate

PR HTML view renders a Cairn-summary block at the top via the
cairnPRSummaryBlock template helper. Three states: ready (rendered
markdown), generating (placeholder while first run completes),
unavailable (org has no service configured). Regenerate button
appears for repo admins; calls the API endpoint wired in this commit.

Forgejo upstream patches: helper.go gets cairnPRSummaryBlock alongside
cairnAgentAuthorBadge; pull_header.tmpl gets a single-line invocation
of the helper. Total: ~5 lines.

Refs: docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md §3.8"
```

---

## Task 9: `?format=md` integration + manifest update

**Files:**
- Modify: `routers/web/cairn/markdown.go` (`RenderPullRequest` inlines summary)
- Modify: `routers/web/cairn/wellknown.go` (`features.simplifier_enabled`)
- Test: `routers/web/cairn/markdown_test.go` (existing test extended)

- [ ] **Step 1: Inline summary into markdown rendering**

In `routers/web/cairn/markdown.go::RenderPullRequest`, at the top of the rendered output (before the existing PR title), prepend the summary if available:

```go
func RenderPullRequest(w io.Writer, pr PullRequestData, repo RepoData) error {
    // Cairn: prepend the simplifier summary if cached.
    if svc := summarizer.Global(); svc != nil {
        if row, err := svc.GetCachedSummary(nil, repo.ID, pr.Number); err == nil {
            fmt.Fprintf(w, "## Summary by cairn\n\n%s\n\n---\n\n", row.SummaryMD)
        }
    }
    // ... existing render ...
}
```

(Implementer reads the existing `RenderPullRequest` to find the right insertion point; `repo.ID` may need to be added to `RepoData` if not already there.)

- [ ] **Step 2: Advertise simplifier in `cairn.json` manifest**

Modify `wellknown.go::BuildManifest` to add to the `features` map:

```go
"simplifier_enabled": setting.Cairn.Enabled && setting.Cairn.SummarizerEnabled,
```

- [ ] **Step 3: Tests**

Extend `markdown_test.go` with a case where a cached summary exists; assert the rendered markdown contains "## Summary by cairn".

Extend `wellknown_test.go` with an assertion that `simplifier_enabled` appears in the features map.

Run: `go test ./routers/web/cairn/... -run 'TestRenderPullRequest|TestBuildManifest'` — expect PASS.

- [ ] **Step 4: Commit**

```bash
git add routers/web/cairn/markdown.go routers/web/cairn/wellknown.go routers/web/cairn/markdown_test.go routers/web/cairn/wellknown_test.go
git commit -m "feat(cairn): inline simplifier summary into ?format=md + manifest

Markdown PR view (?format=md) now renders a 'Summary by cairn' block
at the top when a cached summary exists. AI consumers reading the PR
get the same simplification humans see, surfacing it in the canonical
machine-readable surface.

cairn.json manifest gains features.simplifier_enabled = (Cairn.Enabled
AND Cairn.SummarizerEnabled). Discovery clients can detect simplifier
support without trial calls.

Refs: docs/cairn/specs/2026-05-10-cairn-ai-native-amendment.md §3.8"
```

---

## Task 10: Plan-level final review

**Files:** none (review-only task)

- [ ] **Step 1: Run full test suite for new + modified packages**

```bash
cd ~/Source/cairn
go build ./...
go test -count=1 -race ./models/cairn/... ./services/cairn/summarizer/... ./routers/api/cairn/... ./routers/web/cairn/...
```

All green.

- [ ] **Step 2: Self-review against spec**

Walk the spec amendment §3 (Simplifier) and verify each numbered subsection has a landed implementation:

- §3.1 Purpose — covered by service + rendering
- §3.2 Identity (`cairn` system actor) — covered by template "by cairn" label
- §3.3 Backing (per-org config + bridle provider abstraction) — covered by Tasks 1, 3, 5, 6
- §3.4 Standardized prompt — covered by Task 3
- §3.5 Default PR scope, opt-in commit/file levels — `LevelsEnabled` in config; commit/file orchestration is post-MVP wiring (Service is ready, the actual commit/file generators are not enabled in MVP — note explicitly in the Plan 5 final review)
- §3.6 Public/private — covered by `RepoConsent` + scope filter
- §3.7 Lifecycle (queue, debounce, cache, regenerate, failure mode) — covered
- §3.8 Surfaces — covered (Tasks 6, 8, 9)
- §3.9 Storage — covered (Task 1)
- §3.10 What it does NOT do — verified by absence: no judgment, scoring, or auto-action code anywhere in the simplifier package

- [ ] **Step 3: Document the explicit deferral**

The plan ships **PR-level summarization only**. Commit-level and file-level orchestration are post-MVP — `LevelsEnabled` is wired and stored, but the orchestrator only generates PR-level summaries in MVP. The follow-up plan that adds commit/file-level orchestrators is a separate, smaller effort.

Add a final-review subagent dispatch (per `superpowers:subagent-driven-development`) to scan for unhandled edge cases, naming inconsistencies, and any spec-reviewer concerns.

- [ ] **Step 4: Push the merged work to `cairn` branch**

(Per workspace rule: direct push for each repo, controller handles merge after subagent-driven flow completes.)

```bash
git checkout cairn && git pull
git merge --ff-only <feature-branch-tip>
git push origin cairn
```

Then dual-write: copy the updated runbook entries (none yet for simplifier) and any new spec touchpoints into Drive.

---

## Self-review (writing-plans skill)

**1. Spec coverage:**
- §3.1–3.10 of the amendment all have at least one task implementing them
- §3.5 commit/file level explicitly deferred in MVP and documented in Task 10 Step 3
- §3.6 private-repo consent + data-scope filter implemented end-to-end
- §3.8 all listed endpoints + surfaces implemented

**2. Placeholder scan:**
- Two task steps reference "implementer reads existing pattern from `agents.go` / `bind.go`" — these are not placeholders; they are explicit pointers to canonical sibling code that establishes the pattern. Acceptable per the workspace rule "don't cargo-cult sibling patterns" — implementer audits the pattern before mirroring.
- `BuildPRContextFromForgejo` has a TODO-shaped body in Task 7 with explicit instructions to apply Plan 4's bind.go pattern. This is a deliberate handoff (the body is mechanical Forgejo data extraction, identical in shape to Plan 4 work) rather than a missing requirement.

**3. Type consistency:**
- `cairnmodels.DataScope` used consistently across `summarizer_repo_consent.go`, `prompt.go`, service, API, events
- `PRContext` fields match `BuildUserPrompt`, `SelectFields`, `HashPRContext`
- `LevelFlag` defined in `summarizer_config.go`, used in API and config response with consistent int casting

No additional tasks needed.

---

## Plan complete

Save location: `docs/cairn/plans/2026-05-10-cairn-simplifier.md` (in-repo); mirror to `~/Google Drive/My Drive/nexus/general/cairn/plans/2026-05-10-cairn-simplifier.md`.

**Execution choice (per writing-plans skill):**

1. **Subagent-Driven (recommended)** — fresh subagent per task, two-stage review (spec then code-quality) between tasks, fast iteration; matches Plans 1–4.
2. **Inline Execution** — execute in this session via `superpowers:executing-plans`, batched checkpoints.

Plans 1–4 used Subagent-Driven; recommend the same here for consistency.
