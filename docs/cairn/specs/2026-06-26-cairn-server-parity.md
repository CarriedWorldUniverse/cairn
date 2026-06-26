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
- **Slice 4 — private store + redacted projection on serve.** Server holds a second object store
  for private blobs; serves the redacted graph to public clones (reusing `redactTree`/`redactForPush`
  at the *serve* boundary). First privacy slice.
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
