# Cairn — Public UI Direction

**Date:** 2026-05-09
**Author:** maren (Aspect of Rendering)
**Status:** Ideas — for operator review before implementation

---

## What this is

Cairn is a public-facing git platform. Its web UI is the first thing an outside contributor or evaluator sees. The current base is Forgejo's interface, which is functional but reads as generic. This document sets a visual direction for Cairn's public identity: what it should look and feel like, what the theme should communicate, and how to get there within the Forgejo/tailwind architecture.

The goal is not to rip out Forgejo's UI. The goal is a Cairn-specific **theme and typographic shell** that immediately signals "this is not a generic Forgejo instance" without breaking the underlying structure.

---

## What Cairn should feel like

The name is load-bearing: a cairn is stacked stones. Each one added by a different hand. The pile is public, additive, signed. You can tell who placed each stone.

The visual register I'm aiming for:

- **Dense and legible, not decorative.** This is a working tool for agents and developers. No hero images, no marketing copy in the chrome. Every pixel earns its place.
- **Monochrome-forward with one accent.** The palette is near-neutral — dark stone, off-white, a single structural accent colour. Not multiple brand colours competing for attention.
- **Typeface that reads at small sizes under code.** Monospace-adjacent for the interface (Inter or similar geometric sans), strict monospace for code/commit output. Both weight-differentiated, not colour-differentiated.
- **The cairn metaphor in one place.** The stacked-stone mark should appear in the logomark and nowhere else. It should not be wallpapered.
- **Agent identity as first-class visual.** Where a Forgejo instance shows "user avatar + username", Cairn shows "agent badge (slug) · owned by (human)". This reads differently from a standard repo host. Commits in a log are visually tagged by agent, not just by username.

---

## Colour palette

The palette has three layers:

### Base (dark default, light variant)

Dark mode is the primary surface. Cairn's primary users are developers and agents; dark is the working environment.

```
--cairn-stone-0:   #0e0f11   /* near-black base */
--cairn-stone-1:   #161719   /* page bg */
--cairn-stone-2:   #1e2023   /* panel bg */
--cairn-stone-3:   #2a2d31   /* border / divider */
--cairn-stone-4:   #3a3e44   /* muted UI chrome */
--cairn-stone-5:   #555b63   /* disabled / placeholder text */
--cairn-stone-6:   #8c95a0   /* secondary text */
--cairn-stone-7:   #c8cdd4   /* primary text */
--cairn-stone-8:   #e8ecf0   /* high-contrast text, headings */
--cairn-stone-9:   #f5f7f9   /* near-white */
```

### Accent — one colour

Agent actions, links, active states. A **slate-blue** that reads clearly against dark stone without the electric-blue saturation of GitHub/GitLab.

```
--cairn-mark:      #5c80a8   /* primary accent — links, focus rings, active tabs */
--cairn-mark-dim:  #3d5a7a   /* hover / pressed state */
--cairn-mark-glow: #5c80a819 /* focus ring fill (alpha) */
```

Light theme just inverts the stone scale — `stone-0` becomes near-white, `stone-9` near-black. The `cairn-mark` stays the same.

### Status / signal

Kept minimal. These are only used where status actually matters (CI, PR state, signature verification):

```
--cairn-pass:   #5a8f6e   /* CI passed, signature verified */
--cairn-fail:   #a04040   /* CI failed, key blocked */
--cairn-warn:   #9a7a2a   /* pending, unsigned */
--cairn-muted:  #3a4048   /* inactive / archived */
```

---

## Typography

Forgejo's current font stack uses system fonts. Cairn should pin two:

- **Interface / prose:** [Inter](https://rsms.me/inter/) — geometric sans, designed for screens, reads well at 13–14px. Open license.
- **Code / diffs / commit SHAs / trailers:** [JetBrains Mono](https://www.jetbrains.com/legalforms/fonts/) — optical size designed for code, ligatures off, reads clearly at 12px. Open license.

Both are available as self-hosted WOFF2 (add to `public/assets/fonts/`) — no CDN dependency, which matters for an offline or airgapped Cairn deployment.

Weight usage:
- 400 (Regular) — body, secondary labels
- 500 (Medium) — primary labels, nav items
- 600 (SemiBold) — headings only (h3 and above)

No 700 Bold in the interface chrome. Bold is for user-content headings in markdown, not platform chrome.

---

## Logomark

The mark should be simple enough to render at 16×16 (favicon) without detail loss.

**Concept:** Three horizontal stacked rectangles, slightly offset from each other (like cairn stones stacked by different hands — not perfectly aligned). Each rectangle is slightly wider than the one below. The stack is contained in a square canvas. No gradient, no drop shadow, single flat colour on a dark stone background.

The wordmark is `cairn` in Inter Medium, tracked slightly (+20), lowercase only. The mark and wordmark do not share a baseline — the mark sits slightly above the cap-height. Comfortable whitespace between them.

**What it must not be:**
- No stylised git branch fork shapes (every git host has one)
- No mountain peak (confusable with other platforms)
- No letter-mark

---

## Key screens — what changes from Forgejo

These are the surfaces that need Cairn-specific treatment. Everything else can inherit the theme CSS.

### 1. Home page (unauthenticated)

Currently Forgejo shows a marketing hero. Cairn should show:

- **Left column (narrow):** the mark, tagline ("An agent-native git platform"), links to docs/releases/source
- **Right column (wide):** a live activity stream — recent public commits, with agent identity shown as `[plumb] feat: add identity store` rather than a user avatar

No "create account" CTA at the top. Cairn is not recruiting random signups — it is a specific-purpose tool. The home page should say what it is, not sell it.

Tagline candidates (pick one before build):
- `An agent-native git platform.`
- `Every commit traces to a hand.`
- `Stacked stones. Signed work.`

### 2. Commit page

The commit log is where agent identity is most visible. Each row should show:

```
[plumb]  feat: add identity store   abc1234   2026-05-09 14:32 NZST  ✓
```

Where:
- `[plumb]` is a small pill/badge — background `--cairn-mark-dim`, text `--cairn-stone-9`, monospace, the agent slug
- The human owner (`alice`) is shown on hover/expand only — not in the primary row
- `✓` is the signature verification mark — `--cairn-pass` if verified, `?` in `--cairn-warn` if unsigned
- Commit SHA is monospace, 7 chars, right-aligned in its column

The badge is the visual differentiator. It immediately communicates "this is not a standard user commit" — it is an attributed agent action.

### 3. Agent profile page

A new page type (no Forgejo equivalent). Shows:

- Agent slug + owner
- Derived public key fingerprint (`cairn:...`)
- Status: `active` / `pending` / `blocked`
- Recent commits by this agent (same log format as above)
- Repos this agent has committed to

This page exists so a reviewer landing from a `git log` on an external mirror can verify an agent's identity back to its owner. It is a public trust surface.

### 4. Repository header

The `owner/repo` breadcrumb should show agent identity where applicable. If a repo's last commit was by an agent, the "last modified" line shows the agent badge, not the user avatar.

Clone URLs should default to showing both the ssh and https forms, with the agent-auth note: `# Agents use ssh with HKDF-derived keypair`.

### 5. `.well-known` discovery pages

The spec mandates `/.well-known/llms.txt`, `/.well-known/cairn.json`, `/.well-known/security.txt`. These are plain text — no chrome, no nav. They should be styled like `man` pages: monospace, left-margin only, no sidebar. They exist for machine consumption; the aesthetic should say that.

---

## What we're overriding vs. inheriting

Cairn adds one new theme file: `web_src/css/themes/theme-cairn-dark.css` (and a `theme-cairn-light.css` variant). These override the Forgejo CSS variable set defined in `theme-gitea-dark.css`.

The override strategy:
- Map `--cairn-*` variables onto Forgejo's `--color-primary`, `--color-secondary`, `--color-bg-*`, `--color-text-*` variables
- Set `--cairn-*` as base in `:root`, then map them onto Forgejo's expected names
- This means the theme works with Forgejo's existing components without patching them

The template patches (for agent badge, commit row format, agent profile page) are separate from the theme — they touch the Go template layer, not CSS.

---

## Scope for Phase 1

This document covers ideas. The build should be split:

| Work item | What |
|---|---|
| `theme-cairn-dark.css` | CSS variable override — colour + typography |
| `public/assets/fonts/` | Inter + JetBrains Mono WOFF2 |
| Logomark SVG | Mark only (wordmark can come later) |
| Commit log row | Agent badge pill in `templates/repo/` |
| Home page | Strip Forgejo marketing hero, add activity column |

Agent profile page and `.well-known` styling are Phase 2 — they require new routes that are part of the core Cairn build, not the theme layer.

---

## What I need before implementing

1. **Operator sign-off on palette and tagline.** Colour is a one-way door once it's in the default theme.
2. **Wren's confirmation on font self-hosting approach.** WOFF2 in `public/assets/` should be fine for the Forgejo asset pipeline but wren should confirm there are no export/webpack gotchas.
3. **Anne's input on the mark.** I've described the concept; she may have a better read on proportion and whether three-stones reads immediately at 16px or needs to simplify to two.

---

## Not in scope

- Redesigning Forgejo's component library (buttons, forms, modals) — the theme CSS handles colour; shape stays Forgejo's
- Mobile layout — Forgejo's responsive styles are inherited, not replaced
- Accessibility audit — that comes after the palette is locked, not before
- Light theme — build dark first, derive light after
