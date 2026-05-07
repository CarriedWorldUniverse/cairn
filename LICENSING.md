# Licensing

Cairn is a fork of [Forgejo](https://codeberg.org/forgejo/forgejo). It contains code under two licenses, with a clear, file-path-based boundary.

## The boundary

| Path | License | Provenance |
|---|---|---|
| `LICENSE` | GPLv3 | Forgejo's original license; applies to all Forgejo-derived code |
| `LICENSE-cairn` | AGPLv3 | Cairn's license; applies to all Cairn-specific code |

## What is Cairn-specific

Code authored as part of the Cairn project, not derived from Forgejo upstream, is licensed under **AGPLv3** (see `LICENSE-cairn`). This includes any file under:

- `models/cairn/**`
- `services/cairn/**`
- `routers/api/cairn/**`
- `routers/web/cairn/**`
- `templates/cairn/**`
- `cmd/cairn/**` and `cmd/cairn-*/**`
- `sdk/**`
- `docs/cairn/**`, `docs/brainstorm/**`
- `cairn/**` (top-level cairn-specific tooling, patches, scripts)

## What is Forgejo-derived

Everything else in this repository is derived from Forgejo and remains under **GPLv3** (see `LICENSE`). Modifications to Forgejo-derived files inherit Forgejo's GPLv3, not Cairn's AGPLv3, even when authored by Cairn contributors. This is the legal seam that lets us rebase from upstream Forgejo without re-licensing their code.

## Why AGPL for Cairn-specific code

Cairn is agent-platform infrastructure. The most likely competitive scenario is someone running a hosted Cairn-based service. AGPL closes the SaaS loophole: anyone running modified Cairn as a network service must release their modifications. GPLv3 — Forgejo's choice — does not.

Forgejo's GPLv3 is correct for Forgejo's threat model (most users self-host); AGPL is the deliberate divergence for Cairn's threat model.

## File headers

New Cairn-specific files SHOULD carry an SPDX header:

```go
// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright the Cairn contributors. See LICENSE-cairn for license text.
```

Forgejo-derived files retain their existing headers. Modifications to Forgejo-derived files do NOT add a Cairn header — they remain GPLv3 under Forgejo's license.

## When in doubt

If a file modifies Forgejo-derived code AND adds substantial Cairn-specific logic, prefer **factoring the Cairn-specific logic into a new file under `cairn/` or `*/cairn/`** rather than mixing licenses in a single file. This keeps the boundary clean and makes the eventual graduation off Forgejo (a port, not a rewrite) tractable.
