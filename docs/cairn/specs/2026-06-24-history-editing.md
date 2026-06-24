# cairn — history editing (reword / squash / drop on your own line)

**Status:** draft for approval · 2026-06-24
**Goal:** let you tidy your own in-progress history before it's shared — `cairn reword`, `cairn squash`, `cairn drop` — as discrete CLI verbs that auto-rebase descendants (conflicts-as-data). Strictly bounded to a **non-root, un-folded line's own commits, with no child lines**, so the multi-agent convergence guarantee holds: nobody's foundation ever vanishes. No interactive rebase editor (cairn is CLI + agent-first).

**Operator decisions (locked):** v1 = reword + squash + drop; strict guard (refuse if the line has children).

---

## 1. Model

A line is `base B → S1 → S2 → … → Sn → W`: a chain of **sealed** changes (each `cairn commit`/`Seal` = one sealed commit `Si`, with a stable change-id in its message trailer) topped by the open **working** change `W` (WCC). History editing rewrites a contiguous suffix of `S1…Sn` and rebases everything above it (including `W`).

### Editable guard (strict — checked before any edit)
Refuse with a clear error unless ALL hold for the target commit `C` and its line `L`:
- `L` is **non-root** (`ParentLine != ""`) — trunk is never editable.
- `L` is **un-folded / active** (not folded or abandoned).
- `C` is a **sealed** commit on `L`, **strictly above** `L.BaseCommit` (the branch point) — you can't edit the shared base or below.
- `L` has **no child lines** (no line has `ParentLine == L.ID`) — if someone branched off your line, editing is refused (their base would move). *(This is the strict rule; it conservatively refuses even if the child branched above `C`. v2 can narrow to "no child branches at/below C".)*
The target line is resolved **from the commit** (`C`'s change → its `LineID`), so the verbs don't need a branch argument.

---

## 2. Operations (each rebuilds the sealed chain, then rebases `W`)

Shared engine primitive `rewriteChain(lineID string, newSeq []sealStep) (conflicts []Conflict, err error)`: given the new ordered sequence of sealed steps (each `{changeID, message, tree}`), re-seal them in order (each parented on the previous, or `base` for the first), 3-way-rebasing where a step's tree must move onto a changed parent (drop), recording conflicts as data; then **rebase the working change `W`** onto the new top sealed commit; update all affected `change.head_commit`s, remove deleted change rows, advance the line tip, and record one op — **atomically** (one tx; go-git writes outside). Change-ids are preserved except where a step is removed (squash/drop).

### `reword <commit> -m "msg"`
- Only `C = Sk`'s message changes; all trees unchanged. New sequence = `S1…Sn` with `Sk.message = msg`.
- Re-seal: `Sk` gets a new commit hash (new message, same tree+parent); `Sk+1…Sn`, `W` re-parent onto the rewritten chain (trees identical → **no conflicts**, pure metadata rebase).

### `squash <commit>`  (fold `C` into its parent — combine two commits into one)
- `C = Sk` (`k ≥ 2`; if `Sk`'s parent is the base there's no sealed parent to squash into → error "nothing to squash into"). Combine `Sk` into `Sk-1`: the merged step `S'` has `parent = parent(Sk-1)`, **tree = tree(Sk)** (the cumulative result), and message = `Sk-1.message + "\n\n" + Sk.message` (or keep `Sk-1`'s; pin: concatenate). `Sk`'s change-id is **removed**; `S'` keeps `Sk-1`'s change-id.
- New sequence = `S1…Sk-2, S', Sk+1…Sn`. Because `S'.tree == tree(Sk)` (what `Sk+1` was based on), `Sk+1…W` rebase **cleanly (no conflicts)**.

### `drop <commit>`  (remove `C`)
- `C = Sk` removed. New sequence = `S1…Sk-1, Sk+1…Sn` with `Sk`'s change-id removed. `Sk+1` (and onward) was built on `tree(Sk)` but now parents on `Sk-1` (tree `tree(Sk-1)`), so each rebased step does a **3-way merge**: `base = tree(Sk)` (the dropped state), `ours = <new-parent tree>`, `theirs = tree(Si)` → merged tree, **conflicts recorded as data** (never blocks). This is the one verb that can conflict.

After any verb, `W` (the open working change) is rebased onto the new top sealed commit (same 3-way machinery; a no-content-change rebase is clean). The expressed folder is re-materialized to the new working tip.

---

## 3. Engine (`internal/change/edit.go`)
- `sealedChain(lineID string) ([]sealStep, error)` — walk first-parent from the line's top **sealed** commit (the working commit's parent, or the line tip if no open working change) down to `BaseCommit` (exclusive), returning ordered `[]sealStep{ChangeID, Commit, Tree, Message}` (base→top). Reuse `firstParent`, `ChangeIDOf`, `treeHashOf`, the sealed `change` rows.
- `guardEditable(commit string) (lineID string, chain []sealStep, idx int, err error)` — resolve `C`'s change → line; enforce §1; return the chain + `C`'s index.
- `Reword(commit, message string) ([]Conflict, error)`, `Squash(commit string) ([]Conflict, error)`, `Drop(commit string) ([]Conflict, error)` — each `guardEditable`, build the new sequence, call `rewriteChain`.
- `rewriteChain(lineID, newSeq, workingChangeID)` — re-seal in order (`writeCommit` with the carried change-id + message + rebased/merged tree; `mergeTrees` for steps needing a 3-way), rebase `W`, atomic catalogue update (each surviving change's `head_commit`, delete removed change rows, advance line tip, conflicts, one op). Mirror `Seal`'s tx discipline.

## 4. Worktree + CLI
- `Repo.Reword/Squash/Drop(commit, ...)` — call the engine, then **re-materialize the affected expressed folder(s)** to the new line tip (the line is resolved from the commit; if expressed, refresh it). Surface conflicts (drop) like `Commit`/`Pull` do.
- CLI: `cairn reword <commit> -m "msg"`, `cairn squash <commit>`, `cairn drop <commit>`. All via `openRepoSynced` (so `W` reflects live edits before the rewrite). On a refused guard → the clear `not editable` reason. On drop conflicts → the standard conflict notice + exit 2 (resolve then continue). Usage lines added.

## 5. Conflict handling
Only `drop` can conflict. Conflicts attach to the rebased step's change (conflicts-as-data, same model as merge-forward/pull); `cairn status`/`resolve` work on them; the rewrite still completes (non-blocking). reword/squash never conflict.

---

## 6. Out of scope (later / deferred)
- **reorder** and **split** (the operator deferred these — higher conflict surface).
- Narrowing the child-line guard to "no child at/below C" (v1 refuses if ANY child exists).
- Editing across a fold (folded history is immutable by design).
- Cross-line history editing, `--onto`-style rebase. The pre-existing `ResolveConflict`-appends + pull-FF-working-head edges remain WCC follow-ups.

## 7. Testing / DoD
- **reword**: a sealed commit's message changes; its change-id and tree are preserved; descendants + `W` re-parent (new hashes, same content); `log` shows the new message; no conflicts.
- **squash**: two adjacent commits become one (combined message, tree = the later one); the squashed change-id is gone; descendants rebase cleanly; `log` count drops by one.
- **drop**: a commit is removed; an **independent** later commit rebases cleanly (no conflict); a **dependent** later commit records a conflict-as-data (resolvable), the rewrite still completes.
- **guard**: refuse on the root line; on the base commit / below; on a folded line; on a line **with a child line** (the strict rule) — each with a distinct clear message.
- **working change**: after each verb, `W` is rebased and the expressed folder re-materializes to the new tip; status/diff/log stay coherent.
- Full gate + cross-compile; `skipOnWindows` on e2e; all prior phases unaffected. Atomic: a failure mid-rewrite leaves the line unchanged.
