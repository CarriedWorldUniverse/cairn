# cairn — multi-agent convergence core (Phase 1 spec)

**Status:** draft for approval · 2026-06-22
**Goal:** a source-control substrate where **many agents work the same repo at once and converge without the branch / worktree / merge-back pain** git forces — while **keeping branches and tags** as the useful, familiar tools they are. Phase 1 builds and proves the *convergence engine* in isolation.
**Origin:** Theo (t3.gg) "a better git" thesis — *"I don't have time to build these things, will you?"* (video, 2026-06-22) + his written "Agentic Code Problem" — re-grounded against cairn by the operator into a concrete need: **concurrent multi-agent editing with no big-bang merge.**

---

## 0. The vision in one paragraph (so Phase 1 has a frame)

Three pieces. **cairn** is the *convergence engine*: it owns a shared **change-graph** organised into **lines** (branches) that form a tree via a parent pointer. Each agent's work is a *change* (a stable id) on a line, recorded by a **commit**. **The commit is the only convergence trigger — there is no watcher service.** On every commit the branch **merges its parent forward** (adopts the parent's latest work) so branches never drift; where edits overlap, cairn writes a **conflict object** *into the change* instead of failing — nobody is blocked. Folding a branch **back** into its parent is a separate, **explicit** act (`cairn fold`) — never automatic — so a broken experiment is simply **abandoned** with zero effect on its parent. Because a branch has been adopting its parent all along, fold-back is a **fast-forward**: the conflicts were already resolved, incrementally, at commit time. An **operation log** records every move so anything is undoable/replayable. **porter** (Phase 2) gives each agent a **copy-on-write mount** of a line, surfaced via the CLI as branch-named folders you `express`/`unexpress` on demand — agents edit normal files with normal tools. **ledger** (Phase 3) is where conflict objects and *semantic* changes surface as tracked items. Git stays a **first-class export** throughout: lines ⇒ branches, changes ⇒ commits, plus real **tags** — so `git clone`/`push` and cairn's existing SSH/HTTP frontends keep working. The engine is **local-first**: the CLI runs the whole model in a folder with **no server and no origin** (like `git init`), while `cairn clone`/`push` interoperate with any standard git remote — *the world is git*, and cairn rides on top of it. **Privacy** is enforced as a **redacted, materialized object graph** at the publish boundary: folder/file/commit-level private flags mean a public clone gets the *shape* of private parts but **none of their bytes — the real private objects never enter the public graph at all** (so they can't be recovered by reading raw git objects). Real content comes only from the authorized private store. Commit-level embargo is compile-safe by construction; permanently-private paths are a deliberate **source-available, distribution-controlled** lever — visible but not buildable without the private parts (cairn does not stub or check builds).

**Decisions locked in brainstorming (2026-06-22):**

1. **Collision model = conflicts-as-data (the jj model).** Both edits land; overlap becomes a stored conflict object resolvable later, anywhere; no merge day.
2. **Convergence is commit-triggered — no watcher.** All convergence runs as part of the **commit** action. **Merge-forward (a branch adopts its parent) runs on every commit**, so branches never drift and conflicts resolve incrementally. **Merge-back (fold into the parent) is explicit** (`cairn fold`, itself commit-triggered) — never automatic — so a broken experiment can be **abandoned** with zero effect on its parent.
3. **Branches & tags stay first-class.** A **branch** is a named *line* off a parent; lines form a **tree** via a parent pointer, so branch-of-a-branch = depth ≥ 2 and "where your source is" = your path from the root. **Tags** are first-class version markers (real `refs/tags`, git-exported).
4. **Access model = CoW mounts via porter** (Phase 2), surfaced as branch-named folders you `express`/`unexpress` via the CLI. Agents keep their existing toolchain unchanged.
5. **Storage core = build on go-git** with a change-id + operation-log layer on top — exactly how jj works (its default backend *is* git). Keeps git-compat for free; reuses cairn's go-git core and frontends.
6. **Version derivation tooling (GitVersion-style) is a dedicated later phase.** Phase 1 ships **tags only**; automatic semver derivation from tags + graph distance + branch is specced separately later.
7. **Local-first / serverless.** The convergence engine is an embedded **library**; the CLI runs it fully **locally with no server and no origin** (like `git init`) — lines, commits, fold, conflicts-as-data, op-log, tags all work on a local store. The cairn **server is optional**, hosting the same library for multi-agent collaboration.
8. **The world is git — bidirectional interop.** `cairn clone <git-url>` imports any standard git repo (branches → lines, commits → changes, tags → tags); `cairn push` writes back to a standard git remote as ordinary branches/commits/tags. cairn-specific state rides in `refs/cairn/*` + commit trailers where it can; plain git ignores it. cairn is a *better local + collaboration layer over git*, never an island.
9. **Privacy is path/commit-scoped, not repo-scoped.** Privacy/disclosure flags attach at **folder, file, or commit** granularity. Flagged-private content is **withheld from the public projection and from any public-remote `push`** until explicitly disclosed — extending cairn's delayed-public-projection design (NEX-25) to fine granularity. Cross-cutting; enforced in the projection/publish layer (Phase 3+), not the Phase-1 core, but the export/push path must honour it.
10. **First build = Phase 1, the convergence core** (incl. lines/branches + tags + lineage), proven with a concurrency test harness — no porter, no ledger needed to validate.

Phases 2–5 are sketched in §10 but are **out of scope for this spec**.

---

## 1. Phase 1 scope

**IN:**

1. **Line (branch)** abstraction — a named line of work with a **parent line** (primary/`main` is the root, parent = none). Lines form a tree; a line's lineage is its path from the root.
2. **Change** abstraction — a stable `change_id` independent of content, on a line, carrying a moving series of commits. The visible commit advances; the change_id does not. (jj's change vs commit distinction.)
3. **Commit (the trigger)** — `Commit(change_id, tree)` records a new git commit on the change and **runs merge-forward** (item 4). The commit is the *sole* convergence trigger; there is no filesystem watcher or background service. In Phase 1 the tree is supplied directly by the caller (the test harness; later, the CLI/porter on `cairn commit`).
4. **Merge-forward on commit** — on each commit, the branch's change is rebased onto its **parent line's current tip** (the branch "adopts" the parent) via three-way merge. Clean → updated cleanly. Overlap → a **conflict object** is materialised and the change carries a *conflicted* (but valid) commit. This is what makes fold-back a fast-forward.
5. **Conflict object** — structured record of an overlap: `change_id`, conflicting paths, the three sides (base / parent / change) materialised as diff3-marked blobs, status `open|resolved`. The change is never blocked; the line is simply not foldable while a conflict is open.
6. **Fold (explicit merge-back) & abandon** — `FoldLine` folds a clean line into its parent as a **fast-forward** (it has already adopted the parent); rejected if conflicts are open. Folding is never automatic. **Abandon** drops a line and its changes with **zero effect on the parent** — the experiment-throwaway guarantee.
7. **Lineage tracking** — every line records its `parent_line`; the engine can return a line's full ancestry chain and the repo's line tree (with ahead/behind per edge). This data is exported so a clone reconstructs the tree (§6). The CLI renders it (§11).
8. **Tags** — first-class named version markers on any commit (`refs/tags/<name>`), created via a `Tag` op, git-exported. (No version-number *derivation* in Phase 1 — see §10.)
9. **Operation log** — append-only record of every mutation (commit, rebase, fold, abandon, branch, tag, resolve, undo) with the full ref-map view-state before/after, actor (herald agent id), timestamp. Enables `Undo`/replay.
10. **Git-compat export** — lines ⇒ `refs/heads/<line>`; each open change ⇒ `refs/cairn/change/<change_id>`; tags ⇒ `refs/tags/<name>`; lineage ⇒ recorded so the tree rebuilds; `change_id` ⇒ a `Change-Id:` commit trailer.
11. **Embedded library / local store** — the engine runs **in-process against a local go-git repo + embedded catalogue; no server, no network required** (exactly what the harness exercises). The cairn server is a thin *optional* host of the same library. This makes the local-first / serverless requirement a Phase-1 property, not a retrofit.
12. **Concurrency test harness** — the Phase-1 proof (see §8).

**OUT (later phases / explicitly deferred):**

- porter CoW mounts + the `express`/`unexpress` CLI (Phase 2).
- the `cairn clone <git-url>` / `cairn push` **CLI commands** — the import/export *mapping* is defined in §6; wrapping it as CLI is Phase 2.
- **privacy/disclosure enforcement** (folder/file/commit-level withholding) — the export/push path in §6 reserves the hook; enforcement lands with the projection layer (Phase 3).
- ledger conflict items + semantic projection (Phase 3).
- resolution UX, auto-resolver agents, web UI (Phase 4).
- **version-number derivation (GitVersion-style)** — tags ship in Phase 1, derivation is Phase 5 (§10).
- auth changes (reuse herald exactly as cairn does today).
- cross-repo; rename-aware merge; multi-parent (octopus) lines.
- automatic conflict *resolution* (Phase 1 only *records* conflicts and supports a caller-supplied resolution).

---

## 2. Where it lives in the cairn tree

New package alongside the existing core (README layout): a sibling to `internal/repo/`.

```
internal/change/         the convergence engine
  line.go                Line (branch) model; parent pointer; lineage; fold/abandon
  change.go              Change + Commit model; change_id generation
  oplog.go               operation log (append-only; view-state = ref-map)
  rebase.go              three-way merge / merge-forward onto parent line
  conflict.go            conflict object: diff3 sides, materialise, status
  tag.go                 tag refs
  store.go               persistence (SQLite catalogue + go-git object store)
  export.go              git-compat ref projection (refs/heads, refs/cairn/change/*, refs/tags) + lineage
internal/change/harness/ concurrency test harness (simulated agents)
```

It reuses `internal/repo/`'s go-git handle + SQLite catalogue (cairn already has `schema.sql`). No frontend changes in Phase 1; the engine is exercised via its Go API + the harness. A thin gRPC surface (§7) is included so Phase 2's CLI/porter can call it, but the *proof* does not depend on a running server.

---

## 3. Data model (Phase 1)

SQLite catalogue (extends cairn's existing DB), git object store via go-git.

```
line                             -- a branch; the tree edge is parent_line
  line_id       text PK
  repo_id       uuid
  name          text             -- "main", "exp/idea", "exp/idea/idea2"; unique within repo.
                                  --   name MAY be path-style to mirror lineage, but parent_line is truth.
  parent_line   text NULL        -- NULL for the root line (primary); else the line it adopts
  tip_commit    text             -- current tip of this line
  base_commit   text             -- parent tip this line last adopted (merge-forward base)
  status        text             -- open | folded | abandoned
  created_at, updated_at

change
  change_id     text PK          -- stable; 256-bit random, reverse-hex render (jj-style)
  repo_id       uuid
  line_id       text FK
  author        text             -- herald agent id
  head_commit   text             -- current commit sha (moves; change_id does not)
  status        text             -- open | folded | abandoned
  has_conflict  bool
  created_at, updated_at

conflict
  id            uuid PK
  change_id     text FK
  path          text             -- conflicting file
  base_blob     text             -- git sha of common-base blob
  parent_blob   text             -- git sha of parent-side blob
  change_blob   text             -- git sha of change-side blob
  marked_blob   text             -- diff3-marked materialised blob (what an editor/agent sees)
  status        text             -- open | resolved
  created_at, resolved_at

tag
  repo_id       uuid
  name          text             -- unique within repo
  commit        text             -- the tagged commit sha
  tagger        text             -- herald agent id
  at            timestamp
  primary_key (repo_id, name)

operation                        -- append-only op-log
  id            text PK          -- ulid/monotonic (deterministic via injected clock)
  repo_id       uuid
  op_type       text             -- branch | commit | rebase | fold | abandon | tag | resolve | undo
  actor         text             -- herald agent id (or "system" for merge-forward)
  parent_op     text             -- previous op id (the op DAG)
  view_before   text JSON        -- ref-map { line:tip, change_id:sha, tag:sha … } before
  view_after    text JSON        -- ref-map after
  detail        text JSON        -- op-specific (line/change ids, paths, conflict ids…)
  at            timestamp
```

**Invariants:**
- `change_id` / `line_id` are stable for life; `head_commit` / `tip_commit` move.
- `parent_line` forms a tree (no cycles); the root line has `parent_line = NULL`. A line's lineage = the chain of `parent_line` to the root.
- A conflicted change has a valid `head_commit` (a commit whose tree contains diff3-marked blobs) — never a blocking error state.
- A line folds into its parent **only** when it has no open conflicts (no conflicted commit ever fast-forwards a parent). Fold is explicit.
- **Abandon** removes a line/change and its refs but never mutates the parent — the throwaway guarantee.
- The op-log is the source of truth for "what the world looked like"; `view_after` of the latest op == current ref-map. `Undo(op)` appends a new `undo` op restoring a prior `view_before` (history append-only; undo is itself an op — jj semantics).

---

## 4. The paths (precise)

**Branch & experiment (the core workflow):**
1. `CreateLine(repo, name="exp/idea", parent="main")` → `line_id`, based on `main`'s current tip.
2. Agent edits and **commits** on the line: `Commit(change_id, tree)`. Each commit records the work **and merges `main` forward** into the branch (adopts the parent's latest). Non-overlapping → clean; overlapping → conflict objects, surfaced and resolvable *now*, not saved up.
3. The branch stays continuously current with `main` simply by committing — no separate "pull"/"rebase" step, no watcher.
4a. **Idea works:** `FoldLine("exp/idea")` → because the branch has already adopted `main`, this is a **fast-forward** of `main` to the branch tip. No merge-back storm.
4b. **Idea is broken:** `AbandonLine("exp/idea")` → the line and its changes vanish; `main` is untouched. Zero blast radius.

**Branch-of-a-branch:** `CreateLine(repo, "exp/idea/idea2", parent="exp/idea")`. `idea2` adopts `exp/idea` on each commit; `exp/idea` adopts `main` on each of *its* commits. Fold proceeds one level at a time (`idea2`→`exp/idea`, then `exp/idea`→`main`). Lineage = `main → exp/idea → idea2` (§7 `GetLineage`).

**Collision path (overlap):**
1. Agent A (on `main` or a sibling line) and Agent B (on `exp/idea`) edit the same region of `foo.go`. Both commit; both land.
2. A's edit becomes part of `main`.
3. On B's **next commit**, merge-forward rebases B onto the new `main`; the three-way merge of (base, parent=A, change=B) overlaps → a **conflict object** for `foo.go` with the three sides + a diff3-marked blob; B's `head_commit` is conflicted-but-valid; `has_conflict = true`.
4. **B is not blocked** — keeps editing/committing other files; conflicts persist as data on the change.
5. A resolver supplies a resolved blob via `ResolveConflict` (or edits the marked file and commits); conflict → `resolved`; once no open conflicts remain, `FoldLine` is allowed. op-log: `resolve`, `fold`.

Convergence is order-independent: non-overlapping edits produce identical final content regardless of fold order; overlapping edits deterministically produce the same conflict sides regardless of order (§8).

---

## 5. Three-way merge / merge-forward (the hard, risky part)

**Risk called out:** go-git's built-in merge support is thin. Phase 1 implements a **blob-level diff3 three-way merge** at the tree level rather than relying on go-git for merge:

- Compute three trees: `base` (common-ancestor commit's tree), `ours` (parent-line tip tree), `theirs` (change tip tree). go-git gives tree/blob access and ancestry.
- Per path, classify: unchanged / added-one-side / modified-one-side / modified-both. Only **modified-both with non-identical results** can conflict.
- For modified-both, run a **diff3 / 3-way line merge** on the blob (vendor a small, vetted diff3 implementation — pinned and reviewed). Clean hunks merge; overlapping hunks → conflict markers → a conflict object + marked blob.
- Phase 1 is **content-path level only** (no rename detection; a rename reads as delete+add). Rename-aware merge deferred.

This isolates the only genuinely novel engineering and makes it unit-testable on synthetic trees independent of everything else.

**Change-id generation:** 256 bits of randomness rendered in a reverse-hex alphabet (jj-style), written both to the catalogue and to a `Change-Id:` commit trailer so the mapping survives a plain `git clone`. Randomness is injected (seedable) so the harness is deterministic.

---

## 6. Git interop — export, import, push, privacy (the world is git)

**Export (cairn → git refs):**
- Lines ⇒ `refs/heads/<line.name>` (ordinary branches; plain git clients see ordinary history).
- Each open change ⇒ `refs/cairn/change/<change_id>` at its `head_commit`.
- Tags ⇒ `refs/tags/<name>`.
- **Lineage** ⇒ each line's `parent_line` + `base_commit` is recorded so a clone rebuilds the tree. Phase-1 mechanism: a `Cairn-Parent:` trailer on the line's first commit + the catalogue; revisitable to a dedicated ref namespace if lineage must travel to arbitrary plain-git clones.
- A conflicted change's commit carries diff3-marked blobs in its tree — a plain `git checkout` shows conflict markers (familiar to any tool), while the structured conflict object carries the machine-readable sides.
- Folded changes/lines drop their `refs/cairn/change/*` and (for folded lines) the line ref; history is preserved in the parent. Abandoned lines drop refs; the op-log retains the trace.

**Import (git → cairn, `cairn clone <git-url>`):** fetch a standard git repo via go-git's transports; map **branches → lines**, **commits → changes**, **tags → tags**. The remote's default branch becomes the **root line**; other branches become lines whose parent is inferred (default: the root line, overridable). Existing `Change-Id:` / `Cairn-Parent:` trailers (from a prior cairn export) are honoured so a cairn↔git↔cairn round-trip preserves change identity and lineage; a repo with no such trailers imports cleanly as a flat set of lines off root.

**Push (cairn → git remote, `cairn push`):** write lines back as ordinary `refs/heads/*`, tags as `refs/tags/*`, to any standard git remote (GitHub/GitLab/…). cairn-specific state (change-ids, lineage, op-log pointers) rides in `refs/cairn/*` + commit trailers — other cairn clients get full fidelity, plain git ignores the extra refs. Conflicted commits are **not** pushed to a shared remote (a line must be clean/folded to publish).

**Privacy at the publish boundary — a redacted, *materialized* object graph (NOT a view filter).** The public projection is a **separate, re-written git object graph**, because a checkout/view filter is trivially defeated by reading the raw objects (`git cat-file`) in a clone. For every projected commit, private content is rewritten in the object graph itself:
- **Private file** → a **shape-only placeholder** blob: the path remains (you can see the file exists) but it carries **none of the real bytes** (e.g. content `<<private>>`). The real blob's hash is *not* referenced anywhere in the public graph.
- **Private folder** → the tree entry remains (shape known) but its children are placeholders (recursively); the real subtree objects are absent.
- **Private (embargoed) commit** → a **boundary, not a hole.** The public projection of that line is **frozen at the embargoed commit's parent**: nothing at or after the embargoed commit is shared — **including later, non-private commits that descend from it** — until `Disclose`. (You can't publish what's built on top of an embargo without leaking or incoherence.) `Disclose` advances the public tip to the next embargo boundary, or to the line tip if none remain.

So a public clone contains: public content in full, the **shape** of private parts (paths/structure), and **none of their content** — there is nothing to spelunk. The real private blobs/trees **never enter the public packfile**; authorized clients obtain them from the private store via cairn (herald-scoped), never from a public clone. (Per-flag, a path may escalate from shape-only to **fully omitted** — not even the path appears — for truly secret items; default is shape-only per "know the shape, not the contents." Encryption-in-place via casket is a possible future variant but is not the default, since it leaves ciphertext in the objects.)

This is the load-bearing **delayed-public-projection** mechanism (NEX-25), now at folder/file/commit granularity. **Phase 1 reserves the object-graph-rewrite hook** — export must be able to emit a *redacted graph*, not merely select refs; the redactor + flag store land in Phase 3. **Hard invariant: no private byte ever exists in a public projection's objects.**

**Buildability is explicitly NOT cairn's problem to solve.** Two cases, by design:
- **Embargo (commit-level)** is **compile-safe by construction** — the public projection shows the last *consistent* public snapshot (which compiled) and **does not advance past the embargo boundary**; the held-back commits (and everything after them) appear in order on `Disclose`. Never a half-redacted tree. This is the security/patch-gap use.
- **Permanently-private folders/files** are the **author's choice and the author's risk.** If you privatise paths that public code depends on, you accept that **a public clone cannot be built from source** — there is **no compilable-stub generation and no dependency check**; cairn does not try to make the public tree build. This is the intended **distribution-control** feature: the source is *visible* (read / learn / audit) but **not buildable** without the private parts — anyone wanting a working artifact must **supply or re-implement the private section themselves**. "Source-available, distribution-controlled."

cairn's **existing SSH/HTTP frontends are unchanged** — clone/fetch/push keep working against the projected refs. No frontend work in Phase 1.

---

## 7. API surface (Phase 1)

In-process Go API is primary; a thin JSON-over-gRPC wrapper (matching cairn's existing `internal/grpcapi` style) is added so the Phase-2 CLI/porter can call across the wire.

```
CreateLine(repo_id, name, parent_line)         -> line_id
FoldLine(line_id)                              -> parent_tip | rejected(reason: has_conflict)
AbandonLine(line_id)                           -> ok            (parent untouched)
ListLines(repo_id, {status?})                  -> []line
GetLineage(line_id)                            -> [root … line]  (ancestry chain)
GetLineTree(repo_id)                           -> tree(line, children, ahead/behind per edge)

CreateChange(line_id, author)                  -> change_id
Commit(change_id, tree)                         -> head_commit, conflict_summary   (runs merge-forward)
GetChange(change_id)                           -> change + conflicts
ListChanges(line_id, {status?})                -> []change
ResolveConflict(change_id, path, resolved_blob)-> conflict.status, change.has_conflict
AbandonChange(change_id)                        -> ok

Tag(repo_id, name, commit, tagger)             -> ok
ListTags(repo_id)                              -> []tag

GetOperationLog(repo_id, {since_op?})          -> []operation
Undo(op_id)                                    -> new_op (restores prior view)
```

`tree` in Phase 1 = a path→bytes map (the harness supplies it; the CLI/porter will too). No auth surface added — when fronted (Phase 2) it sits behind herald like cairn's other gRPC services; `author`/`tagger` is gateway/herald-stamped, never trusted from the model.

---

## 8. The Phase-1 proof: concurrency test harness

The Definition of Done. A harness in `internal/change/harness/` that spins up **N simulated agents** across **one repo with a tree of lines**, each emitting a scripted stream of commits (some overlapping, some not), with interleavings driven by a seedable scheduler.

**Properties asserted:**

1. **Convergence (non-overlap):** for any interleaving of non-overlapping edits, after all lines fold, parent content is identical and equals the union of edits.
2. **Deterministic conflicts (overlap):** overlapping edits produce exactly one conflict object per conflicting path, with the correct three sides — independent of order.
3. **No blocking:** an agent with a conflicted change can still commit further edits to other paths and have them retained.
4. **Fast-forward fold:** a branch that has adopted its parent (via commits) folds back as a **fast-forward** — no merge commit, no late conflict — when clean.
5. **Abandon isolation:** abandoning a branch — even one with open conflicts — leaves the parent line byte-for-byte unchanged.
6. **Lineage integrity:** branch-of-a-branch (depth ≥ 2) folds one level at a time; `GetLineage` returns the correct chain; the line tree is reconstructable from the export.
7. **Op-log replay:** replaying the op-log from empty reproduces the exact final ref-map; `Undo` of the last op restores the prior `view_after` exactly.
8. **Resolution closes the loop:** after `ResolveConflict` for all open conflicts, the previously-conflicted line folds and parent content is the resolved content.
9. **Git-compat:** a plain go-git read of `refs/heads/*`, `refs/cairn/change/*`, `refs/tags/*` matches the engine's view; a conflicted ref's blob contains diff3 markers; lineage rebuilds from the export.

**Method:** property-style testing over many seeds (table + randomised interleavings with a fixed seed list), TDD throughout. Runs in CI (the repo already has build+test+vet — PR #19/#22).

**DoD:** all nine properties green in CI; `go test ./...` and `go vet ./...` clean; the harness scales to ≥3 agents, ≥3 lines incl. depth ≥ 2, and ≥hundreds of interleavings without flakiness.

---

## 9. Build sequence (for the implementation plan)

Each step independently testable, TDD:

1. **change_id + Change/Commit model + store** — create change, commit to a go-git commit, move head, write `Change-Id` trailer. Test: commits advance head, change_id stable.
2. **line model (branch) + parent pointer + lineage** — create line off a parent, track base/tip, return ancestry + tree. Test: lineage chain + tree correct.
3. **operation log** — append-only ops with view-state; replay; `Undo`. Test: replay reproduces state; undo restores prior view.
4. **three-way merge / diff3** (the risk) — pure function on three trees → merged tree + conflicts. Test: synthetic trees, every classification, clean vs conflicted hunks.
5. **merge-forward on commit + fan-out** — each commit rebases onto parent tip; conflicts materialise. Test: §4 paths.
6. **fold (explicit) + abandon** — clean fold = fast-forward; abandon leaves parent untouched. Test: §8 properties 4, 5, 6.
7. **conflict object + ResolveConflict** — materialise diff3 blob, resolve, unblock fold. Test: §8 property 8.
8. **tags** — create/list tag refs. Test: tag round-trips through export.
9. **git-compat export** — ref projection (heads/change/tags) + lineage; conflicted-blob markers. Test: §8 property 9.
10. **concurrency harness** — §8 properties 1–9 across seeds.
11. **thin gRPC wrapper** — wire the API for Phase 2 (no auth changes; herald-stamped author when fronted).

Steps 1–4 are foundational and can land first; 5–9 build on them; 10 is the proof; 11 is the Phase-2 seam.

---

## 10. Later phases (sketch — NOT this spec)

- **Phase 2 — porter CoW mounts + the CLI working-copy model.** Branch-named folders backed by **OverlayFS** (shared lower + per-branch upper; unchanged files cost zero per-branch bytes — §11), `express`/`unexpress`, `cairn commit` (CLI verb or a git post-commit hook) as the commit trigger, and **`cairn clone`/`push` against standard git remotes** (§6) — the CLI runs **fully local with no server** (the engine is embedded, §1.11). No watcher service. Deliverable: two real agents editing on dMon, converging with no merge day. **See §11.** porter lineage: NEX-349 (read-only mount → atomic check-in → lease/merge).
- **Phase 3 — ledger conflict items + semantic projection + privacy/disclosure.** Conflict objects → ledger items; micro-commit noise → clean semantic "change" history via the projection engine. **Folder/file/commit-level privacy flags + the withholding filter + `Disclose`** land here (the delayed-public-projection mechanism, NEX-25, at fine granularity), wired into the §6 publish boundary.
- **Phase 4 — resolution UX + auto-resolver agents + git-export polish.** Designated resolver agents clear conflict objects; web UI surfaces lines/changes/conflicts/lineage; rename-aware merge.
- **Phase 5 — version derivation tooling (GitVersion-style).** Built-in semantic-version derivation from tags + graph distance + branch/line conventions: configurable increment rules, pre-release/build metadata, CI-consumable output. (Tags ship in Phase 1; this is the *derivation* layer on top.)

---

## 11. Client / working-copy model (Phase 2 shape — captured now so Phase 1 export serves it)

How it's *used* on disk. A clone produces a real `.git` (the object store + change-graph) plus branches **expressed** as branch-named folders — the good half of git worktrees, without the cost or the merge-back.

```
myrepo/
├── .git/                object store + change-graph; clones like a real git repo —
│                        plain `git` works here (git-compat from Phase 1 §6)
├── main/                default branch, "expressed" as a folder (worktree-style)
├── exp-idea/            a branch, expressed on demand, its own folder
└── exp-idea--idea2/     a branch-of-a-branch; lineage shown by `cairn status`/`tree`
```

- **Clone** → `.git` + the default branch expressed as a folder named for the branch.
- **Any branch** can be expressed the same way; multiple expressed branches live side by side as sibling folders (Theo's "simultaneous checkouts in multiple locations," made trivial).
- **Branches are NEVER nested inside each other's folders.** A branch-of-a-branch (`exp-idea/idea2`) is expressed as its **own flat sibling folder** at the same level (e.g. `exp-idea--idea2/`), *not* as a subfolder inside `exp-idea/`. Putting a branch among another branch's working files is the mess we're avoiding — you'd no longer be able to tell source content from a checked-out branch. **Lineage lives in metadata (the line tree + `.cairn` stamp + `cairn tree`/`status`), never in the directory hierarchy.** The folder layout is flat; the *tree* is logical. (If root clutter ever bites, the flat set can move under a single `wt/` container — still flat, still no nesting — but that's a Phase-2 cosmetic, not the model.)
- Each expressed folder is a **CoW mount** (porter) → expressing is instant and space-cheap. **Mechanism = OverlayFS:** one shared read-only **lower** layer holds the canonical tree (the "real files"); each expressed branch gets a writable **upper** that starts empty. **Unchanged files are served from the shared lower — zero per-branch bytes**; a write is copied-up into that branch's upper. `unexpress` drops the (tiny) upper. Combined with cairn's content-addressed `.git` (each unique blob stored once), duplication is squeezed out both at rest and in the working copies. (Reflink CoW on Btrfs/XFS is an equivalent transparent fallback; a porter FUSE over the blob store is the future "purest"/max-dedup option — both deferred.)
- **Commit is the trigger.** `cairn commit` (or a git post-commit hook in the folder) calls `Commit`, which records the work and merges the parent forward. There is **no daemon watching the folder**.
- **Lineage is always legible:**
  - `cairn tree` → the whole line forest, with ahead/behind per edge.
  - `cairn status` (inside a folder) → "you are on `exp-idea/idea2` ← `exp-idea` ← `main`; adopted main@<sha>; N ahead of exp-idea."
  - each expressed folder carries a tiny `.cairn` stamp (line id, parent chain, base commit) so a tool/agent reads "where am I" with **no server call**.
- **Runs with or without a server.** `cairn init` makes a working repo in a folder with **no origin** (fully local, like `git init`). A remote is optional and can be **either a cairn server** (multi-agent collaboration) **or a plain git remote** (GitHub/GitLab — §6).
- **CLI verbs:**
  - `cairn init` — create a local repo (no server, no origin).
  - `cairn clone <url>` — from a cairn server **or** a standard git URL (§6 import).
  - `cairn push [remote]` — to a cairn server **or** a standard git remote (§6 push; respects privacy).
  - `cairn express <branch>` / `cairn unexpress <branch>` — materialise / remove a branch folder (line created off the chosen parent if new; history stays in the store on unexpress).
  - `cairn commit` — commit + merge-forward (the trigger).
  - `cairn fold <branch>` — explicit merge-back (fast-forward into parent).
  - `cairn abandon <branch>` — throw the experiment away; parent untouched.
  - `cairn private <path|commit>` / `cairn disclose <path|commit>` — set/clear folder/file/commit privacy (enforced at the publish boundary; Phase 3).
  - `cairn tree` / `cairn status` / `cairn tag` — lineage + tagging.

**Phase-1 implication (the only thing Phase 1 must honour):** the git-compat export (§6) must make `.git` a clean, plain-git-readable store — lines ⇒ `refs/heads/*`, tags ⇒ `refs/tags/*`, change refs under `refs/cairn/change/*`, lineage recorded — so this layout and the CLI build on top in Phase 2 without reworking the core. No CLI or mount code in Phase 1.

---

## 12. Open questions for the plan (small, non-blocking)

- **diff3 library vs hand-rolled** — pin a small reviewed diff3 implementation vs implement directly. Lean toward a vetted small lib, pinned.
- **Lineage export mechanism** — `Cairn-Parent:` commit trailer (Phase-1 default) vs a dedicated `refs/cairn/lineage/*` namespace so lineage travels to arbitrary plain-git clones. Pin in step 2/9.
- **Change-id alphabet** — exact reverse-hex alphabet (jj uses k–z). Cosmetic; pin in step 1.
- **op-log id scheme** — ulid vs monotonic counter; must be deterministic under the seedable clock.
- **annotated vs lightweight tags** — Phase 1 lightweight (`refs/tags/<name>` → commit); annotated-tag objects (message/signature) revisit with Phase 5.
- **branch-name ↔ folder-name encoding** — path-style line names (`exp/idea/idea2`) vs flat folder names with separators (`exp-idea--idea2`). Decide with the CLI in Phase 2; the engine treats `name` as opaque.
- **catalogue vs git-ref op store** — Phase 1 stores the op-log in SQLite (cairn already has it); jj keeps it in refs. Revisit if the op-log should travel with `git clone`.
- **local store location** — embedded catalogue in `.git/cairn/` (colocated, jj-style) vs a sibling `.cairn/`. Pin in step 1; must be self-contained for the serverless/local-first mode.
- **import parent inference** — when cloning a plain git repo, how to assign each branch's parent line (default: root) — by merge-base heuristic vs flat-off-root. Pin with the Phase-2 `cairn clone`.
- **push fidelity** — exactly which cairn metadata rides in `refs/cairn/*` + trailers on push to a plain git remote (lineage, change-ids — yes; op-log — local-by-default like jj). Confirm before Phase-2 push.
- **privacy enforcement semantics** — private = *withheld from public projection/push* (disclosure/embargo, the primary meaning) and/or *read-access-scoped per identity* (herald-gated). Pin with the Phase-3 projection/privacy work; the §6 hook must not preclude either.
