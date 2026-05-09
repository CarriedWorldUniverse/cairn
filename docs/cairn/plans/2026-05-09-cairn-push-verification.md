# Cairn Push Verification Implementation Plan (Plan 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cairn rejects pushes whose agent-attributed commits fail signature verification, ownership check, or blocklist check. Plus the deferred items from Plans 2a/2b reviews: slug/domain validation, schema parity test, VerifyTrailers helper, author rendering badge.

**Architecture:** Pre-receive hook integration — small patch on Forgejo's `routers/private/hook_pre_receive.go` calling out to a new `services/cairn/hook/verify.go`. The verify function iterates push commits, extracts SSH signatures from commit objects, calls Plan 1's `VerifyCommitSignature` against the registered agent's public key. Push rejected on any failure. All gated by `setting.Cairn.EnforceSignatures` (default false during migration; flipped to true for production).

**Tech Stack:** Go 1.25+, Forgejo's `modules/git` (commit-object access), `golang.org/x/crypto/ssh` (signature parsing — same package Plan 2b uses for sshsig), Cairn's existing identity primitives.

**Spec ref:** [`docs/cairn/specs/2026-05-09-cairn-foundation-design.md`](../specs/2026-05-09-cairn-foundation-design.md), §4.4 (pre-receive hook), §4.7 (author rendering), §6 (verification flow), §10 (`enforce_signatures` flag).

**Plan 1/2a/2b dependencies:** `cairn.Agent`, `AgentService.GetByEmail` / `IsBlocked`, `Fingerprint`, `ParseAgentEmail`, `VerifyCommitSignature`. Forgejo's hook + git infrastructure.

**Pattern conventions** (established in Plans 2a/2b):
- TDD: failing test → implementation → passing test
- One commit per task, feature branch, controller merges
- httptest for HTTP, `cairntest.NewEngine(t)` for storage
- Connection-per-operation discipline
- `errors.Is` sentinel matching, never string-matching

---

## Task 1: Slug/domain validation in AgentService

**Files:**
- Modify: `services/cairn/identity/agent_service.go` (add validation in Register)
- Modify: `services/cairn/identity/agent_service_test.go` (add validation tests)

**Why:** Plan 2a final review flagged that anonymous Register accepts arbitrary slug + domain. Add grammar-level validation now that the API is real.

**Decisions:**
- Slug grammar: `^[a-z0-9][a-z0-9-]*$`, max 64 chars (matches `agentEmailPattern` from Plan 1)
- Domain: non-empty, max 255 chars (matches the schema VARCHAR(255))
- New error sentinel: `ErrInvalidInput`
- Validation runs in `AgentService.Register` BEFORE `s.users.UserIDByUsername` (cheap reject)

**Tests to add** (TDD pattern — write failing first):

```go
func TestAgentService_Register_RejectsInvalidSlug(t *testing.T) {
	svc, _ := newTestService(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	cases := []struct{ name, slug string }{
		{"empty", ""},
		{"uppercase", "Plumb"},
		{"leading-hyphen", "-plumb"},
		{"underscore", "plum_b"},
		{"space", "plu mb"},
		{"too-long", strings.Repeat("a", 65)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Register(context.Background(), RegisterRequest{
				ProposedOwner: "alice",
				Slug:          tc.slug,
				Domain:        "darksoft.co.nz",
				PublicKey:     pub,
			}, &Caller{UserID: 1, Username: "alice"})
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestAgentService_Register_RejectsInvalidDomain(t *testing.T) {
	svc, _ := newTestService(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	cases := []struct{ name, domain string }{
		{"empty", ""},
		{"too-long", strings.Repeat("a", 256)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Register(context.Background(), RegisterRequest{
				ProposedOwner: "alice",
				Slug:          "plumb",
				Domain:        tc.domain,
				PublicKey:     pub,
			}, &Caller{UserID: 1, Username: "alice"})
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}
```

**Implementation in `agent_service.go`:**

```go
import "regexp"

var (
	ErrInvalidInput = errors.New("cairn identity: invalid input")
	slugPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

const (
	maxSlugLen   = 64
	maxDomainLen = 255
)

func validateRegisterRequest(req RegisterRequest) error {
	if req.Slug == "" || len(req.Slug) > maxSlugLen || !slugPattern.MatchString(req.Slug) {
		return fmt.Errorf("%w: slug must match [a-z0-9][a-z0-9-]* and be 1-%d chars", ErrInvalidInput, maxSlugLen)
	}
	if req.Domain == "" || len(req.Domain) > maxDomainLen {
		return fmt.Errorf("%w: domain must be 1-%d chars", ErrInvalidInput, maxDomainLen)
	}
	return nil
}
```

Call at top of `Register`. The handler already maps `ErrAgentExists` and others; map `ErrInvalidInput` → 400 in `routers/api/cairn/v1/agents.go` PostAgents (one extra `errors.Is` arm).

**Verify** the existing `TestAgentService_Register_AutoApproveWhenProposerIsOwner` etc. still pass.

**Commit message starts:** `feat(cairn): slug/domain validation in AgentService.Register`

---

## Task 2: VerifyTrailers helper

**Files:**
- Create: `services/cairn/identity/trailers.go`
- Create: `services/cairn/identity/trailers_test.go`

**Why:** Spec §6 specifies that commit trailers (`Agent-Id`, `Agent-Owner`, `Agent-Domain`) cross-validate with the parsed author email and the agent record. Plan 3's pre-receive hook needs this helper.

**Implementation:**

```go
// VerifyTrailers parses git commit trailers (the optional last
// paragraph in a commit message) and cross-validates Agent-Id,
// Agent-Owner, Agent-Domain against the agent record + caller-derived
// owner username. Returns nil if all match (or no trailers — they're
// optional from the wire's perspective). Returns ErrTrailerMismatch
// if any trailer disagrees.
func VerifyTrailers(commitMessage string, agent *cairn.Agent, ownerUsername string) error
```

Trailer format per `git interpret-trailers`: a trailer is `Key: Value` on its own line in the last paragraph. We parse only the three Cairn-relevant ones; ignore others.

**Test cases:**
- Valid trailers all matching → nil
- Agent-Id mismatch → ErrTrailerMismatch
- Agent-Owner mismatch → ErrTrailerMismatch
- Agent-Domain mismatch → ErrTrailerMismatch
- Missing trailers (commit has no `Agent-*` lines at all) → nil (trailers are optional in the wire format; presence-and-mismatch is the rejection condition)
- Trailer with leading/trailing whitespace → trimmed before compare

Use Go's `strings.Split(message, "\n\n")` to get paragraphs; the last one is candidate trailers.

**Commit message starts:** `feat(cairn): VerifyTrailers helper for commit trailer cross-validation`

---

## Task 3: Schema parity test

**Files:**
- Create: `models/cairn/migrations/schema_parity_test.go`

**Why:** Plan 1's final review flagged that the migration's local Agent struct could drift from the runtime `cairn.Agent`. Add a test that runs both paths against in-memory SQLite and diffs the resulting schemas.

**Test outline:**
1. Open in-memory engine A; run `V500CreateAgentTables(engineA)` (the migration path)
2. Open in-memory engine B; configure mapper; call `engineB.Sync(new(cairn.Agent), new(cairn.AgentBlocklist))` (the SyncAllTables path)
3. Query both engines' `sqlite_master` for `cairn_agent` and `cairn_agent_blocklist`: get the column list, types, indexes
4. Assert engine A's schema matches engine B's

If they diverge in any column type, NULL-ability, or index, the test fails — flagging future migration drift early.

**Commit message starts:** `test(cairn): schema parity between migration and SyncAllTables paths`

---

## Task 4: SSH signature extraction from commit objects

**Files:**
- Create: `services/cairn/hook/signature.go`
- Create: `services/cairn/hook/signature_test.go`

**Why:** Git stores ssh signatures in commit object headers (`gpgsig` field) when configured with `gpg.format=ssh`. Cairn's hook needs to extract the signature blob, parse PROTOCOL.sshsig, and pass the inner signature + signed data to `VerifyCommitSignature`.

**Reference implementation:** see Plan 2b Task 6's `parseSSHSignatureBlob` and `verifySSHSignedData` in `cmd/cairn/commit_sign_helper.go` — same wire format, same parsing logic. Lift those helpers into `services/cairn/hook/signature.go` so they're reachable from the hook (and refactor cmd/cairn/commit_sign_helper.go to import from the new location, removing the duplication).

**Functions:**
- `ExtractSignedCommitData(commitRaw []byte) (signedPayload, signatureBlock []byte, found bool, err error)` — parses commit object, finds `gpgsig` header, returns the data-as-signed (commit object minus the signature) and the signature PEM block
- `ParseSSHSignature(armored []byte) (sig *ssh.Signature, namespace string, err error)` — parses PEM block per PROTOCOL.sshsig
- `VerifyAgentSignature(commitRaw []byte, agentPubKey ed25519.PublicKey) error` — top-level: extract → parse → verify; returns `ErrSignatureMissing` if no `gpgsig`, `ErrInvalidSignature` if verification fails

**Tests:**
- Round-trip: produce a commit object with a known signature using Plan 2b's signing path, then verify via the new helper
- Tampered commit body → ErrInvalidSignature
- Wrong key → ErrInvalidSignature
- Commit without `gpgsig` → ErrSignatureMissing
- Malformed PEM block → typed error

**Commit message starts:** `feat(cairn): SSH signature extraction from commit objects`

---

## Task 5: Hook verification logic

**Files:**
- Create: `services/cairn/hook/verify.go`
- Create: `services/cairn/hook/verify_test.go`

**Why:** The actual push-verification logic. Iterates commits in the push, treats agent emails specially, calls signature/ownership/blocklist checks.

**Function signature:**

```go
// VerifyAgentCommits walks the new commits in a push and rejects any
// that look like agent commits (author matching nexus-{slug}@{domain})
// but fail signature, ownership, or blocklist verification.
//
// commits is the slice of commit raw bytes (caller responsibility:
// git-walk between old and new ref tips, supply the new commits).
//
// Returns nil if all commits pass (or the feature is disabled).
// Returns error with a clear rejection reason on any failure.
func VerifyAgentCommits(
    ctx context.Context,
    commits []CommitToVerify,
    svc *cairnidentity.AgentService,
    enforce bool,
) error

type CommitToVerify struct {
    SHA       string
    AuthorName  string
    AuthorEmail string
    Message     string
    Raw         []byte // full commit object bytes
}
```

**Behaviour per commit:**
1. If `enforce == false`: log "would-have-checked" and return nil (migration mode)
2. Parse author email via `cairnidentity.ParseAgentEmail`. Not an agent email → skip (vanilla Forgejo handles)
3. Look up agent: `svc.GetByEmail(slug, domain)` → not found → REJECT (orphan agent)
4. Check `agent.Status == active` → otherwise REJECT (pending or unknown)
5. Check `svc.IsBlocked(agent.Fingerprint)` → blocked → REJECT
6. Extract signature via `ExtractSignedCommitData(commit.Raw)` → no signature → REJECT (signed_required)
7. Verify signature via `VerifyAgentSignature(commit.Raw, agent.PublicKey)` → invalid → REJECT
8. Cross-validate trailers via `cairnidentity.VerifyTrailers(commit.Message, agent, ownerUsername)` → mismatch → REJECT
9. Pass

**Tests** (use httptest-free in-memory setup; `AgentService` with fake stores):
- All checks pass → no error
- enforce=false → no error even with bad signatures
- Non-agent commit → skipped
- Orphan agent (slug not registered) → reject
- Pending agent → reject
- Blocked agent → reject
- Missing signature → reject
- Tampered commit → reject
- Trailer mismatch → reject

**Commit message starts:** `feat(cairn): VerifyAgentCommits push-verification logic`

---

## Task 6: Pre-receive hook integration

**Files:**
- Modify: `routers/private/hook_pre_receive.go` (Forgejo upstream patch — small additive)
- Possibly: helper file in `services/cairn/hook/` for the Forgejo binding

**Why:** Wire `VerifyAgentCommits` into Forgejo's existing pre-receive hook. The hook fires before git accepts the push to a ref; we add a Cairn check that inspects the new commits.

**Integration shape** (based on what worked in Plan 2a Task 7 for routes):
- Find Forgejo's `hook_pre_receive.go` — the function that runs per ref. Locate the existing checks (branch protection, push limits, etc.). Add a Cairn check at the end (so vanilla Forgejo behaviour runs first).
- The Cairn check needs: a list of new commits in the push (between old and new SHAs). Forgejo provides this via `git rev-list <old>..<new>`.
- For each commit SHA, fetch the raw commit object via Forgejo's `git.Repository.GetCommit(sha)` and call `cat-file -p` (or use git's plumbing directly).
- Build the `CommitToVerify` slice; pass to `VerifyAgentCommits`.

This task is the most version-dependent — the actual Forgejo APIs for `RevList` / `GetCommit` / commit-raw access differ across versions. Verify before writing.

**Important context:** Cairn's `Init` (Plan 2a Task 7) constructs the global AgentService. The hook needs access to that. Either:
- Export `cairnv1.GlobalHandler()` (already exists) and reach in to `.svc`, OR
- Add `cairnv1.GlobalAgentService()` accessor for clarity, OR
- Move the AgentService into a separate package `services/cairn/identity/global.go` so both the API handler and the hook can use it without circular import

**Decision (for the implementer):** preferred is the third — move the global into the identity package since it's now used by both api/v1 and hook. Keeps each importer pointing at the same struct.

**Commit message starts:** `feat(cairn): pre-receive hook integration with VerifyAgentCommits`

---

## Task 7: Author rendering badge

**Files:**
- Create: `routers/web/cairn/agent_author.go` (helper to detect/render agent authors)
- Modify: One or two Forgejo commit-display templates (find via `grep -rln 'AuthorEmail' templates/repo/`)

**Why:** Spec §4.7 — when Forgejo's web UI renders a commit, recognise `nexus-{slug}@{domain}` author emails and add a "🤖 plumb (under alice)" badge alongside the email. Vanilla Forgejo path retains for non-agent emails.

**Approach:**
- Add a small Go helper `IsAgentAuthor(email) bool` and `AgentAuthorLabel(email) string` (returns "plumb (under alice)" or "") used in templates
- Modify Forgejo's commit-list and commit-detail templates to call the helper conditionally
- This is mostly template work — the existing AuthorEmail rendering stays intact, we just add the badge inline

**Tests:** Go-side helpers tested in `agent_author_test.go`. Template changes verified visually (smoke test against running binary).

**Commit message starts:** `feat(cairn): agent author badge rendering in commit views`

---

## End-of-plan verification

After all 7 tasks:

```bash
go test ./... 2>&1 | grep -v 'no test files' | tail -20
```

Then live integration smoke test:

```bash
# Build, set up local Cairn instance with test data
go build -o /tmp/cairn-plan3-test .
mkdir -p /tmp/cairn-plan3-data
cat > /tmp/cairn-plan3.ini <<EOF
[database]
DB_TYPE = sqlite3
PATH = /tmp/cairn-plan3-data/cairn.db
[repository]
ROOT = /tmp/cairn-plan3-data/repos
[server]
APP_DATA_PATH = /tmp/cairn-plan3-data
DOMAIN = localhost
HTTP_PORT = 3000
[security]
INSTALL_LOCK = true
[cairn]
hmac_key_path = /tmp/cairn-plan3-data/instance-hmac.key
enforce_signatures = true
EOF

/tmp/cairn-plan3-test migrate --config /tmp/cairn-plan3.ini
/tmp/cairn-plan3-test admin user create --config /tmp/cairn-plan3.ini \
    --username alice --email nexus@darksoft.co.nz --password testpassword --admin
/tmp/cairn-plan3-test web --config /tmp/cairn-plan3.ini &
sleep 3

# Set up agent identity
TEST_HOME=$(mktemp -d) ; export XDG_CONFIG_HOME=$TEST_HOME
mkdir -p $TEST_HOME/cairn ; head -c 32 /dev/urandom > $TEST_HOME/cairn/seed ; chmod 0600 $TEST_HOME/cairn/seed

CAIRN_PASSWORD=testpassword /tmp/cairn-plan3-test cairn auth login \
    --instance http://localhost:3000 --username alice
/tmp/cairn-plan3-test cairn agent init \
    --instance http://localhost:3000 --slug plumb --domain darksoft.co.nz
/tmp/cairn-plan3-test cairn agent submit \
    --instance http://localhost:3000 --owner alice --slug plumb --domain darksoft.co.nz

# Configure git to sign with cairn helper
git config --global gpg.format ssh
git config --global gpg.ssh.program "/tmp/cairn-plan3-test cairn commit-sign-helper -instance http://localhost:3000 -slug plumb"

# Make a test repo, configure as plumb, push
# ... commit, push, observe accept

# Now block plumb and try again — should reject
/tmp/cairn-plan3-test cairn agents block <fingerprint> --reason "test block" \
    --instance http://localhost:3000
# git push — should fail with the rejection reason

pkill -f cairn-plan3-test
rm -rf /tmp/cairn-plan3-* $TEST_HOME
```

Expected: signed agent commits accepted; orphan/blocked/tampered commits rejected with clear messages.
