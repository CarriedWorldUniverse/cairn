# Cairn Foundation — Identity Layer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the data and crypto foundation that every other Cairn-specific feature depends on — agent schema, casket-go HKDF derivation, fingerprint algorithm, email parser, signature verifier, instance HMAC key handling, and xorm-backed stores with the connection-per-operation discipline.

**Architecture:** Cairn is an in-tree fork of Forgejo. New code lives under additive packages (`models/cairn/`, `services/cairn/`) so upstream rebases don't collide. Schema migrations register with Forgejo's existing xorm engine, sharing one SQLite database. Cryptography uses casket-go's Ed25519 primitives plus a new `DeriveAgentKey` helper added in this plan. Every database operation acquires a short-lived xorm session, executes, and releases — no long-lived sessions.

**Tech Stack:** Go 1.25+, Forgejo v15.0.1 (the fork base), xorm (Forgejo's ORM), SQLite with WAL mode, casket-go (Ed25519+ECDH primitives), Go's standard `testing` package, `crypto/hkdf`, `crypto/hmac`, `crypto/sha256`, `crypto/ed25519`.

**Spec ref:** [`docs/cairn/specs/2026-05-09-cairn-foundation-design.md`](../specs/2026-05-09-cairn-foundation-design.md), specifically §5 (data model), §6 (identity model), and §14 build steps 2-5.

**Repos involved:**
- `github.com/CarriedWorldUniverse/casket-go` — gets a new `DeriveAgentKey` helper (Task 1 only)
- `github.com/CarriedWorldUniverse/cairn` (this repo, branch `cairn`) — everything else (Tasks 2-8)

**Branch strategy:** Each task lands as one commit on a feature branch off `cairn`, pushed with `git push origin <branch>`. After the plan completes, the feature branch merges to `cairn` via PR (or direct push per team cadence).

**Forgejo conventions to know:**
- Tests live alongside code: `agent.go` → `agent_test.go` in the same directory.
- Forgejo uses xorm: structs are mapped to tables via struct tags. Sessions are obtained from `db.GetEngine(ctx)` or via `*xorm.Session` directly.
- Migration files: each migration has a numeric prefix and registers with `migrations.Migrate()`. Forgejo's existing series is around v300; Cairn starts at v500.
- Test helpers: Forgejo has `models/unittest` for fixture setup. We'll use a lighter pattern — temp SQLite + manual schema creation — for Cairn-specific tests so they're independent of Forgejo's fixture loader.

---

## Task 1: Add `DeriveAgentKey` helper to casket-go

**Repo:** `github.com/CarriedWorldUniverse/casket-go` (separate from Cairn)

**Files:**
- Create: `agent.go`
- Create: `agent_test.go`

**Why:** Cairn's identity layer derives Ed25519 keypairs from `(owner_seed, agent_slug)` deterministically. Per the locked decision, this primitive lives in casket-go alongside the existing channel-identity crypto. Cairn imports `casket.DeriveAgentKey` once it's published.

- [ ] **Step 1: Clone casket-go and create a feature branch**

```bash
cd /tmp
git clone https://github.com/CarriedWorldUniverse/casket-go.git
cd casket-go
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
git checkout -b add-derive-agent-key
```

- [ ] **Step 2: Write the failing test in `agent_test.go`**

Replace any existing content if the file exists; otherwise create.

```go
package casket

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestDeriveAgentKey_Deterministic(t *testing.T) {
	seed := bytes.Repeat([]byte{0xab}, 32)

	priv1, pub1, err := DeriveAgentKey(seed, "plumb")
	if err != nil {
		t.Fatalf("first derivation: %v", err)
	}
	priv2, pub2, err := DeriveAgentKey(seed, "plumb")
	if err != nil {
		t.Fatalf("second derivation: %v", err)
	}

	if !bytes.Equal(priv1, priv2) {
		t.Errorf("private keys differ across calls: %x vs %x", priv1, priv2)
	}
	if !bytes.Equal(pub1, pub2) {
		t.Errorf("public keys differ across calls: %x vs %x", pub1, pub2)
	}
}

func TestDeriveAgentKey_DifferentSlugsIndependent(t *testing.T) {
	seed := bytes.Repeat([]byte{0xab}, 32)

	_, pubPlumb, err := DeriveAgentKey(seed, "plumb")
	if err != nil {
		t.Fatal(err)
	}
	_, pubAnvil, err := DeriveAgentKey(seed, "anvil")
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(pubPlumb, pubAnvil) {
		t.Error("different slugs produced identical public keys")
	}
}

func TestDeriveAgentKey_DifferentSeedsIndependent(t *testing.T) {
	seedA := bytes.Repeat([]byte{0xaa}, 32)
	seedB := bytes.Repeat([]byte{0xbb}, 32)

	_, pubA, err := DeriveAgentKey(seedA, "plumb")
	if err != nil {
		t.Fatal(err)
	}
	_, pubB, err := DeriveAgentKey(seedB, "plumb")
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(pubA, pubB) {
		t.Error("different seeds produced identical public keys for same slug")
	}
}

func TestDeriveAgentKey_KnownVector(t *testing.T) {
	// Known vector to detect accidental algorithm changes.
	// seed = 32 bytes of 0xab, slug = "plumb"
	// Expected pubkey is computed once and frozen here.
	seed := bytes.Repeat([]byte{0xab}, 32)
	expectedPubHex := "FROZEN_AT_FIRST_PASSING_RUN"

	_, pub, err := DeriveAgentKey(seed, "plumb")
	if err != nil {
		t.Fatal(err)
	}
	got := hex.EncodeToString(pub)

	if expectedPubHex == "FROZEN_AT_FIRST_PASSING_RUN" {
		// First-pass: print the value and fail so the developer freezes it.
		t.Logf("seed=%x slug=%s -> pubkey=%s", seed, "plumb", got)
		t.Fatalf("freeze the expected pubkey above into expectedPubHex")
	}
	if got != expectedPubHex {
		t.Errorf("pubkey changed; got %s want %s", got, expectedPubHex)
	}
}

func TestDeriveAgentKey_RejectsEmptySeed(t *testing.T) {
	_, _, err := DeriveAgentKey([]byte{}, "plumb")
	if err == nil {
		t.Error("expected error for empty seed")
	}
}

func TestDeriveAgentKey_RejectsEmptySlug(t *testing.T) {
	seed := bytes.Repeat([]byte{0xab}, 32)
	_, _, err := DeriveAgentKey(seed, "")
	if err == nil {
		t.Error("expected error for empty slug")
	}
}

func TestDeriveAgentKey_PrivateKeyIsValidEd25519(t *testing.T) {
	seed := bytes.Repeat([]byte{0xab}, 32)
	priv, _, err := DeriveAgentKey(seed, "plumb")
	if err != nil {
		t.Fatal(err)
	}
	// Ed25519 private keys are 64 bytes (32-byte seed + 32-byte public key).
	if len(priv) != 64 {
		t.Errorf("private key length: got %d want 64", len(priv))
	}
}
```

- [ ] **Step 3: Run the test and verify it fails**

Run: `go test ./...`
Expected: FAIL with `undefined: DeriveAgentKey`.

- [ ] **Step 4: Write minimal implementation in `agent.go`**

```go
// Package casket — agent identity derivation.
//
// DeriveAgentKey produces a deterministic Ed25519 keypair from an owner's
// identity seed and an agent slug. Same (seed, slug) always produces the
// same keypair, on any machine. Different slugs produce independent keys.
//
// The derivation uses HKDF-SHA256 with info string "cairn-agent-v1:" + slug,
// length 32 bytes (Ed25519's seed size). The HKDF output is fed into
// ed25519.NewKeyFromSeed to produce the keypair.
//
// Reference: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6
// in the Cairn repo.

package casket

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"io"

	"golang.org/x/crypto/hkdf"
)

const agentKeyInfoPrefix = "cairn-agent-v1:"

// DeriveAgentKey derives a deterministic Ed25519 keypair from an owner's
// identity seed and an agent slug. See package doc for details.
//
// Errors: returns an error if seed is empty or slug is empty.
func DeriveAgentKey(seed []byte, slug string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	if len(seed) == 0 {
		return nil, nil, errors.New("casket: seed must not be empty")
	}
	if slug == "" {
		return nil, nil, errors.New("casket: slug must not be empty")
	}

	info := []byte(agentKeyInfoPrefix + slug)
	r := hkdf.New(sha256.New, seed, nil, info)

	derivedSeed := make([]byte, ed25519.SeedSize) // 32 bytes
	if _, err := io.ReadFull(r, derivedSeed); err != nil {
		return nil, nil, err
	}

	priv := ed25519.NewKeyFromSeed(derivedSeed)
	pub := priv.Public().(ed25519.PublicKey)
	return priv, pub, nil
}
```

- [ ] **Step 5: Add `golang.org/x/crypto` if not already a dep**

```bash
go get golang.org/x/crypto/hkdf
go mod tidy
```

- [ ] **Step 6: Run the test, capture the known-vector output, freeze it**

```bash
go test -run TestDeriveAgentKey -v
```

Expected: most tests PASS, but `TestDeriveAgentKey_KnownVector` FAILs with a log line like:

```
seed=abababab...ab slug=plumb -> pubkey=<some 64-char hex string>
```

Copy that hex string. Edit `agent_test.go`, replace `"FROZEN_AT_FIRST_PASSING_RUN"` with the captured value:

```go
expectedPubHex := "<the captured 64-char hex>"
```

- [ ] **Step 7: Run all tests, expect PASS**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 8: Commit**

```bash
git add agent.go agent_test.go go.mod go.sum
git commit -m "$(cat <<'EOF'
feat: add DeriveAgentKey HKDF-Ed25519 helper

Adds DeriveAgentKey(seed, slug) for Cairn agent identity derivation.
Same (seed, slug) -> same keypair on any machine; different slugs
produce independent keys via HKDF-SHA256 with info string
"cairn-agent-v1:" + slug.

Tested:
- determinism across calls
- independence across slugs and seeds
- known-vector regression check
- input validation (empty seed/slug rejected)
- output is a valid 64-byte Ed25519 private key

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6
in github.com/CarriedWorldUniverse/cairn

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 9: Push and merge to main**

```bash
git push origin add-derive-agent-key
# Open PR via gh CLI or push directly per team cadence
```

If team cadence is direct-push to main:

```bash
git checkout main
git pull
git merge add-derive-agent-key
git push origin main
```

---

## Task 2: Create Cairn package skeletons

**Repo:** `github.com/CarriedWorldUniverse/cairn` from here on.

**Files:**
- Create: `models/cairn/doc.go`
- Create: `models/cairn/migrations/doc.go`
- Create: `services/cairn/doc.go`
- Create: `services/cairn/identity/doc.go`
- Create: `routers/api/cairn/doc.go`
- Create: `routers/api/cairn/v1/doc.go`
- Create: `routers/web/cairn/doc.go`
- Create: `routers/web/cairn/templates/md/doc.go`
- Create: `cmd/cairn/doc.go`

**Why:** Establishes the additive package layout from spec §4. Each package has a one-line `doc.go` so directories exist in the tree from day one and `go list ./...` sees them. Future tasks add real code into these directories.

- [ ] **Step 1: Create a feature branch off `cairn`**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-package-skeletons
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Create each `doc.go` stub**

Each file gets one comment line plus the package declaration. Example for `models/cairn/doc.go`:

```go
// Package cairn — Cairn-specific data models (agents, agent_blocklist, …).
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn
```

Apply the same pattern to all nine files, with appropriate one-line descriptions:

| Path | Description |
|---|---|
| `models/cairn/doc.go` | Cairn-specific data models (agents, agent_blocklist, …) |
| `models/cairn/migrations/doc.go` | Cairn schema migrations, registered with Forgejo's xorm engine |
| `services/cairn/doc.go` | Cairn-specific services |
| `services/cairn/identity/doc.go` | Agent identity primitives — fingerprinting, email parsing, signature verification |
| `routers/api/cairn/doc.go` | Cairn-specific REST API namespace |
| `routers/api/cairn/v1/doc.go` | Cairn API v1 endpoints |
| `routers/web/cairn/doc.go` | Cairn web UI augmentations (markdown rendering, .well-known) |
| `routers/web/cairn/templates/md/doc.go` | Server-side markdown templates for ?format=md rendering |
| `cmd/cairn/doc.go` | Cairn CLI commands (auth, agent registration, signing helper) |

For each, the package name in the `package` line is the last directory segment (e.g., `package md`, `package v1`, `package identity`).

- [ ] **Step 3: Verify the tree builds**

Run: `go build ./...`
Expected: success, no output.

- [ ] **Step 4: Verify packages are listed**

Run: `go list ./models/cairn/... ./services/cairn/... ./routers/api/cairn/... ./routers/web/cairn/... ./cmd/cairn/...`
Expected: nine package paths printed, one per line.

- [ ] **Step 5: Commit**

```bash
git add models/cairn services/cairn routers/api/cairn routers/web/cairn cmd/cairn
git commit -m "$(cat <<'EOF'
feat(cairn): create package skeletons for the Cairn additive layer

Establishes the directory layout per the design spec §4:
- models/cairn/ + models/cairn/migrations/
- services/cairn/ + services/cairn/identity/
- routers/api/cairn/ + routers/api/cairn/v1/
- routers/web/cairn/ + routers/web/cairn/templates/md/
- cmd/cairn/

Each directory has a one-line doc.go stub so the package exists in
the tree from day one. Subsequent commits add real code into these
packages without restructuring.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §4

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-package-skeletons
```

(Then merge to `cairn` per team cadence — direct push or PR.)

---

## Task 3: Define `Agent` and `AgentBlocklist` models with first migration

**Files:**
- Create: `models/cairn/agent.go`
- Create: `models/cairn/agent_test.go`
- Create: `models/cairn/agent_blocklist.go`
- Create: `models/cairn/agent_blocklist_test.go`
- Create: `models/cairn/migrations/v500_create_agent_tables.go`
- Modify: `models/migrations/migrations.go` (Forgejo's migration registry — append Cairn entries)

**Why:** Spec §5 defines the schema. xorm uses struct tags to map structs to tables. Migrations register with Forgejo's existing migration list so a single `cairn migrate` runs both sets.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-schema-and-migration
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write the failing test for `Agent` model in `models/cairn/agent_test.go`**

```go
package cairn

import (
	"testing"
	"time"
)

func TestAgent_TableName(t *testing.T) {
	var a Agent
	if got, want := a.TableName(), "cairn_agent"; got != want {
		t.Errorf("TableName() = %q, want %q", got, want)
	}
}

func TestAgent_StatusValues(t *testing.T) {
	if AgentStatusPending != "pending" {
		t.Errorf("AgentStatusPending = %q, want %q", AgentStatusPending, "pending")
	}
	if AgentStatusActive != "active" {
		t.Errorf("AgentStatusActive = %q, want %q", AgentStatusActive, "active")
	}
}

func TestAgent_IsActive(t *testing.T) {
	cases := []struct {
		name   string
		status AgentStatus
		want   bool
	}{
		{"pending", AgentStatusPending, false},
		{"active", AgentStatusActive, true},
		{"unknown", AgentStatus("unknown"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := Agent{Status: tc.status}
			if got := a.IsActive(); got != tc.want {
				t.Errorf("IsActive() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAgent_RequiredFields(t *testing.T) {
	now := time.Now()
	a := Agent{
		Fingerprint: "cairn:abc123",
		UserID:      42,
		Slug:        "plumb",
		Domain:      "darksoft.co.nz",
		PublicKey:   []byte{1, 2, 3, 4},
		Status:      AgentStatusActive,
		CreatedAt:   now,
		ActivatedAt: &now,
	}
	// Sanity: every required field is set without compile error.
	if a.Fingerprint == "" || a.UserID == 0 || a.Slug == "" {
		t.Error("required fields zero")
	}
}
```

- [ ] **Step 3: Run, expect failure**

Run: `go test ./models/cairn/...`
Expected: FAIL — `undefined: Agent`, `undefined: AgentStatus*`.

- [ ] **Step 4: Implement `models/cairn/agent.go`**

```go
package cairn

import "time"

// AgentStatus is the lifecycle state of an agent record.
type AgentStatus string

const (
	// AgentStatusPending — proposed, awaiting owner approval.
	AgentStatusPending AgentStatus = "pending"
	// AgentStatusActive — approved, may sign commits and act under the owner.
	AgentStatusActive AgentStatus = "active"
)

// Agent is the registered identity of a Cairn agent under a human owner.
//
// Fingerprint is HMAC-SHA256(instance_hmac_key, public_key), formatted as
// "cairn:" + base64. Slug uniqueness is scoped per UserID. Email convention
// is "nexus-{Slug}@{Domain}". See docs/cairn/specs/2026-05-09-cairn-
// foundation-design.md §5 and §6.
type Agent struct {
	ID          int64       `xorm:"pk autoincr"`
	Fingerprint string      `xorm:"VARCHAR(80) NOT NULL UNIQUE"`
	UserID      int64       `xorm:"NOT NULL INDEX"`
	Slug        string      `xorm:"VARCHAR(64) NOT NULL"`
	Domain      string      `xorm:"VARCHAR(255) NOT NULL"`
	PublicKey   []byte      `xorm:"BLOB NOT NULL"`
	Status      AgentStatus `xorm:"VARCHAR(16) NOT NULL DEFAULT 'pending'"`
	CreatedAt   time.Time   `xorm:"NOT NULL"`
	ActivatedAt *time.Time
}

// TableName returns the SQL table name.
func (a Agent) TableName() string {
	return "cairn_agent"
}

// IsActive reports whether the agent may currently act.
func (a Agent) IsActive() bool {
	return a.Status == AgentStatusActive
}
```

- [ ] **Step 5: Run, expect PASS**

Run: `go test ./models/cairn/...`
Expected: all `TestAgent_*` PASS.

- [ ] **Step 6: Write the failing test for `AgentBlocklist` in `models/cairn/agent_blocklist_test.go`**

```go
package cairn

import (
	"testing"
	"time"
)

func TestAgentBlocklist_TableName(t *testing.T) {
	var b AgentBlocklist
	if got, want := b.TableName(), "cairn_agent_blocklist"; got != want {
		t.Errorf("TableName() = %q, want %q", got, want)
	}
}

func TestAgentBlocklist_RequiredFields(t *testing.T) {
	b := AgentBlocklist{
		AgentID:   42,
		BlockedAt: time.Now(),
		Reason:    "key compromised",
	}
	if b.AgentID == 0 || b.BlockedAt.IsZero() {
		t.Error("required fields zero")
	}
}
```

- [ ] **Step 7: Run, expect failure**

Run: `go test ./models/cairn/...`
Expected: FAIL — `undefined: AgentBlocklist`.

- [ ] **Step 8: Implement `models/cairn/agent_blocklist.go`**

```go
package cairn

import "time"

// AgentBlocklist records that an agent has been blocked from acting.
// A non-empty row for AgentID means the agent's pushes are rejected
// even if its row in cairn_agent is status="active".
type AgentBlocklist struct {
	ID        int64     `xorm:"pk autoincr"`
	AgentID   int64     `xorm:"NOT NULL INDEX"`
	BlockedAt time.Time `xorm:"NOT NULL"`
	Reason    string    `xorm:"TEXT"`
}

// TableName returns the SQL table name.
func (b AgentBlocklist) TableName() string {
	return "cairn_agent_blocklist"
}
```

- [ ] **Step 9: Run, expect PASS**

Run: `go test ./models/cairn/...`
Expected: all PASS.

- [ ] **Step 10: Write the migration in `models/cairn/migrations/v500_create_agent_tables.go`**

```go
// Package migrations holds Cairn-specific schema migrations.
// Cairn migrations begin at v500 to leave room above Forgejo's existing
// series. They register with Forgejo's xorm engine alongside upstream's;
// one migration table, one ordering.

package migrations

import (
	"xorm.io/xorm"
)

// V500CreateAgentTables creates cairn_agent and cairn_agent_blocklist.
func V500CreateAgentTables(x *xorm.Engine) error {
	type Agent struct {
		ID          int64  `xorm:"pk autoincr"`
		Fingerprint string `xorm:"VARCHAR(80) NOT NULL UNIQUE"`
		UserID      int64  `xorm:"NOT NULL INDEX"`
		Slug        string `xorm:"VARCHAR(64) NOT NULL"`
		Domain      string `xorm:"VARCHAR(255) NOT NULL"`
		PublicKey   []byte `xorm:"BLOB NOT NULL"`
		Status      string `xorm:"VARCHAR(16) NOT NULL DEFAULT 'pending'"`
		CreatedAt   int64  `xorm:"NOT NULL"`
		ActivatedAt int64
	}

	type AgentBlocklist struct {
		ID        int64  `xorm:"pk autoincr"`
		AgentID   int64  `xorm:"NOT NULL INDEX"`
		BlockedAt int64  `xorm:"NOT NULL"`
		Reason    string `xorm:"TEXT"`
	}

	if err := x.Sync2(new(Agent), new(AgentBlocklist)); err != nil {
		return err
	}

	// Composite index for email lookup at push time.
	if _, err := x.Exec(
		`CREATE INDEX IF NOT EXISTS idx_cairn_agent_email_lookup ON cairn_agent (slug, domain)`,
	); err != nil {
		return err
	}
	// Slug uniqueness scoped per UserID.
	if _, err := x.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_cairn_agent_user_slug ON cairn_agent (user_id, slug)`,
	); err != nil {
		return err
	}
	return nil
}
```

The struct definitions inside the function are intentional — Forgejo's migration pattern uses local struct types so future model changes don't break old migrations.

- [ ] **Step 11: Register the migration with Forgejo's migrator**

Open `models/migrations/migrations.go`. Find the `migrations` slice (probably near the top of the file, declared as `var migrations = []Migration{ ... }`).

Find the last entry. Append after it:

```go
// Cairn migrations begin at v500.
NewMigration("Cairn: create agent tables", cairnmigrations.V500CreateAgentTables),
```

Add the import at the top of `migrations.go`:

```go
cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"
```

(Adjust the alias if `cairnmigrations` collides with an existing import.)

- [ ] **Step 12: Run the migration in a clean DB to verify it works**

```bash
# Build a Cairn binary
go build -o /tmp/cairn-test ./cmd/cairn || go build -o /tmp/cairn-test .

# Run migrations against a temp SQLite
mkdir -p /tmp/cairn-test-data
cat > /tmp/cairn-test-app.ini <<EOF
[database]
DB_TYPE = sqlite3
PATH = /tmp/cairn-test-data/cairn.db

[repository]
ROOT = /tmp/cairn-test-data/repos

[server]
APP_DATA_PATH = /tmp/cairn-test-data
DOMAIN = localhost
HTTP_PORT = 3000
EOF

/tmp/cairn-test migrate --config /tmp/cairn-test-app.ini
```

Verify the tables exist:

```bash
sqlite3 /tmp/cairn-test-data/cairn.db ".schema cairn_agent"
sqlite3 /tmp/cairn-test-data/cairn.db ".schema cairn_agent_blocklist"
sqlite3 /tmp/cairn-test-data/cairn.db ".indices cairn_agent"
```

Expected output: both tables visible, with the documented columns and the two indexes (`idx_cairn_agent_email_lookup` and `idx_cairn_agent_user_slug`).

- [ ] **Step 13: Run the model tests**

Run: `go test ./models/cairn/...`
Expected: all PASS.

- [ ] **Step 14: Clean up the test data dir**

```bash
rm -rf /tmp/cairn-test-data /tmp/cairn-test /tmp/cairn-test-app.ini
```

- [ ] **Step 15: Commit**

```bash
git add models/cairn/ models/migrations/migrations.go
git commit -m "$(cat <<'EOF'
feat(cairn): add Agent and AgentBlocklist models + migration v500

Adds the data foundation per the design spec §5:
- Agent struct: fingerprint, user_id, slug, domain, public_key,
  status (pending|active), created_at, activated_at. Slug uniqueness
  is scoped per user_id; lookup index on (slug, domain).
- AgentBlocklist struct: agent_id, blocked_at, reason.
- Migration v500 creates both tables and the (slug, domain) lookup
  index plus the (user_id, slug) uniqueness index.
- Registered with Forgejo's xorm migrator. v500 leaves room above
  Forgejo's existing v300-series.

Verified by running `cairn migrate` against a clean SQLite database
and inspecting the resulting schema.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §5

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-schema-and-migration
```

(Then merge to `cairn`.)

---

## Task 4: `AgentStore` interface + xorm implementation

**Files:**
- Create: `services/cairn/identity/store.go`
- Create: `services/cairn/identity/xorm_store.go`
- Create: `services/cairn/identity/xorm_store_test.go`

**Why:** Spec §4.1 mandates the connection-per-operation discipline. The interface is backend-agnostic so Postgres can drop in later. The xorm implementation always opens a session, executes, releases — never holds a session across boundaries.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-agent-store
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Define the interface in `services/cairn/identity/store.go`**

```go
package identity

import (
	"context"
	"errors"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// ErrAgentNotFound is returned when a lookup finds no matching agent row.
var ErrAgentNotFound = errors.New("cairn identity: agent not found")

// ErrAgentExists is returned when registration would violate the
// (user_id, slug) uniqueness.
var ErrAgentExists = errors.New("cairn identity: agent already exists for (user, slug)")

// AgentStore is the backend-agnostic data access for agents.
//
// Every method opens a short-lived session, executes, and releases.
// Implementations MUST NOT hold sessions across method boundaries.
type AgentStore interface {
	Register(ctx context.Context, a *cairn.Agent) error
	GetByFingerprint(ctx context.Context, fingerprint string) (*cairn.Agent, error)
	GetByEmail(ctx context.Context, slug, domain string) (*cairn.Agent, error)
	ListByUser(ctx context.Context, userID int64, status cairn.AgentStatus) ([]*cairn.Agent, error)
	Approve(ctx context.Context, fingerprint string) error
}
```

- [ ] **Step 3: Write the failing test in `services/cairn/identity/xorm_store_test.go`**

```go
package identity

import (
	"context"
	"testing"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	cairnmigrations "github.com/CarriedWorldUniverse/cairn/models/cairn/migrations"
	"xorm.io/xorm"
	_ "github.com/mattn/go-sqlite3"
)

func newTestEngine(t *testing.T) *xorm.Engine {
	t.Helper()
	eng, err := xorm.NewEngine("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := cairnmigrations.V500CreateAgentTables(eng); err != nil {
		t.Fatal(err)
	}
	return eng
}

func TestXormAgentStore_RegisterAndGet(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	a := &cairn.Agent{
		Fingerprint: "cairn:abc123",
		UserID:      1,
		Slug:        "plumb",
		Domain:      "darksoft.co.nz",
		PublicKey:   []byte{1, 2, 3, 4},
		Status:      cairn.AgentStatusActive,
		CreatedAt:   time.Now(),
	}
	if err := s.Register(ctx, a); err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 {
		t.Error("ID not populated after Register")
	}

	got, err := s.GetByFingerprint(ctx, "cairn:abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != "plumb" {
		t.Errorf("Slug = %q, want %q", got.Slug, "plumb")
	}
}

func TestXormAgentStore_GetByEmail(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	a := &cairn.Agent{
		Fingerprint: "cairn:def456",
		UserID:      1,
		Slug:        "plumb",
		Domain:      "darksoft.co.nz",
		PublicKey:   []byte{5, 6, 7, 8},
		Status:      cairn.AgentStatusActive,
		CreatedAt:   time.Now(),
	}
	if err := s.Register(ctx, a); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetByEmail(ctx, "plumb", "darksoft.co.nz")
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint != "cairn:def456" {
		t.Errorf("Fingerprint = %q, want %q", got.Fingerprint, "cairn:def456")
	}
}

func TestXormAgentStore_NotFound(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormAgentStore(eng)

	_, err := s.GetByFingerprint(context.Background(), "cairn:no-such-fp")
	if err != ErrAgentNotFound {
		t.Errorf("err = %v, want %v", err, ErrAgentNotFound)
	}

	_, err = s.GetByEmail(context.Background(), "ghost", "example.com")
	if err != ErrAgentNotFound {
		t.Errorf("err = %v, want %v", err, ErrAgentNotFound)
	}
}

func TestXormAgentStore_DuplicateRegister(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	a := &cairn.Agent{
		Fingerprint: "cairn:dup",
		UserID:      1,
		Slug:        "plumb",
		Domain:      "darksoft.co.nz",
		PublicKey:   []byte{1},
		Status:      cairn.AgentStatusActive,
		CreatedAt:   time.Now(),
	}
	if err := s.Register(ctx, a); err != nil {
		t.Fatal(err)
	}

	// Same (user_id, slug) should fail.
	a2 := *a
	a2.ID = 0
	a2.Fingerprint = "cairn:dup-2"
	err := s.Register(ctx, &a2)
	if err == nil {
		t.Error("expected error registering duplicate (user_id, slug)")
	}
}

func TestXormAgentStore_ListByUser(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	now := time.Now()
	for i, slug := range []string{"plumb", "anvil", "forge"} {
		a := &cairn.Agent{
			Fingerprint: "cairn:fp" + slug,
			UserID:      1,
			Slug:        slug,
			Domain:      "darksoft.co.nz",
			PublicKey:   []byte{byte(i)},
			Status:      cairn.AgentStatusActive,
			CreatedAt:   now,
		}
		if err := s.Register(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListByUser(ctx, 1, cairn.AgentStatusActive)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}

func TestXormAgentStore_Approve(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormAgentStore(eng)

	ctx := context.Background()
	a := &cairn.Agent{
		Fingerprint: "cairn:pending",
		UserID:      1,
		Slug:        "plumb",
		Domain:      "darksoft.co.nz",
		PublicKey:   []byte{1},
		Status:      cairn.AgentStatusPending,
		CreatedAt:   time.Now(),
	}
	if err := s.Register(ctx, a); err != nil {
		t.Fatal(err)
	}

	if err := s.Approve(ctx, "cairn:pending"); err != nil {
		t.Fatal(err)
	}

	got, _ := s.GetByFingerprint(ctx, "cairn:pending")
	if got.Status != cairn.AgentStatusActive {
		t.Errorf("Status = %q, want %q", got.Status, cairn.AgentStatusActive)
	}
	if got.ActivatedAt == nil || got.ActivatedAt.IsZero() {
		t.Error("ActivatedAt not set after approve")
	}
}
```

- [ ] **Step 4: Run, expect failure**

Run: `go test ./services/cairn/identity/...`
Expected: FAIL — `undefined: NewXormAgentStore`.

- [ ] **Step 5: Implement `services/cairn/identity/xorm_store.go`**

```go
package identity

import (
	"context"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"xorm.io/xorm"
)

// xormAgentStore is the xorm-backed AgentStore. Each method opens a
// short-lived session, executes, and releases — no long-lived sessions.
// See spec §4.1 for the connection discipline rationale.
type xormAgentStore struct {
	engine *xorm.Engine
}

// NewXormAgentStore returns an AgentStore backed by the given xorm engine.
func NewXormAgentStore(engine *xorm.Engine) AgentStore {
	return &xormAgentStore{engine: engine}
}

func (s *xormAgentStore) Register(ctx context.Context, a *cairn.Agent) error {
	sess := s.engine.NewSession()
	defer sess.Close()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	_, err := sess.Context(ctx).Insert(a)
	return err
}

func (s *xormAgentStore) GetByFingerprint(ctx context.Context, fingerprint string) (*cairn.Agent, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var a cairn.Agent
	has, err := sess.Context(ctx).Where("fingerprint = ?", fingerprint).Get(&a)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, ErrAgentNotFound
	}
	return &a, nil
}

func (s *xormAgentStore) GetByEmail(ctx context.Context, slug, domain string) (*cairn.Agent, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var a cairn.Agent
	has, err := sess.Context(ctx).
		Where("slug = ? AND domain = ?", slug, domain).
		Get(&a)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, ErrAgentNotFound
	}
	return &a, nil
}

func (s *xormAgentStore) ListByUser(ctx context.Context, userID int64, status cairn.AgentStatus) ([]*cairn.Agent, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var out []*cairn.Agent
	q := sess.Context(ctx).Where("user_id = ?", userID)
	if status != "" {
		q = q.And("status = ?", string(status))
	}
	if err := q.Find(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *xormAgentStore) Approve(ctx context.Context, fingerprint string) error {
	sess := s.engine.NewSession()
	defer sess.Close()
	now := time.Now()
	count, err := sess.Context(ctx).
		Where("fingerprint = ?", fingerprint).
		Cols("status", "activated_at").
		Update(&cairn.Agent{
			Status:      cairn.AgentStatusActive,
			ActivatedAt: &now,
		})
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrAgentNotFound
	}
	return nil
}
```

- [ ] **Step 6: Run, expect PASS**

Run: `go test ./services/cairn/identity/...`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add services/cairn/identity/
git commit -m "$(cat <<'EOF'
feat(cairn): add AgentStore interface + xorm implementation

Backend-agnostic AgentStore interface with operations: Register,
GetByFingerprint, GetByEmail, ListByUser, Approve.

xormAgentStore implementation enforces the connection-per-operation
discipline (spec §4.1) — every method opens a short-lived session,
executes, releases. No long-lived sessions held across boundaries.

Tests cover register-and-get, lookup-by-email, not-found,
duplicate registration (uniqueness violation), list-by-user,
and approval transitions pending->active with ActivatedAt set.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §4.1, §5

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-agent-store
```

(Merge to `cairn`.)

---

## Task 5: `AgentBlocklistStore` interface + xorm implementation

**Files:**
- Modify: `services/cairn/identity/store.go` (add `AgentBlocklistStore` interface)
- Create: `services/cairn/identity/xorm_blocklist_store.go`
- Create: `services/cairn/identity/xorm_blocklist_store_test.go`

**Why:** Spec §6 mandates an `agent_blocklist` for surgical revocation, paired with the nuclear root-rotation primitive. Same connection discipline as `AgentStore`.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-blocklist-store
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Add `AgentBlocklistStore` interface to `services/cairn/identity/store.go`**

Append after the `AgentStore` interface:

```go
// AgentBlocklistStore is the backend-agnostic data access for the
// agent blocklist. Same connection-per-operation discipline.
type AgentBlocklistStore interface {
	Block(ctx context.Context, agentID int64, reason string) error
	IsBlocked(ctx context.Context, agentID int64) (bool, error)
	List(ctx context.Context) ([]*cairn.AgentBlocklist, error)
}
```

- [ ] **Step 3: Write the failing test in `services/cairn/identity/xorm_blocklist_store_test.go`**

```go
package identity

import (
	"context"
	"testing"
)

func TestXormBlocklistStore_BlockAndIsBlocked(t *testing.T) {
	eng := newTestEngine(t) // helper from xorm_store_test.go
	defer eng.Close()
	s := NewXormBlocklistStore(eng)

	ctx := context.Background()
	const agentID int64 = 42

	blocked, err := s.IsBlocked(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked {
		t.Error("agent reported blocked before any Block() call")
	}

	if err := s.Block(ctx, agentID, "key compromised"); err != nil {
		t.Fatal(err)
	}

	blocked, err = s.IsBlocked(ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("agent not reported blocked after Block() call")
	}
}

func TestXormBlocklistStore_List(t *testing.T) {
	eng := newTestEngine(t)
	defer eng.Close()
	s := NewXormBlocklistStore(eng)

	ctx := context.Background()
	for _, id := range []int64{1, 2, 3} {
		if err := s.Block(ctx, id, "test"); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}
```

- [ ] **Step 4: Run, expect failure**

Run: `go test ./services/cairn/identity/...`
Expected: FAIL — `undefined: NewXormBlocklistStore`.

- [ ] **Step 5: Implement `services/cairn/identity/xorm_blocklist_store.go`**

```go
package identity

import (
	"context"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"xorm.io/xorm"
)

type xormBlocklistStore struct {
	engine *xorm.Engine
}

// NewXormBlocklistStore returns an AgentBlocklistStore backed by xorm.
func NewXormBlocklistStore(engine *xorm.Engine) AgentBlocklistStore {
	return &xormBlocklistStore{engine: engine}
}

func (s *xormBlocklistStore) Block(ctx context.Context, agentID int64, reason string) error {
	sess := s.engine.NewSession()
	defer sess.Close()
	row := &cairn.AgentBlocklist{
		AgentID:   agentID,
		BlockedAt: time.Now(),
		Reason:    reason,
	}
	_, err := sess.Context(ctx).Insert(row)
	return err
}

func (s *xormBlocklistStore) IsBlocked(ctx context.Context, agentID int64) (bool, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	count, err := sess.Context(ctx).
		Where("agent_id = ?", agentID).
		Count(&cairn.AgentBlocklist{})
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *xormBlocklistStore) List(ctx context.Context) ([]*cairn.AgentBlocklist, error) {
	sess := s.engine.NewSession()
	defer sess.Close()
	var out []*cairn.AgentBlocklist
	if err := sess.Context(ctx).Find(&out); err != nil {
		return nil, err
	}
	return out, nil
}
```

- [ ] **Step 6: Run, expect PASS**

Run: `go test ./services/cairn/identity/...`
Expected: all tests PASS (both AgentStore and BlocklistStore).

- [ ] **Step 7: Commit**

```bash
git add services/cairn/identity/store.go services/cairn/identity/xorm_blocklist_store.go services/cairn/identity/xorm_blocklist_store_test.go
git commit -m "$(cat <<'EOF'
feat(cairn): add AgentBlocklistStore interface + xorm impl

Operations: Block (record), IsBlocked (lookup), List (admin view).
Same connection-per-operation discipline as AgentStore.

Tests cover the block transition, the not-blocked default state,
and the multi-row list operation.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §5, §6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-blocklist-store
```

(Merge to `cairn`.)

---

## Task 6: `Fingerprint` and `ParseAgentEmail` primitives

**Files:**
- Create: `services/cairn/identity/identity.go`
- Create: `services/cairn/identity/identity_test.go`

**Why:** Spec §6 specifies HMAC-SHA256 fingerprint with per-instance key, formatted `cairn:<base64>`. Email parser extracts slug and domain from author addresses matching `nexus-{slug}@{domain}`. Both are pure functions with no state; tests are exhaustive.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-identity-primitives
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write the failing test in `services/cairn/identity/identity_test.go`**

```go
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func TestFingerprint_Format(t *testing.T) {
	hmacKey := []byte("test-instance-hmac-key-32-bytes!!")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	fp := Fingerprint(hmacKey, pub)

	if !strings.HasPrefix(fp, "cairn:") {
		t.Errorf("fingerprint missing cairn: prefix: %q", fp)
	}
	// HMAC-SHA256 is 32 bytes; base64 with padding is 44 chars,
	// without padding 43. Total with prefix: 49 or 50 chars.
	if l := len(fp); l < 49 || l > 51 {
		t.Errorf("fingerprint length unexpected: %d (%q)", l, fp)
	}
}

func TestFingerprint_Deterministic(t *testing.T) {
	hmacKey := []byte("test-instance-hmac-key-32-bytes!!")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	a := Fingerprint(hmacKey, pub)
	b := Fingerprint(hmacKey, pub)

	if a != b {
		t.Errorf("non-deterministic: %q != %q", a, b)
	}
}

func TestFingerprint_DifferentKeysProduceDifferentFingerprints(t *testing.T) {
	hmacKey := []byte("test-instance-hmac-key-32-bytes!!")
	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)

	fp1 := Fingerprint(hmacKey, pub1)
	fp2 := Fingerprint(hmacKey, pub2)

	if fp1 == fp2 {
		t.Error("different pubkeys produced identical fingerprint")
	}
}

func TestFingerprint_DifferentHMACKeysProduceDifferentFingerprints(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	keyA := []byte("instance-A-hmac-key-bytes-32-len!")
	keyB := []byte("instance-B-hmac-key-bytes-32-len!")

	fpA := Fingerprint(keyA, pub)
	fpB := Fingerprint(keyB, pub)

	if fpA == fpB {
		t.Error("same pubkey on different instances produced same fingerprint")
	}
}

func TestParseAgentEmail_Valid(t *testing.T) {
	cases := []struct {
		email      string
		wantSlug   string
		wantDomain string
	}{
		{"nexus-plumb@darksoft.co.nz", "plumb", "darksoft.co.nz"},
		{"nexus-anvil@example.com", "anvil", "example.com"},
		{"nexus-x@y.z", "x", "y.z"},
	}
	for _, tc := range cases {
		t.Run(tc.email, func(t *testing.T) {
			slug, domain, ok := ParseAgentEmail(tc.email)
			if !ok {
				t.Fatalf("ok=false for %q", tc.email)
			}
			if slug != tc.wantSlug {
				t.Errorf("slug = %q, want %q", slug, tc.wantSlug)
			}
			if domain != tc.wantDomain {
				t.Errorf("domain = %q, want %q", domain, tc.wantDomain)
			}
		})
	}
}

func TestParseAgentEmail_Invalid(t *testing.T) {
	cases := []string{
		"nexus@darksoft.co.nz",         // no nexus- prefix
		"nexus-@darksoft.co.nz",          // empty slug
		"nexus-plumb",                    // no @
		"nexus-plumb@",                   // empty domain
		"",                               // empty
		"NEXUS-PLUMB@darksoft.co.nz",     // case-sensitive — uppercase rejected
		"nexus-PLUMB@darksoft.co.nz",     // mixed case slug rejected
		"nexus-pl umb@darksoft.co.nz",    // space in slug
	}
	for _, e := range cases {
		t.Run(e, func(t *testing.T) {
			slug, domain, ok := ParseAgentEmail(e)
			if ok {
				t.Errorf("ok=true for invalid %q (slug=%q domain=%q)", e, slug, domain)
			}
		})
	}
}
```

- [ ] **Step 3: Run, expect failure**

Run: `go test ./services/cairn/identity/...`
Expected: FAIL — `undefined: Fingerprint, ParseAgentEmail`.

- [ ] **Step 4: Implement `services/cairn/identity/identity.go`**

```go
// Package identity holds Cairn's agent identity primitives:
// fingerprinting, email parsing, signature verification, and
// instance-HMAC-key handling.
//
// See docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6.
package identity

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
)

const fingerprintPrefix = "cairn:"

// Fingerprint computes an agent's fingerprint as
// "cairn:" + base64(HMAC-SHA256(instanceHMACKey, publicKey)).
// The HMAC binds the fingerprint to the issuing instance, providing
// cross-instance unlinkability and resistance to fingerprint spoofing
// without the instance key.
func Fingerprint(instanceHMACKey []byte, publicKey ed25519.PublicKey) string {
	mac := hmac.New(sha256.New, instanceHMACKey)
	mac.Write(publicKey)
	sum := mac.Sum(nil)
	return fingerprintPrefix + base64.StdEncoding.EncodeToString(sum)
}

// agentEmailPattern matches "nexus-<slug>@<domain>" with slug consisting
// of lowercase letters, digits, and hyphens. Domain is anything after @.
var agentEmailPattern = regexp.MustCompile(`^nexus-([a-z0-9][a-z0-9-]*)@([^\s@]+)$`)

// ParseAgentEmail extracts (slug, domain) from an agent email of the form
// "nexus-{slug}@{domain}". Returns ok=false for non-agent emails or
// malformed input.
func ParseAgentEmail(email string) (slug, domain string, ok bool) {
	m := agentEmailPattern.FindStringSubmatch(email)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}
```

- [ ] **Step 5: Run, expect PASS**

Run: `go test ./services/cairn/identity/...`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add services/cairn/identity/identity.go services/cairn/identity/identity_test.go
git commit -m "$(cat <<'EOF'
feat(cairn): add Fingerprint and ParseAgentEmail primitives

Fingerprint(hmacKey, pubkey) returns "cairn:" + base64(HMAC-SHA256(...))
per spec §6. HMAC binds the fingerprint to the issuing instance for
cross-instance unlinkability and spoof resistance.

ParseAgentEmail parses "nexus-{slug}@{domain}" via regex with
slug constrained to lowercase alphanumerics + hyphens.

Tests cover format, determinism, instance-key sensitivity,
public-key sensitivity, and a battery of invalid email shapes.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-identity-primitives
```

(Merge to `cairn`.)

---

## Task 7: Instance HMAC key — load and first-run generation

**Files:**
- Create: `services/cairn/identity/instance_init.go`
- Create: `services/cairn/identity/instance_init_test.go`

**Why:** Spec §6 requires the instance HMAC key to live at a configured path (default `~cairn-data/instance-hmac.key`), generated on first run, mode `0400`. `LoadInstanceHMACKey` either reads the existing file or creates a new one with `crypto/rand`.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-instance-hmac-init
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write the failing test in `services/cairn/identity/instance_init_test.go`**

```go
package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadInstanceHMACKey_GeneratesIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance-hmac.key")

	key, err := LoadInstanceHMACKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(key) != 32 {
		t.Errorf("key length = %d, want 32", len(key))
	}

	// File should now exist with mode 0400.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0400 {
		t.Errorf("file mode = %#o, want 0400", perm)
	}
}

func TestLoadInstanceHMACKey_ReadsExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance-hmac.key")

	first, err := LoadInstanceHMACKey(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadInstanceHMACKey(path)
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Error("subsequent loads returned different keys")
	}
}

func TestLoadInstanceHMACKey_RejectsTooShort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "instance-hmac.key")
	// Write a 16-byte file (too short).
	if err := os.WriteFile(path, []byte("0123456789abcdef"), 0400); err != nil {
		t.Fatal(err)
	}

	_, err := LoadInstanceHMACKey(path)
	if err == nil {
		t.Error("expected error for too-short key")
	}
}

func TestGenerateInstanceHMACKey_Random(t *testing.T) {
	a, err := GenerateInstanceHMACKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateInstanceHMACKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 32 || len(b) != 32 {
		t.Errorf("generated key lengths: %d, %d", len(a), len(b))
	}
	if string(a) == string(b) {
		t.Error("two calls returned identical keys (vanishingly unlikely)")
	}
}
```

- [ ] **Step 3: Run, expect failure**

Run: `go test ./services/cairn/identity/...`
Expected: FAIL — `undefined: LoadInstanceHMACKey, GenerateInstanceHMACKey`.

- [ ] **Step 4: Implement `services/cairn/identity/instance_init.go`**

```go
package identity

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
)

const minHMACKeyBytes = 32

// GenerateInstanceHMACKey returns 32 fresh random bytes suitable for
// use as an HMAC-SHA256 key.
func GenerateInstanceHMACKey() ([]byte, error) {
	b := make([]byte, minHMACKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("cairn identity: generate HMAC key: %w", err)
	}
	return b, nil
}

// LoadInstanceHMACKey reads the HMAC key from path. If the file does
// not exist, generates a new key, writes it to path with mode 0400,
// and returns it. Returns an error if the file exists but is shorter
// than 32 bytes.
//
// Caller is responsible for ensuring the parent directory exists with
// appropriate ownership.
func LoadInstanceHMACKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil && os.IsNotExist(err) {
		// First run: generate and persist.
		key, gerr := GenerateInstanceHMACKey()
		if gerr != nil {
			return nil, gerr
		}
		if werr := os.WriteFile(path, key, 0400); werr != nil {
			return nil, fmt.Errorf("cairn identity: write HMAC key %q: %w", path, werr)
		}
		return key, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cairn identity: read HMAC key %q: %w", path, err)
	}
	if len(b) < minHMACKeyBytes {
		return nil, errors.New("cairn identity: HMAC key file too short (min 32 bytes)")
	}
	return b, nil
}
```

- [ ] **Step 5: Run, expect PASS**

Run: `go test ./services/cairn/identity/...`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add services/cairn/identity/instance_init.go services/cairn/identity/instance_init_test.go
git commit -m "$(cat <<'EOF'
feat(cairn): add instance HMAC key load + first-run generation

LoadInstanceHMACKey(path):
- if file missing: generate 32 random bytes, write with mode 0400, return
- if file present and >=32 bytes: read and return
- if file present but too short: error

GenerateInstanceHMACKey: 32 bytes from crypto/rand.

Tests cover first-run generation, idempotent reads, file-mode
enforcement, and the too-short rejection path.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-instance-hmac-init
```

(Merge to `cairn`.)

---

## Task 8: `VerifyCommitSignature` — Ed25519 signature verification

**Files:**
- Create: `services/cairn/identity/signature.go`
- Create: `services/cairn/identity/signature_test.go`

**Why:** Spec §6 — Cairn verifies commit signatures against registered agent public keys. Git uses SSH-format Ed25519 signatures via `gpg.format = ssh`. The verification logic accepts a signed payload + signature blob and a public key, and returns nil for valid or an error for invalid.

This task uses Go's `crypto/ed25519.Verify`. SSH-format signature parsing is wrapped in a small helper. The actual integration into Forgejo's pre-receive hook is a separate task in a later plan.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
git checkout -b cairn-signature-verify
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write the failing test in `services/cairn/identity/signature_test.go`**

```go
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
)

func TestVerifyCommitSignature_Valid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte("commit object payload to be signed")
	sig := ed25519.Sign(priv, payload)

	if err := VerifyCommitSignature(payload, sig, pub); err != nil {
		t.Errorf("valid signature rejected: %v", err)
	}
}

func TestVerifyCommitSignature_Invalid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte("commit object payload")
	sig := ed25519.Sign(priv, payload)

	// Flip a byte in the signature.
	tampered := make([]byte, len(sig))
	copy(tampered, sig)
	tampered[0] ^= 0xff

	err := VerifyCommitSignature(payload, tampered, pub)
	if err == nil {
		t.Error("tampered signature accepted")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestVerifyCommitSignature_WrongKey(t *testing.T) {
	pub1, priv1, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	_ = pub1
	payload := []byte("commit payload")
	sig := ed25519.Sign(priv1, payload)

	err := VerifyCommitSignature(payload, sig, pub2)
	if err == nil {
		t.Error("signature accepted under wrong public key")
	}
}

func TestVerifyCommitSignature_TamperedPayload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte("original commit payload")
	sig := ed25519.Sign(priv, payload)

	tamperedPayload := []byte("modified commit payload")

	err := VerifyCommitSignature(tamperedPayload, sig, pub)
	if err == nil {
		t.Error("signature accepted for tampered payload")
	}
}

func TestVerifyCommitSignature_MalformedInputs(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := VerifyCommitSignature(nil, []byte{1, 2, 3}, pub); err == nil {
		t.Error("nil payload accepted")
	}
	if err := VerifyCommitSignature([]byte("p"), nil, pub); err == nil {
		t.Error("nil signature accepted")
	}
	if err := VerifyCommitSignature([]byte("p"), []byte{1, 2}, pub); err == nil {
		t.Error("too-short signature accepted")
	}
	if err := VerifyCommitSignature([]byte("p"), make([]byte, 64), nil); err == nil {
		t.Error("nil pubkey accepted")
	}
}
```

- [ ] **Step 3: Run, expect failure**

Run: `go test ./services/cairn/identity/...`
Expected: FAIL — `undefined: VerifyCommitSignature, ErrInvalidSignature`.

- [ ] **Step 4: Implement `services/cairn/identity/signature.go`**

```go
package identity

import (
	"crypto/ed25519"
	"errors"
)

// ErrInvalidSignature is returned by VerifyCommitSignature when the
// signature does not verify against the public key.
var ErrInvalidSignature = errors.New("cairn identity: invalid signature")

// VerifyCommitSignature verifies an Ed25519 signature on a payload
// against a public key. Returns nil on valid, ErrInvalidSignature on
// invalid, or another error for malformed inputs.
//
// Note: this verifies *raw* Ed25519 signatures. The pre-receive hook
// is responsible for extracting the signature blob and the signed
// payload (commit object minus the signature trailer) from the git
// commit before calling this function. SSH-format wrapping (which
// git produces with gpg.format=ssh) is unwrapped at the call site.
func VerifyCommitSignature(payload, signature []byte, publicKey ed25519.PublicKey) error {
	if payload == nil {
		return errors.New("cairn identity: nil payload")
	}
	if len(signature) == 0 {
		return errors.New("cairn identity: empty signature")
	}
	if len(signature) != ed25519.SignatureSize {
		return errors.New("cairn identity: signature size mismatch")
	}
	if len(publicKey) == 0 {
		return errors.New("cairn identity: empty public key")
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("cairn identity: public key size mismatch")
	}

	if !ed25519.Verify(publicKey, payload, signature) {
		return ErrInvalidSignature
	}
	return nil
}
```

- [ ] **Step 5: Run, expect PASS**

Run: `go test ./services/cairn/identity/...`
Expected: all PASS.

- [ ] **Step 6: Run the full test suite for the identity package**

Run: `go test -v ./services/cairn/identity/...`
Expected: every Cairn-side identity test passes (Fingerprint, ParseAgentEmail, LoadInstanceHMACKey, AgentStore, BlocklistStore, VerifyCommitSignature).

- [ ] **Step 7: Commit**

```bash
git add services/cairn/identity/signature.go services/cairn/identity/signature_test.go
git commit -m "$(cat <<'EOF'
feat(cairn): add VerifyCommitSignature for Ed25519 commit signatures

Pure-Ed25519 signature verification. Returns nil for valid,
ErrInvalidSignature for invalid, or other errors for malformed
inputs.

The function takes raw bytes — payload, signature, public key.
SSH-format unwrapping (which git produces with gpg.format=ssh)
is the call-site's responsibility; this function focuses on the
crypto, not the framing.

Tests cover happy path, tampered signature, wrong key, tampered
payload, and malformed input variants.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-signature-verify
```

(Merge to `cairn`.)

---

## End-of-plan verification

After all 8 tasks land on the `cairn` branch:

- [ ] **Run the full test suite**

```bash
cd ~/Source/cairn
git checkout cairn
git pull
go test ./models/cairn/... ./services/cairn/... -v
```

Expected: every test in this plan PASSES.

- [ ] **Verify the migration runs against a clean database**

```bash
go build -o /tmp/cairn-foundation-test .
mkdir -p /tmp/cairn-foundation-data

cat > /tmp/cairn-foundation.ini <<EOF
[database]
DB_TYPE = sqlite3
PATH = /tmp/cairn-foundation-data/cairn.db

[repository]
ROOT = /tmp/cairn-foundation-data/repos

[server]
APP_DATA_PATH = /tmp/cairn-foundation-data
DOMAIN = localhost
HTTP_PORT = 3000
EOF

/tmp/cairn-foundation-test migrate --config /tmp/cairn-foundation.ini

sqlite3 /tmp/cairn-foundation-data/cairn.db "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'cairn_%';"
```

Expected output:
```
cairn_agent
cairn_agent_blocklist
```

Then clean up:

```bash
rm -rf /tmp/cairn-foundation-test /tmp/cairn-foundation-data /tmp/cairn-foundation.ini
```

- [ ] **Notify the operator**

Plan 1 (foundation — identity layer) complete. Cairn now has:
- `casket.DeriveAgentKey` for HKDF-Ed25519 agent key derivation (in casket-go)
- `cairn_agent` and `cairn_agent_blocklist` tables, registered with Forgejo's xorm engine
- `AgentStore` and `AgentBlocklistStore` interfaces with xorm implementations and the connection-per-operation discipline
- `Fingerprint`, `ParseAgentEmail`, `LoadInstanceHMACKey`, `GenerateInstanceHMACKey`, `VerifyCommitSignature` primitives in `services/cairn/identity/`

Ready for **Plan 2: Registration flow — API + CLI**, which builds on this foundation and produces the first user-visible Cairn surface.

---

## Notes for the executing agent

- Cairn uses xorm. xorm sessions wrap a `database/sql` connection; `NewSession`/`Close` is `Acquire`/`Release` against the pool. Never store a session in a struct field.
- Tests use in-memory SQLite (`:memory:`). Each test calls `newTestEngine` for an isolated engine; engines are closed via `defer eng.Close()`.
- Forgejo's existing test framework (`models/unittest`) is **not** used here. Cairn-specific tests are independent so they run without the Forgejo fixtures dance.
- All commits use `nexus-cw <nexus@darksoft.co.nz>` as the committer, matching the team's existing convention. Per-agent attribution lands in a later plan once Cairn's identity machinery is wired into git.
- Every task ends with a single commit and a feature branch push. Merging to `cairn` happens per team cadence — direct-push if branch protection is off, PR if it's on. The plan does not prescribe one or the other.
- If a step fails for reasons not covered here (build error, flaky test, environment issue), surface to the operator before forcing a workaround. Do not paper over a real failure with a code change that wasn't planned.
