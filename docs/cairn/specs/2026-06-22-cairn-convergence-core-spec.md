# cairn — multi-agent convergence core (Phase 1 spec)

**Status:** draft for approval · 2026-06-22
**Goal:** a source-control substrate where **many agents work the same repo at once and converge without the branch / worktree / merge-back pain** git forces. Phase 1 builds and proves the *convergence engine* in isolation.
**Origin:** Theo (t3.gg) "a better git" thesis — *"I don't have time to build these things, will you?"* (video, 2026-06-22) + his written "Agentic Code Problem" — re-grounded against cairn by the operator into a concrete need: **concurrent multi-agent editing with no big-bang merge.**

---

## 0. The vision in one paragraph (so Phase 1 has a frame)

Three pieces. **cairn** is the *convergence engine*: it owns a shared **change-graph** where each agent's work is a *change* (a stable id, continuously snapshotted), it runs **continuous auto-rebase** as changes land, and where edits overlap it writes a **conflict object** *into the change* instead of failing — nobody is blocked. An **operation log** records every move so anything is undoable/replayable. **porter** (Phase 2) gives each agent a **copy-on-write mount** of the live tree so agents edit normal files with normal tools and porter streams continuous snapshots into cairn — killing worktrees. **ledger** (Phase 3) is where conflict objects and *semantic* changes surface as tracked items, reusing cairn's projection engine (noisy private snapshots → clean semantic history is the same machinery as delayed-public-projection). Git stays a **first-class export** throughout: branches/commits are a projected view, so `git clone`/`push` and cairn's existing SSH/HTTP frontends keep working.

**Decisions locked in brainstorming (2026-06-22):**

1. **Collision model = conflicts-as-data + continuous auto-rebase (the jj model).** Both edits land; overlap becomes a stored conflict object resolvable later, anywhere; no merge day.
2. **Access model = CoW mounts via porter** (Phase 2). Agents keep their existing toolchain unchanged.
3. **Storage core = build on go-git** with a change-id + operation-log layer on top — exactly how jj works (its default backend *is* git). Keeps git-compat for free; reuses cairn's go-git core and frontends.
4. **First build = Phase 1, the convergence core**, proven with a concurrency test harness — no porter, no ledger needed to validate.

Phases 2–4 are sketched in §10 but are **out of scope for this spec**.

---

## 1. Phase 1 scope

**IN:**

1. **Change** abstraction — a stable `change_id` independent of content, carrying a moving series of snapshots (git commits). The visible commit advances; the change_id does not. (jj's change vs commit distinction.)
2. **Continuous snapshot** — `Snapshot(change_id, tree)` records a new commit on the change and triggers auto-rebase. In Phase 1 the tree is supplied directly by the caller (the test harness; later, porter).
3. **Continuous auto-rebase** — when trunk advances, every open change is rebased onto the new trunk tip via three-way merge. Clean → updated cleanly. Overlap → a **conflict object** is materialised and the change carries a *conflicted* (but valid) commit.
4. **Conflict object** — structured record of an overlap: `change_id`, conflicting paths, the three sides (base / trunk / change) materialised as diff3-marked blobs, status `open|resolved`. The change is never blocked; trunk is simply not advanced *by* a conflicted change until it resolves.
5. **Operation log** — append-only record of every mutation (snapshot, rebase, integrate, resolve, abandon, undo) with the full ref-map view-state before/after, actor (herald agent id), timestamp. Enables `Undo`/replay.
6. **Trunk integration** — a clean change can be folded into trunk, advancing it, which re-bases all other open changes. Integration policy is pluggable (continuous vs explicit-ready); Phase 1 default: explicit `Integrate(change_id)`.
7. **Git-compat export** — trunk is a normal branch (`refs/heads/<default>`); each change is `refs/cairn/change/<change_id>` at its latest commit; `change_id` is also written as a `Change-Id:` commit trailer. Existing cairn clone/fetch sees these refs.
8. **Concurrency test harness** — the Phase-1 proof (see §8).

**OUT (later phases / explicitly deferred):**

- porter CoW mounts + snapshot daemon (Phase 2).
- ledger conflict items + semantic projection (Phase 3).
- resolution UX, auto-resolver agents, web UI (Phase 4).
- auth changes (reuse herald exactly as cairn does today).
- cross-repo / multi-trunk / change stacking beyond a single linear trunk.
- automatic conflict *resolution* (Phase 1 only *records* conflicts and supports a caller-supplied resolution).

---

## 2. Where it lives in the cairn tree

New package alongside the existing core (README layout): a sibling to `internal/repo/`.

```
internal/change/         the convergence engine
  change.go              Change + Snapshot model; change_id generation
  oplog.go               operation log (append-only; view-state = ref-map)
  rebase.go              three-way merge / auto-rebase onto trunk
  conflict.go            conflict object: diff3 sides, materialise, status
  trunk.go               trunk advance + re-base fan-out
  store.go               persistence (SQLite catalogue + go-git object store)
  export.go              git-compat ref projection (refs/heads, refs/cairn/change/*)
internal/change/harness/ concurrency test harness (simulated agents)
```

It reuses `internal/repo/`'s go-git handle + SQLite catalogue (cairn already has `schema.sql`). No frontend changes in Phase 1; the engine is exercised via its Go API + the harness. A thin gRPC surface (§7) is included so Phase 2's porter can call it, but the *proof* does not depend on a running server.

---

## 3. Data model (Phase 1)

SQLite catalogue (extends cairn's existing DB), git object store via go-git.

```
change
  change_id     text PK          -- stable; 256-bit random, reverse-hex render (jj-style), e.g. "z" alphabet
  repo_id       uuid             -- existing cairn repo
  author        text             -- herald agent id
  head_commit   text             -- current snapshot's git sha (moves; change_id does not)
  base_commit   text             -- trunk sha this change is currently based on
  status        text             -- open | integrated | abandoned
  has_conflict  bool
  created_at, updated_at

conflict
  id            uuid PK
  change_id     text FK
  path          text             -- conflicting file
  base_blob     text             -- git sha of common-base blob
  trunk_blob    text             -- git sha of trunk-side blob
  change_blob   text             -- git sha of change-side blob
  marked_blob   text             -- diff3-marked materialised blob (what an editor/agent sees)
  status        text             -- open | resolved
  created_at, resolved_at

operation                        -- append-only op-log
  id            text PK          -- monotonic / ulid (deterministic in tests via injected clock)
  repo_id       uuid
  op_type       text             -- snapshot | rebase | integrate | resolve | abandon | undo
  actor         text             -- herald agent id (or "system" for auto-rebase)
  parent_op     text             -- previous op id (the op DAG)
  view_before   text JSON        -- ref-map { trunk: sha, change_id: sha, ... } before
  view_after    text JSON        -- ref-map after
  detail        text JSON        -- op-specific (change_id, paths, conflict ids…)
  at            timestamp
```

**Invariants:**
- `change_id` is stable for the life of the change; `head_commit` moves with each snapshot/rebase.
- A conflicted change has a valid `head_commit` (a commit whose tree contains diff3-marked blobs) — it is *never* an error state that blocks the agent.
- Trunk advances **only** via clean changes (no conflicted commit ever becomes trunk).
- The op-log is the source of truth for "what the world looked like"; `view_after` of the latest op == current ref-map. `Undo(op)` writes a new `undo` op whose `view_after` restores a prior `view_before` (history is append-only; undo is itself an op — jj semantics).

---

## 4. The two paths (precise)

**Happy path (non-overlapping):**
1. `CreateChange(repo, author)` → `change_id`, based on current trunk tip.
2. `Snapshot(change_id, tree₁)` → new commit C₁ on the change (parent = base_commit). op-log: `snapshot`.
3. Another agent integrates its change → trunk advances T→T'. op-log: `integrate`, then `rebase` for every other open change.
4. This change auto-rebases C₁ onto T'. Non-overlapping → clean rebased commit C₁'. `base_commit = T'`. No conflict.
5. `Integrate(change_id)` when ready → trunk advances to C₁'. Other open changes re-base. Done — no branch named, no PR, no merge step.

**Collision path (overlap):**
1. Agent A and Agent B each hold an open change; both snapshot edits to the same region of `foo.go`. Both snapshots **land** as commits on their respective changes.
2. A integrates first → trunk = A's tip.
3. B's change auto-rebases onto the new trunk; the three-way merge of (base, trunk=A's edit, B's edit) overlaps → a **conflict object** is written for `foo.go` with the three sides + a diff3-marked blob; B's `head_commit` becomes a conflicted-but-valid commit; `has_conflict = true`.
4. **B is not blocked** — B keeps editing other files; further snapshots layer on top of the conflicted commit.
5. A resolver (any agent, or a human, in a later phase; in Phase 1 a caller) supplies a resolved tree for `foo.go` via `ResolveConflict`; the conflict object → `resolved`; once no open conflicts remain, B's change is integratable. op-log: `resolve`.

Everything above is order-independent at the convergence level: the final trunk content for a given set of non-overlapping edits is the same regardless of integration order; overlapping edits deterministically produce the same conflict sides regardless of order (see §8 properties).

---

## 5. Three-way merge / auto-rebase (the hard, risky part)

**Risk called out:** go-git's built-in merge support is thin. Phase 1 implements a **blob-level diff3 three-way merge** at the tree level rather than relying on go-git for merge:

- Compute the three trees: `base` (common ancestor commit's tree), `ours` (trunk tip tree), `theirs` (change tip tree). go-git gives us tree/blob access and ancestry.
- For each path, classify: unchanged / added-one-side / modified-one-side / modified-both. Only **modified-both with non-identical results** can conflict.
- For modified-both, run a **diff3 / 3-way line merge** on the blob (vendor a small, well-tested merge lib, e.g. a diff3 implementation — pinned and reviewed; no heavy dependency). Clean hunks merge; overlapping hunks produce conflict markers → a conflict object + marked blob.
- Directory/rename handling for Phase 1: **content-path level only** (no rename detection); a rename reads as delete+add. Rename-aware merge is deferred.

This isolates the only genuinely novel engineering and makes it unit-testable on synthetic trees independent of everything else.

**Change-id generation:** 256 bits of randomness rendered in a reverse-hex alphabet (jj-style, collision-safe, human-distinguishable), written both to the catalogue and to a `Change-Id:` commit trailer so the mapping survives a plain `git clone`. Randomness is injected (seedable) so the harness is deterministic.

---

## 6. Git-compat export (§3 item 7, detail)

- Trunk ⇒ `refs/heads/<default_branch>` (a normal branch; plain git clients see ordinary history).
- Each open change ⇒ `refs/cairn/change/<change_id>` at its `head_commit`.
- A conflicted change's commit carries diff3-marked blobs in its tree — a plain `git checkout` of that ref shows conflict markers in the file (familiar to any tool), while the structured conflict object carries the machine-readable sides.
- Integrated changes drop their `refs/cairn/change/*` ref (history preserved in trunk). Abandoned changes drop the ref; the op-log retains the trace.

This means cairn's **existing SSH/HTTP frontends are unchanged** — clone/fetch/push keep working against the projected refs. No frontend work in Phase 1.

---

## 7. API surface (Phase 1)

In-process Go API is primary; a thin JSON-over-gRPC wrapper (matching cairn's existing `internal/grpcapi` style) is added so Phase-2 porter can call across the wire. Both are the same operations:

```
CreateChange(repo_id, author)                 -> change_id
Snapshot(change_id, tree)                      -> head_commit, conflict_summary
GetChange(change_id)                           -> change + conflicts
ListChanges(repo_id, {status?})                -> []change
Integrate(change_id)                           -> trunk_commit | rejected(reason: has_conflict)
ResolveConflict(change_id, path, resolved_blob)-> conflict.status, change.has_conflict
Abandon(change_id)                             -> ok
GetOperationLog(repo_id, {since_op?})          -> []operation
Undo(op_id)                                    -> new_op (restores prior view)
```

`tree` in Phase 1 = a path→bytes map (the harness supplies it; porter will too). No auth surface added — when fronted (Phase 2), it sits behind herald exactly like cairn's other gRPC services; the `author` is gateway/herald-stamped, never trusted from the model.

---

## 8. The Phase-1 proof: concurrency test harness

The Definition of Done. A harness in `internal/change/harness/` that spins up **N simulated agents** against **one repo**, each emitting a scripted stream of snapshots (some overlapping, some not), with interleavings driven by a seedable scheduler.

**Properties asserted:**

1. **Convergence (non-overlap):** for any interleaving of non-overlapping edits, after all changes integrate, trunk content is identical and equals the union of edits. (Run across many seeds.)
2. **Deterministic conflicts (overlap):** overlapping edits produce exactly one conflict object per conflicting path, with the correct three sides — independent of integration order.
3. **No blocking:** an agent with a conflicted change can still snapshot further edits to other paths and have them retained.
4. **Op-log replay:** replaying the op-log from empty reproduces the exact final ref-map; `Undo` of the last op restores the prior `view_after` exactly.
5. **Resolution closes the loop:** after `ResolveConflict` for all open conflicts, the previously-conflicted change integrates and trunk content is the resolved content.
6. **Git-compat:** after a run, a plain go-git read of `refs/heads/<default>` and `refs/cairn/change/*` matches the engine's view; a conflicted ref's blob contains diff3 markers.

**Method:** property-style testing over many seeds (table + randomised interleavings with a fixed seed list), TDD throughout. Runs in CI (the repo already has build+test+vet — PR #19/#22).

**DoD:** all six properties green in CI; `go test ./...` and `go vet ./...` clean; the harness can scale to ≥3 agents and ≥hundreds of interleavings without flakiness.

---

## 9. Build sequence (for the implementation plan)

Each step independently testable, TDD:

1. **change_id + Change/Snapshot model + store** — create change, snapshot to a go-git commit, move head, write `Change-Id` trailer. Test: snapshots advance head, change_id stable.
2. **operation log** — append-only ops with view-state; replay; `Undo`. Test: replay reproduces state; undo restores prior view.
3. **three-way merge / diff3** (the risk) — pure function on three trees → merged tree + conflicts. Test: synthetic trees, every classification, clean vs conflicted hunks.
4. **auto-rebase + trunk advance + fan-out** — integrate advances trunk and re-bases open changes; conflicts materialise as objects. Test: the two paths in §4.
5. **conflict object + ResolveConflict** — materialise diff3 blob, resolve, unblock integration. Test: §8 property 5.
6. **git-compat export** — ref projection; conflicted-blob markers. Test: §8 property 6.
7. **concurrency harness** — §8 properties 1–6 across seeds.
8. **thin gRPC wrapper** — wire the API for Phase 2 (no auth changes; herald-stamped author when fronted).

Steps 1–3 are pure/foundational and can land first; 4–6 build on them; 7 is the proof; 8 is the Phase-2 seam.

---

## 10. Later phases (sketch — NOT this spec)

- **Phase 2 — porter CoW mounts + snapshot daemon.** Per-agent copy-on-write mount of the live tree; daemon watches the mount and calls `Snapshot`. Deliverable: two real agents editing on dMon, converging with no merge day. porter already has design lineage (NEX-349: read-only mount → atomic check-in → lease/merge).
- **Phase 3 — ledger conflict items + semantic projection.** Conflict objects → ledger items; micro-snapshot noise → clean semantic "change" history via the projection engine (same machinery as delayed-public-projection).
- **Phase 4 — resolution UX + auto-resolver agents + git-export polish.** Designated resolver agents clear conflict objects; web UI surfaces changes/conflicts; rename-aware merge.

---

## 11. Open questions for the plan (small, non-blocking)

- **diff3 library vs hand-rolled** — pin a small reviewed diff3 implementation vs implement the line-merge directly. Decide in step 3 plan; lean toward a vetted small lib, pinned.
- **Integration policy** — explicit `Integrate` (Phase-1 default) vs continuous auto-integrate of clean changes. Phase 1 ships explicit; expose the policy seam.
- **Change-id alphabet** — exact reverse-hex alphabet (jj uses k–z). Cosmetic; pin in step 1.
- **op-log id scheme** — ulid vs monotonic counter; must be deterministic under the seedable clock for the harness.
- **trunk model** — single default-branch trunk for Phase 1; multi-trunk / stacked changes deferred.
- **catalogue vs git-ref op store** — Phase 1 stores the op-log in SQLite (cairn already has it); jj keeps an op store in refs. Revisit if we want the op-log to travel with `git clone`.
