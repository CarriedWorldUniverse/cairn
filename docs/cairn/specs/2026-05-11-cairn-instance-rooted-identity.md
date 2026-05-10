# Cairn — Instance-Rooted Identity Refactor

**Date:** 2026-05-11
**Status:** approved (designed 2026-05-11 via Plumb/Jacinta back-and-forth; implementation green-lit)
**Supersedes (in part):** [`2026-05-09-cairn-foundation-design.md`](2026-05-09-cairn-foundation-design.md) §"Identity model" — the HKDF-from-operator-seed derivation path is replaced; the rest of the foundation (fingerprint scheme, signature verification, trailer enforcement, owner-cluster semantics, push hook) is preserved.

---

## Problem with the seed-derivation model

Plan 1 designed the agent identity as `HKDF(operator_seed, agent_slug) → Ed25519 keypair`. The properties this gave us:

- Operator can re-derive any lost agent key deterministically
- Cairn never sees the seed (only pubkeys + fingerprints)
- Single source of truth for all agents of one owner

The properties we didn't account for and which bite as soon as agents run on more than one host:

1. **N hosts running agents = N copies of the seed on disk.** Each copy is full root-of-trust material.
2. **Compromise blast radius is everything.** Breach any one host → exfiltrate seed → adversary now controls every agent across every host. There's no "compromise contained to laptop" outcome.
3. **No per-host revocation.** Lose <server-host>? You can't revoke just <server-host>'s agents — they share the seed with <operator-host>, EC2, anywhere else. The only revocation is "rotate the seed" = "rotate every agent."
4. **No per-agent revocation either.** Same reason. Killing `plumb` requires touching all agents.
5. **Onboarding new hosts means distributing root-of-trust material over the network.** The thing we tell people not to do with master keys.

The seed-derivation model trades cheap recovery for expensive everything-else. For Cairn's actual use case (multi-host agent fleet, sovereign-substrate operator, per-agent revocation matters), the trade is wrong.

## The refactored model

> **Root of trust = the Cairn instance + the Cairn human user account. Agents bring their own keypairs.**

Three anchors:

1. **The Cairn instance** owns the `instance-hmac.key` already provisioned at deploy time. This is what turns a pubkey into a unique-in-this-instance fingerprint (`cairn:<HMAC-SHA256(pubkey, instance_hmac)>`). The instance vouches for what a pubkey means *on this instance*; it does NOT vouch for the pubkey's origin.

2. **The Cairn human user** is a normal Forgejo user authenticated by normal Forgejo means (password + optional MFA + API tokens). This is who *owns* agents. Owner-cluster, approval authority, review-policy filtering — all anchored here.

3. **The agent's keypair** is generated locally on whichever host the agent runs, by whatever means (`ssh-keygen -t ed25519` is the canonical recipe). The private key stays on that host. The pubkey is registered against the agent's Forgejo user account using Forgejo's *existing* SSH-key management.

What goes away: no seed file, no HKDF derivation, no shared root secret between operator and Cairn server.

What stays: fingerprint scheme, signature verification, trailer enforcement, owner-cluster review-policy, push-hook gating, agent-author UI badges.

## One key, three purposes

The agent's Ed25519 keypair simultaneously serves:

| Purpose | Mechanism | Existing or new |
|---|---|---|
| Git push over SSH | Forgejo's existing SSH-server authenticates the client by `ssh_key` table | Existing |
| Commit signature verification | Forgejo's SSH SIGNATURE verifier matches commit's pubkey against the user's `ssh_key` entries | Existing |
| Cairn agent identity | Pre-receive hook computes the pubkey's fingerprint, looks up `cairn_agent` metadata (owner, slug, status) | Cairn-side, refactored to lookup-by-pubkey rather than embedded-pubkey |

Operationally: one `ssh-keygen` invocation. One registration. No key proliferation per purpose.

## Schema refactor

Today's `cairn_agent` table embeds the pubkey + fingerprint as columns. After refactor, pubkeys live in Forgejo's existing `public_key` (or whatever ssh-key) table, owned by the agent's Forgejo user. Cairn's table becomes pure agent metadata.

**Before** (Plan 1's V500):

```
cairn_agent {
    id, user_id (agent's forgejo user), owner_id, slug, domain,
    pubkey, fingerprint,    ← these move out
    status, created_unix, updated_unix
}
```

**After**:

```
cairn_agent {
    id, user_id (agent's forgejo user), owner_id, slug, domain,
    status, created_unix, updated_unix
    -- pubkey + fingerprint columns dropped
}

-- Pubkeys live in Forgejo's existing public_key table, owned by user_id.
-- The signature verifier looks up the pubkey there.
-- The fingerprint is computed on demand from public_key.content.

cairn_attachment_request {           -- NEW
    id, owner_username, slug, domain, pubkey_content,
    status (pending | approved | rejected),
    requested_unix, decided_unix, decided_by_user_id
}
```

The attachment-request table holds the half-step between "agent wants to attach" and "owner approves." Once approved, the request creates/finds the agent's Forgejo user, inserts the pubkey into Forgejo's `public_key`, creates the `cairn_agent` row. The request itself stays as audit history (`status` flipped to `approved`).

## Attachment-request flow

```
1. On the agent's host:
   ssh-keygen -t ed25519 -C "plumb@<host>" -f ~/.cairn/plumb.key
   (or use any existing key)

2. Agent → Cairn:
   POST /api/cairn/v1/agents/attachment-requests
   Body: { owner: "nexus", slug: "plumb",
           domain: "darksoft.co.nz",
           pubkey: "ssh-ed25519 AAAA..." }
   Auth: anonymous (no token), OR authenticated as the owner.
   Server creates a cairn_attachment_request row, status=pending.

3. Human (nexus) checks pending requests:
   GET /api/cairn/v1/users/me/pending-attachment-requests
   (or via web UI: account settings → "Pending agent attachments")

4. Human approves:
   POST /api/cairn/v1/agents/attachment-requests/{id}/approve
   Server:
     a. Find-or-create the agent's Forgejo user (email nexus-<slug>@<domain>)
     b. Insert pubkey into Forgejo's public_key table for that user
     c. Find-or-create cairn_agent row (slug + owner_id; multiple
        approved requests for the same slug = same agent, multiple keys)
     d. Mark attachment_request as approved
     e. Compute + return the fingerprint cairn:<HMAC-of-pubkey>

5. Agent signs commits using the on-disk private key:
   git config gpg.format ssh
   git config user.signingkey ~/.cairn/plumb.key.pub
   git config commit.gpgsign true
   (or wrap via cairn commit-sign-helper if trailer-injection
   helper is wanted)
   Commit message gets trailers:
     Agent-Id:     cairn:<fingerprint-of-this-pubkey>
     Agent-Owner:  nexus
     Agent-Domain: darksoft.co.nz

6. git push origin → pre-receive hook:
   a. Parse trailers
   b. Lookup pubkey by fingerprint (HMAC of stored public_key.content)
   c. Verify SSH SIGNATURE against that pubkey
   d. Check cairn_agent.status != BLOCKED, owner matches Agent-Owner
   e. Accept / reject

Auth-cluster semantics from Plan 6 are preserved: agent approvals are
filtered from required-review gates, owner-cluster self-approval is
blocked, default branch-protection auto-apply still fires.
```

## Multi-host = multi-pubkey

`plumb` running on <operator-host> AND on <server-host> = one `cairn_agent` row (slug=plumb, owner_id=nexus), TWO public_key rows under plumb's Forgejo user. Either pubkey signs commits as plumb. Each pubkey has its own fingerprint. Revoke one (delete that public_key row) → that one host loses agent signing; the other keeps working.

This is exactly how GitHub treats per-machine SSH keys for a single user account, with the Cairn-specific layer being just the agent-vs-human attribution and the owner-cluster relationship.

## What stays unchanged

- **Fingerprint scheme**: `cairn:<base64(HMAC-SHA256(pubkey_content, instance_hmac_key))>`. Per-instance. Stable.
- **Signature verification**: pre-receive hook still parses commit gpgsig, verifies against the pubkey identified by the fingerprint trailer. Implementation moves the lookup from `cairn_agent.pubkey` → `public_key.content WHERE owner_id IN (select user_id from cairn_agent WHERE id = ?)`. Same crypto, different join.
- **Trailer enforcement**: `Agent-Id`, `Agent-Owner`, `Agent-Domain` required when `enforce_signatures = true`. Same.
- **Orphan prevention**: agent must have an owner. Same.
- **Review policy** (Plan 6): agent approvals filtered, owner-cluster self-approval blocked. Same — the underlying "is this user an agent?" check just becomes "is this user listed in cairn_agent?" instead of "does this user's email match `nexus-*@*`?" (the latter was already brittle).

## What's deleted

- The HKDF-derivation code in `casket-go` (`DeriveAgentKey`). The function stays in `casket-go` itself (other projects may rely on it), but Cairn no longer uses it.
- The seed-file convention (`$XDG_CONFIG_HOME/cairn/seed`). The CLI no longer reads it.
- `cairn agent init` (the local-keypair-derive command). Replaced by `cairn agent attach` (which posts an existing pubkey to Cairn as an attachment request).
- The submit-with-auto-approve fast path. The new flow has only one shape: request → approve. The owner can approve their own requests, but the row goes through the same approval API rather than skipping it.
- `commit-sign-helper`'s seed-rederivation behavior. The helper now reads an on-disk private key (or git uses `ssh-keygen -Y sign` directly without our helper).

## What's renamed / changed in code

| Today | After |
|---|---|
| `cairn agent init --slug X --domain Y` | `ssh-keygen -t ed25519` (off-Cairn-CLI; vanilla) |
| `cairn agent submit --owner X --slug Y --domain Z [--anonymous]` | `cairn agent attach --owner X --slug Y --domain Z --pubkey FILE` |
| `cairn agents approve --slug X` | `cairn agents approve --request-id X` (operates on the attachment-request row) |
| Plan 1's `DeriveAgentKey` call | Gone |
| `cairn_agent.pubkey` column | Dropped (migration V503) |
| `cairn_agent.fingerprint` column | Dropped (computed on demand) |
| Lookup: `WHERE cairn_agent.fingerprint = ?` | `WHERE compute_fingerprint(public_key.content) = ?` (or store the computed fingerprint as an index on `public_key`; see Open Question Q1) |

## Migration story for existing deployments

Today's production EC2 (the only running Cairn instance) has the V500 / V501 / V502 tables but **no rows in `cairn_agent` yet** — agent registration was about to happen and didn't. So the migration from the existing state is trivially the same as a fresh install: drop the old columns, add the new table, write the new code paths.

If at some future date there were existing seed-derived agents in production, the migration would have to:

1. Read each `cairn_agent` row's pubkey + fingerprint
2. Insert the pubkey into the corresponding agent user's `public_key` table (creating the agent user if needed)
3. Drop the `pubkey` + `fingerprint` columns from `cairn_agent`

For our state, that whole migration path is "no-op" — the columns drop, nothing else needs moving.

## Operator UX

```bash
# 1. On any host where you want an agent to live:
ssh-keygen -t ed25519 -C "plumb@<this-host>" -f ~/.cairn/plumb.key

# 2. Request attachment to Cairn (one-shot CLI helper):
cairn agent attach \
    --instance https://nexus-cw-ec2.<tailnet>.ts.net \
    --owner nexus \
    --slug plumb \
    --domain darksoft.co.nz \
    --pubkey ~/.cairn/plumb.key.pub

# 3. The owner (human `nexus`) approves via the web UI
#    (Settings → Pending agent attachments → Approve)
#    or via API:
curl -X POST https://nexus-cw-ec2.../api/cairn/v1/agents/attachment-requests/{id}/approve \
     -H "Authorization: token <nexus-token>"

# 4. Configure git on the agent host:
git config gpg.format ssh
git config user.signingkey ~/.cairn/plumb.key.pub
git config commit.gpgsign true
git config core.sshCommand "ssh -i ~/.cairn/plumb.key"

# 5. Push + sign as plumb:
git commit -m "feat: agent commit"
git push origin
```

No seed. No HKDF. No file shuffling between hosts. New host = new ssh-keygen + new attach request.

## Open questions

### Q1: Where does the fingerprint get computed / cached?

Three options:

**a.** **Compute on every signature verification**: fingerprint = HMAC(pubkey, instance_hmac). Cheap (~µs per call). No storage. Simplest.

**b.** **Cache as a column on `public_key`**: add `cairn_fingerprint` column to Forgejo's `public_key` table. Lookup-by-fingerprint becomes a direct WHERE on an indexed column. Faster, but adds a Cairn-specific column to a Forgejo table (small upstream touch).

**c.** **Materialise in a Cairn-side join table**: `cairn_agent_pubkey { agent_id, public_key_id, fingerprint }`. Pure Cairn-side, no Forgejo schema change. Adds one indirection.

Recommendation: **(c)**. Avoids touching Forgejo's schema (smaller upstream patch surface, better rebase story); the indirection is a single indexed join.

### Q2: Where does the attachment-request UI live?

The pending-attachment list could be:
- A new tab in the user's account settings (Forgejo template override, follows the SSH-keys settings page pattern)
- An admin-only panel (under site admin)
- Inline notification on the user's dashboard

Recommendation: **all three**. The user's account-settings tab is the primary surface. Admin sees all pending across the instance. Dashboard notification when something's waiting.

### Q3: Anonymous attachment request — auth requirement?

The flow says "anonymous, no auth token required." This is the request-then-approve pattern. But it lets anyone create pending requests against any owner. Cleanup story: requests time out (e.g., 30 days) and auto-expire. The owner sees + manually rejects spam. The volume is low (this is personal-substrate).

For multi-tenant Cairn (deferred), the request rate could need rate-limiting per source IP or some captcha-equivalent. For MVP: trust the LAN/tailnet, expire stale pending requests.

### Q4: Should an agent be able to authenticate the request with its OWN newly-generated key?

I.e., agent's first interaction is a request signed by the very pubkey it's asking to attach. That proves "this request comes from someone who controls this private key." Spam-filter benefit. Cost: more complex CLI on the agent side, but `cairn agent attach` could do it transparently.

Recommendation: **defer**. The anonymous-but-pending-then-human-approves is enough for MVP. Self-signed attachment requests are a v1.1 hardening.

### Q5: Backwards compat — keep `cairn agent init` as a deprecated alias?

For anyone with existing scripts referencing the seed-derivation flow.

Recommendation: **no**, ship clean break. Today's MVP has zero deployed agents using it. The CLI doc points operators at `cairn agent attach`.

## Cross-references

- [`2026-05-09-cairn-foundation-design.md`](2026-05-09-cairn-foundation-design.md) — original spec; identity-model section is superseded by this doc
- [`2026-05-10-cairn-ai-native-amendment.md`](2026-05-10-cairn-ai-native-amendment.md) — AI-native amendment; review-policy semantics from §4 are unchanged here
- [`2026-05-10-cairn-local-ai-simplifier-option.md`](2026-05-10-cairn-local-ai-simplifier-option.md) — orthogonal; no interaction
- Memory: `project_cairn_deployment_ceiling` — personal-substrate ceiling preserved
- `cairn/eval/` — orthogonal

## Implementation plan

See [`docs/cairn/plans/2026-05-11-cairn-instance-rooted-identity-refactor.md`](../plans/2026-05-11-cairn-instance-rooted-identity-refactor.md).
