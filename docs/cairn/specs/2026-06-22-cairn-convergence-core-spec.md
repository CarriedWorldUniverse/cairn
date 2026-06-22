# cairn — multi-agent convergence core (Phase 1 spec)

**Status:** draft for approval · 2026-06-22
**Goal:** a source-control substrate where **many agents work the same repo at once and converge without the branch / worktree / merge-back pain** git forces — while **keeping branches and tags** as the useful, familiar tools they are. Phase 1 builds and proves the *convergence engine* in isolation.
**Origin:** Theo (t3.gg) "a better git" thesis — *"I don't have time to build these things, will you?"* (video, 2026-06-22) + his written "Agentic Code Problem" — re-grounded against cairn by the operator into a concrete need: **concurrent multi-agent editing with no big-bang merge.**

---

## 0. The vision in one paragraph (so Phase 1 has a frame)

Three pieces. **cairn** is the *convergence engine*: it owns a shared **change-graph** organised into **lines** (branches). Each agent's work is a *change* (a stable id, continuously snapshotted) on a line; cairn runs **continuous auto-rebase** so every line continuously **adopts the changes on its parent line**, and where edits overlap it writes a **conflict object** *into the change* instead of failing — nobody is blocked. Because a branch is always current with its parent, **"merge-back" degrades to a fast-forward** (the conflicts were already resolved, incrementally, as the work happened); a broken experiment is simply **abandoned** with zero effect on the parent. An **operation log** records every move so anything is undoable/replayable. **porter** (Phase 2) gives each agent a **copy-on-write mount** of a line so agents edit normal files with normal tools and porter streams continuous snapshots into cairn — killing worktrees. **ledger** (Phase 3) is where conflict objects and *semantic* changes surface as tracked items, reusing cairn's projection engine. Git stays a **first-class export** throughout: lines ⇒ branches, changes ⇒ commits, plus real **tags** — so `git clone`/`push` and cairn's existing SSH/HTTP frontends keep working.

**Decisions locked in brainstorming (2026-06-22):**

1. **Collision model = conflicts-as-data + continuous auto-rebase (the jj model).** Both edits land; overlap becomes a stored conflict object resolvable later, anywhere; no merge day.
2. **Branches & tags stay first-class.** A **branch** is a named experiment *line* off a parent; it **continuously adopts** its parent's changes (forward auto-rebase), so merge-back is resolved incrementally and degrades to a fast-forward — and a broken experiment is **abandoned** with zero effect on primary. **Tags** are first-class version markers (real `refs/tags`, git-exported).
3. **Access model = CoW mounts via porter** (Phase 2). Agents keep their existing toolchain unchanged.
4. **Storage core = build on go-git** with a change-id + operation-log layer on top — exactly how jj works (its default backend *is* git). Keeps git-compat for free; reuses cairn's go-git core and frontends.
5. **Version derivation tooling (GitVersion-style) is a dedicated later phase.** Phase 1 ships **tags only**; automatic semver derivation from tags + graph distance + branch is specced separately later.
6. **First build = Phase 1, the convergence core** (incl. lines/branches + tags), proven with a concurrency test harness — no porter, no ledger needed to validate.

Phases 2–5 are sketched in §10 but are **out of scope for this spec**.

---

## 1. Phase 1 scope

**IN:**

1. **Line (branch)** abstraction — a named line of work with a **parent line** (primary/`main` is the root line, parent = none). A line continuously rebases its open changes onto its parent's tip ("adopts" the parent).
2. **Change** abstraction — a stable `change_id` independent of content, on a line, carrying a moving series of snapshots (git commits). The visible commit advances; the change_id does not. (jj's change vs commit distinction.)
3. **Continuous snapshot** — `Snapshot(change_id, tree)` records a new commit on the change and triggers auto-rebase. In Phase 1 the tree is supplied directly by the caller (the test harness; later, porter).
4. **Continuous auto-rebase / parent-adoption** — when a parent line advances, every child line's open changes are rebased onto the new parent tip via three-way merge. Clean → updated cleanly. Overlap → a **conflict object** is materialised and the change carries a *conflicted* (but valid) commit. This is what makes merge-back a fast-forward.
5. **Conflict object** — structured record of an overlap: `change_id`, conflicting paths, the three sides (base / parent / change) materialised as diff3-marked blobs, status `open|resolved`. The change is never blocked; the line is simply not folded into its parent while a conflict is open.
6. **Fold-up & abandon** — folding a clean line into its parent is a **fast-forward** (it's already adopted the parent). **Abandon** drops a line and all its changes with **zero effect on the parent** — the experiment-throwaway guarantee.
7. **Tags** — first-class named version markers on any change/commit (`refs/tags/<name>`), created via a `Tag` op, git-exported. (No version-number *derivation* in Phase 1 — see §10.)
8. **Operation log** — append-only record of every mutation (snapshot, rebase, fold, abandon, branch, tag, resolve, undo) with the full ref-map view-state before/after, actor (herald agent id), timestamp. Enables `Undo`/replay.
9. **Git-compat export** — lines ⇒ `refs/heads/<line>`; each open change ⇒ `refs/cairn/change/<change_id>`; tags ⇒ `refs/tags/<name>`; `change_id` also written as a `Change-Id:` commit trailer. Existing cairn clone/fetch sees these refs.
10. **Concurrency test harness** — the Phase-1 proof (see §8).

**OUT (later phases / explicitly deferred):**

- porter CoW mounts + snapshot daemon (Phase 2).
- ledger conflict items + semantic projection (Phase 3).
- resolution UX, auto-resolver agents, web UI (Phase 4).
- **version-number derivation (GitVersion-style)** — tags ship in Phase 1, derivation is Phase 5 (§10).
- auth changes (reuse herald exactly as cairn does today).
- cross-repo; rename-aware merge; stacked/octopus lines beyond single-parent.
- automatic conflict *resolution* (Phase 1 only *records* conflicts and supports a caller-supplied resolution).

---

## 2. Where it lives in the cairn tree

New package alongside the existing core (README layout): a sibling to `internal/repo/`.

```
internal/change/         the convergence engine
  line.go                Line (branch) model; parent pointer; fold/abandon
  change.go              Change + Snapshot model; change_id generation
  oplog.go               operation log (append-only; view-state = ref-map)
  rebase.go              three-way merge / auto-rebase onto parent line
  conflict.go            conflict object: diff3 sides, materialise, status
  tag.go                 tag refs
  store.go               persistence (SQLite catalogue + go-git object store)
  export.go              git-compat ref projection (refs/heads, refs/cairn/change/*, refs/tags)
internal/change/harness/ concurrency test harness (simulated agents)
```

It reuses `internal/repo/`'s go-git handle + SQLite catalogue (cairn already has `schema.sql`). No frontend changes in Phase 1; the engine is exercised via its Go API + the harness. A thin gRPC surface (§7) is included so Phase 2's porter can call it, but the *proof* does not depend on a running server.

---

## 3. Data model (Phase 1)

SQLite catalogue (extends cairn's existing DB), git object store via go-git.

```
line                             -- a branch
  line_id       text PK
  repo_id       uuid
  name          text             -- "main", "exp/agent-a-idea"; unique within repo
  parent_line   text NULL        -- NULL for the root line (primary); else the line it adopts
  tip_commit    text             -- current integrated tip of this line
  base_commit   text             -- parent tip this line is currently rebased onto
  status        text             -- open | folded | abandoned
  created_at, updated_at

change
  change_id     text PK          -- stable; 256-bit random, reverse-hex render (jj-style)
  repo_id       uuid
  line_id       text FK
  author        text             -- herald agent id
  head_commit   text             -- current snapshot's git sha (moves; change_id does not)
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
  op_type       text             -- branch | snapshot | rebase | fold | abandon | tag | resolve | undo
  actor         text             -- herald agent id (or "system" for auto-rebase)
  parent_op     text             -- previous op id (the op DAG)
  view_before   text JSON        -- ref-map { line:tip, change_id:sha, tag:sha … } before
  view_after    text JSON        -- ref-map after
  detail        text JSON        -- op-specific (line/change ids, paths, conflict ids…)
  at            timestamp
```

**Invariants:**
- `change_id` / `line_id` are stable for life; `head_commit` / `tip_commit` move.
- A conflicted change has a valid `head_commit` (a commit whose tree contains diff3-marked blobs) — it is *never* a blocking error state.
- A line is folded into its parent **only** when it has no open conflicts (no conflicted commit ever fast-forwards a parent).
- **Abandon** removes a line/change and its refs but never mutates the parent — the throwaway guarantee.
- The op-log is the source of truth for "what the world looked like"; `view_after` of the latest op == current ref-map. `Undo(op)` appends a new `undo` op restoring a prior `view_before` (history append-only; undo is itself an op — jj semantics).

---

## 4. The paths (precise)

**Branch & experiment (the core workflow):**
1. `CreateLine(repo, name="exp/idea", parent="main")` → `line_id`, based on `main`'s current tip.
2. Agent snapshots changes on the line: `Snapshot(change_id, tree)` → commits on the line.
3. **Parent advances** (someone folds work into `main`, tip T→T'). op-log: `fold`, then `rebase` fan-out.
4. The experiment line **auto-adopts**: its open changes rebase onto T'. Non-overlapping → clean; overlapping → conflict objects (resolved incrementally, now, not saved up). `base_commit = T'`.
5a. **Idea works:** `FoldLine("exp/idea")` → because the line already adopted `main`, this is a **fast-forward** of `main` to the line's tip. No merge-back, no conflict storm.
5b. **Idea is broken:** `AbandonLine("exp/idea")` → the line and its changes vanish; `main` is untouched. Zero blast radius.

**Happy path (concurrent, non-overlapping) — same line or sibling lines:**
Two agents snapshot non-overlapping edits; both land; auto-rebase converges them; folding is order-independent and trunk content equals the union of edits.

**Collision path (overlap):**
1. Agent A and Agent B edit the same region of `foo.go` (same parent). Both snapshots **land** as commits.
2. A folds first → parent tip = A's edit.
3. B's line auto-rebases onto the new parent; the three-way merge of (base, parent=A, change=B) overlaps → a **conflict object** for `foo.go` with the three sides + a diff3-marked blob; B's `head_commit` becomes conflicted-but-valid; `has_conflict = true`.
4. **B is not blocked** — keeps editing other files; further snapshots layer on top.
5. A resolver supplies a resolved blob via `ResolveConflict`; conflict → `resolved`; once no open conflicts remain, B's line folds (fast-forward). op-log: `resolve`, `fold`.

Convergence is order-independent: non-overlapping edits produce identical final content regardless of fold order; overlapping edits deterministically produce the same conflict sides regardless of order (§8).

---

## 5. Three-way merge / auto-rebase (the hard, risky part)

**Risk called out:** go-git's built-in merge support is thin. Phase 1 implements a **blob-level diff3 three-way merge** at the tree level rather than relying on go-git for merge:

- Compute three trees: `base` (common-ancestor commit's tree), `ours` (parent-line tip tree), `theirs` (change tip tree). go-git gives tree/blob access and ancestry.
- Per path, classify: unchanged / added-one-side / modified-one-side / modified-both. Only **modified-both with non-identical results** can conflict.
- For modified-both, run a **diff3 / 3-way line merge** on the blob (vendor a small, vetted diff3 implementation — pinned and reviewed). Clean hunks merge; overlapping hunks → conflict markers → a conflict object + marked blob.
- Phase 1 is **content-path level only** (no rename detection; a rename reads as delete+add). Rename-aware merge deferred.

This isolates the only genuinely novel engineering and makes it unit-testable on synthetic trees independent of everything else.

**Change-id generation:** 256 bits of randomness rendered in a reverse-hex alphabet (jj-style), written both to the catalogue and to a `Change-Id:` commit trailer so the mapping survives a plain `git clone`. Randomness is injected (seedable) so the harness is deterministic.

---

## 6. Git-compat export (§1 item 9, detail)

- Lines ⇒ `refs/heads/<line.name>` (ordinary branches; plain git clients see ordinary history).
- Each open change ⇒ `refs/cairn/change/<change_id>` at its `head_commit`.
- Tags ⇒ `refs/tags/<name>`.
- A conflicted change's commit carries diff3-marked blobs in its tree — a plain `git checkout` shows conflict markers (familiar to any tool), while the structured conflict object carries the machine-readable sides.
- Folded changes/lines drop their `refs/cairn/change/*` and (for folded lines) the line ref; history is preserved in the parent. Abandoned lines drop refs; the op-log retains the trace.

cairn's **existing SSH/HTTP frontends are unchanged** — clone/fetch/push keep working against the projected refs. No frontend work in Phase 1.

---

## 7. API surface (Phase 1)

In-process Go API is primary; a thin JSON-over-gRPC wrapper (matching cairn's existing `internal/grpcapi` style) is added so Phase-2 porter can call across the wire.

```
CreateLine(repo_id, name, parent_line)         -> line_id
FoldLine(line_id)                              -> parent_tip | rejected(reason: has_conflict)
AbandonLine(line_id)                           -> ok            (parent untouched)
ListLines(repo_id, {status?})                  -> []line

CreateChange(line_id, author)                  -> change_id
Snapshot(change_id, tree)                       -> head_commit, conflict_summary
GetChange(change_id)                           -> change + conflicts
ListChanges(line_id, {status?})                -> []change
ResolveConflict(change_id, path, resolved_blob)-> conflict.status, change.has_conflict
AbandonChange(change_id)                        -> ok

Tag(repo_id, name, commit, tagger)             -> ok
ListTags(repo_id)                              -> []tag

GetOperationLog(repo_id, {since_op?})          -> []operation
Undo(op_id)                                    -> new_op (restores prior view)
```

`tree` in Phase 1 = a path→bytes map (the harness supplies it; porter will too). No auth surface added — when fronted (Phase 2) it sits behind herald like cairn's other gRPC services; `author`/`tagger` is gateway/herald-stamped, never trusted from the model.

---

## 8. The Phase-1 proof: concurrency test harness

The Definition of Done. A harness in `internal/change/harness/` that spins up **N simulated agents** across **one repo with multiple lines**, each emitting a scripted stream of snapshots (some overlapping, some not), with interleavings driven by a seedable scheduler.

**Properties asserted:**

1. **Convergence (non-overlap):** for any interleaving of non-overlapping edits, after all changes fold, parent content is identical and equals the union of edits.
2. **Deterministic conflicts (overlap):** overlapping edits produce exactly one conflict object per conflicting path, with the correct three sides — independent of fold order.
3. **No blocking:** an agent with a conflicted change can still snapshot further edits to other paths and have them retained.
4. **Parent-adoption / fast-forward fold:** an experiment line that has continuously adopted its parent folds back as a **fast-forward** (no merge commit, no late conflict) when clean.
5. **Abandon isolation:** abandoning an experiment line — even one with open conflicts — leaves the parent line byte-for-byte unchanged.
6. **Op-log replay:** replaying the op-log from empty reproduces the exact final ref-map; `Undo` of the last op restores the prior `view_after` exactly.
7. **Resolution closes the loop:** after `ResolveConflict` for all open conflicts, the previously-conflicted line folds and parent content is the resolved content.
8. **Git-compat:** a plain go-git read of `refs/heads/*`, `refs/cairn/change/*`, `refs/tags/*` matches the engine's view; a conflicted ref's blob contains diff3 markers.

**Method:** property-style testing over many seeds (table + randomised interleavings with a fixed seed list), TDD throughout. Runs in CI (the repo already has build+test+vet — PR #19/#22).

**DoD:** all eight properties green in CI; `go test ./...` and `go vet ./...` clean; the harness scales to ≥3 agents, ≥2 lines, and ≥hundreds of interleavings without flakiness.

---

## 9. Build sequence (for the implementation plan)

Each step independently testable, TDD:

1. **change_id + Change/Snapshot model + store** — create change, snapshot to a go-git commit, move head, write `Change-Id` trailer. Test: snapshots advance head, change_id stable.
2. **line model (branch) + parent pointer** — create line off a parent, track base/tip. Test: lines created, parentage recorded.
3. **operation log** — append-only ops with view-state; replay; `Undo`. Test: replay reproduces state; undo restores prior view.
4. **three-way merge / diff3** (the risk) — pure function on three trees → merged tree + conflicts. Test: synthetic trees, every classification, clean vs conflicted hunks.
5. **auto-rebase / parent-adoption + fold + fan-out** — parent advance re-bases child lines; clean fold = fast-forward; conflicts materialise. Test: §4 paths.
6. **conflict object + ResolveConflict** — materialise diff3 blob, resolve, unblock fold. Test: §8 property 7.
7. **abandon** — drop line/change with no parent effect. Test: §8 property 5.
8. **tags** — create/list tag refs. Test: tag round-trips through export.
9. **git-compat export** — ref projection (heads/change/tags); conflicted-blob markers. Test: §8 property 8.
10. **concurrency harness** — §8 properties 1–8 across seeds.
11. **thin gRPC wrapper** — wire the API for Phase 2 (no auth changes; herald-stamped author when fronted).

Steps 1–4 are foundational and can land first; 5–9 build on them; 10 is the proof; 11 is the Phase-2 seam.

---

## 10. Later phases (sketch — NOT this spec)

- **Phase 2 — porter CoW mounts + snapshot daemon.** Per-agent copy-on-write mount of a line; daemon watches the mount and calls `Snapshot`. Deliverable: two real agents editing on dMon, converging with no merge day. porter lineage: NEX-349 (read-only mount → atomic check-in → lease/merge).
- **Phase 3 — ledger conflict items + semantic projection.** Conflict objects → ledger items; micro-snapshot noise → clean semantic "change" history via the projection engine (same machinery as delayed-public-projection).
- **Phase 4 — resolution UX + auto-resolver agents + git-export polish.** Designated resolver agents clear conflict objects; web UI surfaces lines/changes/conflicts; rename-aware merge.
- **Phase 5 — version derivation tooling (GitVersion-style).** Built-in semantic-version derivation from tags + graph distance + branch/line conventions: configurable increment rules, pre-release/build metadata, CI-consumable output. (Tags themselves ship in Phase 1; this is the *derivation* layer on top.)

---

## 11. Open questions for the plan (small, non-blocking)

- **diff3 library vs hand-rolled** — pin a small reviewed diff3 implementation vs implement directly. Lean toward a vetted small lib, pinned.
- **Fold policy** — explicit `FoldLine` (Phase-1 default) vs continuous auto-fold of clean lines. Phase 1 ships explicit; expose the policy seam.
- **Parent-adoption cadence** — re-base child lines immediately on parent advance (Phase-1 default) vs batched. Pin in step 5.
- **Change-id alphabet** — exact reverse-hex alphabet (jj uses k–z). Cosmetic; pin in step 1.
- **op-log id scheme** — ulid vs monotonic counter; must be deterministic under the seedable clock.
- **annotated vs lightweight tags** — Phase 1 lightweight (`refs/tags/<name>` → commit); annotated-tag objects (message/signature) revisit with Phase 5.
- **catalogue vs git-ref op store** — Phase 1 stores the op-log in SQLite (cairn already has it); jj keeps it in refs. Revisit if the op-log should travel with `git clone`.
