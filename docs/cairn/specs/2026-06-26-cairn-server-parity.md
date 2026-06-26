# cairn server → convergence-core parity

**Date:** 2026-06-26
**Status:** roadmap + Slice 0 landed. Destination chosen by the owner: the **privacy/embargo enforcement point**.

## Architecture reality (why this is needed)

The running cairn server is the **older git-hosting model** (`internal/repo` — orgs, mTLS,
herald, the CWB era). `internal/change` (the entire convergence engine that powers the CLI) is
imported by **zero** server code paths: `cmd/cairn-server/main.go` opens `repo.Open(...)`, and the
`ChangeService` gRPC facade was never registered (dead code). The pre-receive hook enforces only
"no force-push / delete on the default branch" — it knows nothing of lines, changes, or conflicts.

**The server is a faithful but *blind* git locker.** Notably, **full cairn↔cairn fidelity already
works over it** by accident: because branch protection only inspects the default ref, a client's
`refs/cairn/meta` and `refs/cairn/*` push through and are stored verbatim, and the *client* engine
already does meta export on push + full graph reconstruction on clone. So the server is
convergence-*carrying*, just not convergence-*aware*.

## What "parity with the CLI" means (and doesn't)

It does **not** mean RPC-ing the ~40 CLI subcommands. Merge-forward, undo, bisect, reauthor,
history-edit, blame are deterministic and **local-first by design** (spec §1.11) — RPC-ing them
yields a bad thin-client and throws away local-first. Parity = hosting the **three things only a
server can do**:

1. **Convergence-fidelity hosting hub** — host cairn repos so clone/push/pull preserve full
   line/change/conflict/meta fidelity, *and the server understands it* enough to inspect state and
   enforce convergence-correct protection (mostly true today by accident → make it intentional).
2. **The server-only privacy tier** (spec §6) — emit a **redacted materialized object graph** to
   public clones (private bytes never enter the public packfile), hold real blobs in a
   **herald-scoped private store**, **per-identity read-gate** it, track **commit-level embargo**,
   and implement **`Disclose`**. *This is the reason a cairn server exists* (the NEX-25 patch-gap)
   and exists nowhere yet. **← chosen destination.**
3. **Multi-agent live hub** — server-side merge-forward on receive + express/lease arbitration.
   The eventual prize; highest risk (server-side write-convergence + concurrency). Last.

## Roadmap (privacy-point destination)

Every convergence-aware behavior needs the server to *read the change-graph*, so the engine-embed
is the shared first step regardless of destination.

- **Slice 0 — embed the engine, read-only (DONE).** `change.OpenBare(bareDir)` opens an engine
  whose git store IS the hosted bare repo, with an **in-memory** catalogue reconstructed from
  `refs/cairn/meta` (`LoadFromMeta`) — no server-side `.cairn/cairn.db`. `repo.Service.ChangeGraph`
  reads the hosted line tree + open-conflict count. Zero behavior change to clone/push/pull.
- **Slice 1 — convergence-aware introspection API.** A *registered* read-only gRPC `ChangeService`
  (`GetLineTree`/`Conflicts`/`Log`/`Show`) over the embedded engine, so a client/UI/ledger sees
  change-graph state without a full clone.
- **Slice 2 — convergence-correct protection.** Pre-receive validates `refs/cairn/meta` consistency
  and change-sealed semantics, not just git fast-forward.
- **Slice 4 — the embargo tier.** *Decision (2026-06-26): the server tier is **embargo**, not
  "secrets on the server."* Two distinct concepts: a `private` path is a **secret** — never pushed,
  even to a cairn server (unchanged); an **embargoed commit** is content you DO distribute, just
  **gated and not-yet-public** (the patch-gap). The real embargoed bytes live on a trusted,
  herald-gated cairn server in **plaintext** (gated by identity like GitHub holds private repos —
  no client-side keys, since encryption was rejected for key-custody reasons).
  - **4a — client embargo model (DONE).** `cairn embargo <commit>` / `embargo ls` /
    `disclose <commit>`; `embargo` table; flags travel in `refs/cairn/meta` (so the server can read
    them via Slice 0's engine); push to a **git** remote freezes the public tip at the embargo
    boundary (`PublicTip`, composed before redaction in `embargoCapForPush`). An embargo push to a
    **cairn** remote is refused (4b not built — avoids leaking via `refs/cairn/*`).
  - **4b — server private store + gated serve.** *Architecture decision (2026-06-26): two bares per
    repo (`<id>.git` public/frozen + `<id>.embargo.git` real), so per-identity content split becomes
    per-identity **bare selection** — the shell-out `git-upload-pack` can't filter one stream's
    refs, but it can be pointed at a different bare.* Recipient ACL owned by **cairn**
    (`embargo_recipient` table keyed by herald agent-id; herald scopes are too coarse). Receive
    model: **receive-then-relocate** (one atomic push → public bare → a post-receive step moves
    `refs/cairn/embargo/*` into the embargo bare; the embargo bare is self-sufficient — the
    relocation fetch copies the full reachable set, so public can gc the dangling bytes; base
    duplication is a later space optimization).
    - **4b-1 — server substrate (DONE).** `repo.Service.RelocateEmbargoRefs` + lazy
      `EmbargoStoragePath`/`ensureEmbargoBare`; post-receive hook (`cmd/cairn-server`) calls it.
      After a push, embargoed refs are segregated into the embargo bare and no public ref reaches
      them (provably frozen). Client still refuses embargo→cairn (no leak until 4b-2).
    - **4b-2 — client dual-push (DONE).** The refusal is replaced by a dual projection: the public
      side gets capped `refs/heads`/`refs/tags` and **no cairn meta** (a public clone reconstructs
      the frozen **flat** graph — valid, embargo-free; full cairn fidelity returns on disclose),
      while the REAL tips + full `ExportMeta` go to `refs/cairn/embargo/*` (the server relocates
      them via 4b-1). This sidesteps building a capped meta. Verified end-to-end: client push →
      relocation → public bare has no ref reaching the embargoed fix; the embargo bare holds it.
    - **4b-3 — gated serve.** Wire the per-identity gate (recipient → embargo bare, else public
      bare) into the SSH/HTTP serve. Authorized clone gets the real embargoed content.
    - **4b-4 — disclose migration + paired gc.** Prune disclosed embargo refs; gc the bares safely.
- **Slice 5 — per-identity read gating.** A herald-identity → private-store ACL check at the fetch
  boundary decides real bytes vs redacted shape. (cairn owns the path-ACL; herald is the identity
  oracle.)
- **Slice 6 — embargo + `Disclose`.** Commit-level embargo metadata; public tip frozen at the last
  non-embargoed commit; `Disclose` advances it. The patch-gap payoff.
- **Slice 7 (later) — multi-agent live hub.** Server-side merge-forward on receive + lease
  arbitration. Needs a real concurrency/locking story.

## Key decisions

- **Embed `change.Engine`; keep `repo.Service` as the catalogue/auth/routing shell.** The engine is
  a pure local-first library (no server calls, SQLite busy_timeout, on-demand go-git) — safe to
  embed. Reuse from `internal/repo`: org/repo catalogue, herald/X-CWB auth glue, mTLS, hook install.
- **Server catalogue = reconstructed from `refs/cairn/meta`, in memory** — the meta ref is the
  single source of truth; no second on-disk DB to keep coherent under concurrent pushes.
- **Per-identity gating: cairn owns the private-store ACL keyed by herald agent-ID;** herald stays
  the identity oracle, not the path-ACL store. (Confirm before Slice 5.)

## Risks / open

- Concurrency: the engine reading the bare store while `git-receive-pack` writes it. Read-only in
  Slice 0 mitigates; server-side *write* convergence (Slice 7) needs a real locking story.
- Redaction at serve vs at push: moving redaction to the serve boundary changes who holds the
  private blobs (server private store vs never-pushed client). Architectural fork at Slice 4.
- The git-PR model (`repo.Pull`/`FastForward`) is incompatible with lines/changes/sealing; revisit
  (don't rip out early).
