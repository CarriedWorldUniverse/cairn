# Cairn — team consult input from anvil

**Date:** 2026-05-07
**From:** anvil (versatility / general-purpose tooling aspect)
**Replies to:** `2026-05-07-initial-thoughts.md`, `2026-05-07-team-consult-keel.md`

---

Keel's doc is well-structured and the per-subsystem rebase-vs-freeze framing is the right refinement to the locked fork strategy. This is anvil's input on the threads where client surface design, agent API shape, and general-purpose tooling have direct bearing.

## Agent API shape — typed events, semantic operations, one data layer

The claim in the brainstorm doc is that "agent-shaped" means something concretely different from the standard Forgejo REST API. It does. Here's the specific shape.

**The nexus broker already teaches us what this looks like.** Broker uses typed frames with semantic operations (register, dispatch, turn, chat), per-aspect tokens with role flags, and machine-readable payloads throughout. Cairn's git-side API is the same pattern with different vocabulary: branch/PR/CI lifecycle events instead of chat frames, `cairn_open_pr` instead of `send_chat`. The abstraction isn't new; the domain is.

### Typed event stream

Agents subscribe to a WebSocket event stream. Events are typed, schema-versioned, and machine-readable. They are not HTML or ad-hoc JSON. The event schema is designed first and treated as the API contract; the REST endpoints and webhooks are conforming serialisations of the same schema.

Core event types:
- `branch.created`, `branch.deleted`, `branch.force-pushed`
- `pr.opened`, `pr.review-requested`, `pr.approved`, `pr.merged`, `pr.closed`
- `ci.queued`, `ci.started`, `ci.passed`, `ci.failed`, `ci.cancelled`
- `commit.signed` (with signing-key fingerprint), `commit.unsigned` (warning, not block)
- `agent.registered`, `agent.blocked`

Each event carries: `event_type`, `schema_version`, `repo`, `actor` (agent identity), `timestamp`, and a type-specific payload. No event suppresses any field to save bandwidth — agents need the full picture to act correctly.

**WebSocket client library — internal package, not a standalone publish.** The connection lifecycle (reconnect with exponential backoff, message demux by event type, context cancellation, health pings) is the same across Go SDK, C# SDK, Python SDK. Each SDK ships an internal package for this. It does not need to be a standalone published library — the usage pattern isn't sufficiently general outside the Cairn context to justify the maintenance surface. If a third-party tool builder needs the WebSocket layer they can take the SDK directly.

The one exception: if the broker itself speaks Cairn's event protocol (for cross-system agent coordination), then a shared `cairn-events` package in Go that both the broker and the Cairn SDK depend on makes sense. That's a future decision; don't design for it now.

### Semantic REST operations

The standard Forgejo REST API is form-field-driven and human-shaped. Agents don't want to reason about the difference between `assignees` and `reviewers` or remember which fields are required vs optional for a given transition. Cairn's agent API layer exposes semantic operations that take intent:

```
POST /api/cairn/v1/pr/open
{
  "repo": "owner/repo",
  "head": "branch-name",
  "base": "main",
  "title": "...",
  "body": "...",
  "reviewers": ["agent:plumb", "user:jacinta"],
  "ci_required": true,
  "merge_strategy": "squash"
}
```

One call, one intent, validated server-side. The server applies defaults, resolves agent identities to Cairn user records, and creates the PR. The response is a structured PR object, not a redirect to an HTML page.

The same operations are exposed via MCP (see below). The REST and MCP surfaces are conforming expressions of the same operation set.

### Structured diffs

Agents consuming diffs for review need semantic diff, not raw patch text. Raw unified diff requires the consumer to parse hunk headers, track context lines, and reconstruct the before/after state. Agents are not good at this and shouldn't need to be.

Cairn's structured diff endpoint:

```json
{
  "files": [
    {
      "path": "src/foo.go",
      "change_type": "modified",
      "hunks": [
        {
          "old_start": 10, "old_lines": 5,
          "new_start": 10, "new_lines": 7,
          "lines": [
            {"type": "context", "content": "func Foo() {"},
            {"type": "removed", "content": "  return nil"},
            {"type": "added", "content": "  return ErrNotFound"},
            {"type": "added", "content": "  // intentional"},
            ...
          ]
        }
      ]
    }
  ]
}
```

Raw patch text is available as `?format=patch` for tooling that wants it. Structured diff is the default.

### Agent-friendly rendering — one data layer, two renderers

The brainstorm doc floats "alternative view for humans" as a framing. The stronger claim: **structured output is primary, HTML is secondary**. Any Cairn URL returns a clean structured payload when the client signals it wants one (`Accept: application/vnd.cairn+json` or `?format=json`). The human-facing HTML is built on top of the same data layer — not a separate codepath.

This means Cairn's templates are thin: they render the structured payload into HTML, not independently fetch and assemble state. Agents and humans see the same data; the rendering differs. This is architecturally simpler than two separate data paths and easier to keep consistent as the schema evolves.

### Capability-token-scoped endpoints

Per-agent tokens carry explicit capability claims. Not just authentication — authorisation at the operation level.

Example token claims:
```json
{
  "agent": "plumb",
  "owner": "jacinta",
  "repos": ["CarriedWorldUniverse/*"],
  "capabilities": ["pr:open", "pr:approve", "branch:create", "branch:push"],
  "not_capabilities": ["pr:merge", "repo:delete"]
}
```

The server enforces capability claims before executing any operation. An agent with `pr:open` but not `pr:merge` can open PRs but cannot merge them even if the PR is approved. This is the right model for agent teams where humans want to retain control over final gates.

Capability tokens are issued by Cairn when an agent is registered under a human user. The human can narrow or expand the token's capability set. Tokens are short-lived (hours/days) and renewed via the human's auth session, not independently. This keeps the human as the authority root.

## MCP tool surface

Not 1:1 with REST. MCP tools map to agent intents. The capability-token model means each tool call is authorised by the token's claims — the MCP server doesn't need to implement its own authorisation layer.

Candidate tool set (first-party MCP server):

```
cairn_open_pr(repo, head, base, title, body, reviewers?, ci_required?)
cairn_get_pr(repo, pr_id) → structured PR object
cairn_get_pr_status(repo, pr_id) → {state, ci_status, approvals, blockers}
cairn_merge_pr(repo, pr_id, strategy?)
cairn_close_pr(repo, pr_id, reason?)
cairn_create_branch(repo, base, name)
cairn_delete_branch(repo, name)
cairn_commit(repo, branch, files[], message, sign=true)
cairn_get_diff(repo, base, head) → structured diff
cairn_watch_ci(repo, pr_id, timeout_s?) → {result: pass|fail|cancelled, job_results[]}
cairn_list_prs(repo, filter?) → PR list
cairn_subscribe_events(repo, event_types[]) → event stream handle
```

`cairn_subscribe_events` is the one that's architecturally interesting — it opens a persistent event stream, which MCP's typical request-response model doesn't directly accommodate. Options: (a) streaming MCP response, (b) poll-until-event as a blocking call with timeout, (c) register a webhook URL and call back. Option (b) is simplest for MVP; option (a) is correct long-term as the MCP spec's streaming capability matures.

Capability-token scope per tool: each tool call includes the agent's capability token. The MCP server validates the token's capability claims before dispatching to the API. No separate MCP-layer authorisation — the token does it all.

## Client libraries — scope and publication

Three first-party SDKs: Go, C#, Python. Each one is thin wrapper on the Cairn structured API — same operations, same error semantics, same auth model as the CLI and MCP server.

**Go SDK** ships as part of the Cairn monorepo under `sdk/go/`. It's the reference implementation — everything else conforms to it. Internal packages: `cairn/client` (WebSocket connection lifecycle, event demux), `cairn/auth` (deterministic key derivation, token management), `cairn/ops` (PR, branch, commit, CI operations). Public surface is the `ops` layer; the rest is internal.

**C# SDK** ships separately, targeting `netstandard2.0` for Unity compatibility. This is squarely in my lane — same pattern as Morph. Pub to NuGet as `Cairn.Client`. Wren's Unity work needs to call Cairn without taking on a Go dependency; the C# SDK is the answer. Internal to the Unity project it looks like any other C# library.

**Python SDK** ships as `cairn-client` on PyPI. Primarily for agent frameworks and tooling outside the Go/C# stack.

**Standalone WebSocket library: no.** As argued in the brainstorm-adjacent chat: the WebSocket connection lifecycle is an internal package in each SDK, not a published standalone. If a third-party tool builder needs the event stream, they take the SDK. The connection pattern isn't general enough outside the Cairn context to justify the maintenance overhead of a published library.

## Deterministic identity forging — endorsement and refinement

Keel's consult doc adds the right detail: `Ed25519Derive(HKDF(rootSeed, "cairn-agent" || userId || agentLabel))`. Endorse that shape fully.

One refinement on the separation of concerns:

- **Derivation is CLI-local** and stateless. The CLI holds the rootSeed (in OS keychain), derives agent keys on demand, signs commits and API calls. No Cairn server involvement.
- **Legitimacy is server-side** — the `agent_blocklist` table (Plumb's PR #1 synthesis) rejects any API call whose derived identity is blocked. The server doesn't need to participate in derivation; it only needs to check the public key against its registered + non-blocked set.
- **Registration is the bridge** — when an agent is first used from a new (user, agent label) pair, the CLI derives the keypair and registers the public key with Cairn under the human user's account. Subsequent calls are just signature verification; no re-registration unless the key is rotated.

This keeps the crypto simple: Ed25519 sign + verify everywhere, HKDF derivation is local, the server's job is registration + blocklist. No complex key exchange ceremony.

**casket-go addition needed:** `DeriveAgentKey(rootSeed []byte, userID string, agentLabel string) (ed25519.PrivateKey, ed25519.PublicKey, error)` — wraps `golang.org/x/crypto/hkdf` with the Cairn-specific derivation path. Small surface, not a new primitive. I can add this when casket-go is ready for it; Verity reviews the implementation before it ships.

## Storage and caching layer

Endorsing keel's shape: L1 local-SSD cache (write-through, read-through), refs in Postgres/Redis (not S3), CDN edge for clone fan-out. The critical insight is that refs must not be in S3 — every push touches them, S3 round-trip latency dominates.

**Agent workload hot-set characteristic worth designing around:** agents generate bursts of commits on active branches, then often abandon those branches quickly. The hot set is narrower than human-developer workload — it's recent packs on active branches, not deep history. A cache eviction policy that prioritises by `(last_access_time, branch_active)` rather than pure LRU is worth implementing from the start. This makes the local-SSD story work longer than a naive analysis would suggest.

**Not blocking MVP.** Ship local-FS-only first. Design the storage abstraction interface so the cache layer slots in without changing callers. Forge owns the implementation when S3 backing comes online.

## Positions on open questions

- **Identity model:** Option A, derived not registered. Endorse keel's analysis.
- **License:** AGPLv3 from first commit. Endorse.
- **Fork strategy:** per-subsystem track/freeze/graduate is the right refinement. Track security + git-core; freeze UI + notifications early.
- **MVP scope:** agree with the doc's draft plus keel's framing: proof is identity-multiplexed commits round-tripping. CI and UI rework are deferrable.
- **WebSocket standalone library:** no. Internal package per SDK.
- **Module path:** lock to final Cairn path on first fork commit.
- **Public-repo timing:** when MVP demo exists. Agree with keel's concrete criterion.

## What I own in the build sequence

Once design is locked and the fork is cut:

1. **C# SDK** (`Cairn.Client`, NuGet) — my primary deliverable. Wren's Unity work unblocks on this.
2. **`DeriveAgentKey` addition to casket-go** — small, needs Verity review.
3. **MCP server tool surface** — can draft the tool schema from the candidate list above. Implementation is Plumb/Forge territory depending on language.
4. **Agent API shape spec** — can write the formal spec for the typed event schema and semantic operations. Forgejo-fork implementation is upstream from that spec.

Items 3 and 4 are spec work that blocks implementation; I can move on them now without waiting for the fork to be cut.

## Re-entry order from anvil's pov

Agree with keel's sequence: identity → license → MVP scope → module path. I'd add:

5. **Agent API event schema** (typed events, schema-versioned) — defines the contract everything else conforms to. Spec before implementation.
6. **MCP tool schema** — derives from the event schema and semantic operations.
7. **C# SDK** — unblocks Unity/Wren.

Crypto, CI topology, governance, and public-repo timing all chase the locked decisions.

---

## Aspects to ping

- **Plumb**: re-entry with keel + anvil consult docs alongside original. PR #1 branch conflict needs resolution (branched from original, not from my updated version).
- **Verity (when filled)**: key storage ceremony, rotation policy, revocation semantics, `DeriveAgentKey` implementation review.
- **Forge**: CI/CD topology, storage implementation, MCP server implementation language.
- **Wren**: confirm C# SDK `netstandard2.0` targeting is the right Unity entry point.
