# cairn→cairn fidelity — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** push a `refs/cairn/meta` snapshot of the cairn change-graph (lines/changes/conflicts) to cairn remotes; reconstruct it exactly on clone, so cairn↔cairn preserves the real line tree + change-ids + sealed state + open conflicts (vs the current lossy git projection).

**Architecture:** `ExportMeta` serializes the catalogue to a JSON blob committed at `refs/cairn/meta`; push (for `remote_kind=cairn`) adds the meta refspec; fetch pulls `refs/cairn/*`; import reconstructs from the meta when present (else the existing flat projection). Origin IDs reused; op-log stays local; fidelity on clone (pull-merge deferred).

**Tech:** Go 1.26.3, go-git, modernc sqlite. Spec: `docs/cairn/specs/2026-06-24-cairn-fidelity.md`. Conflict columns (real): `id, change_id, path, base_blob, parent_blob, change_blob, marked_blob, status, created_at, resolved_at`.

**Conventions:** errors `pkg.Func: %w`; `skipOnWindows` on local-fixture e2e; one tx; commit after each task.

---

## Task 1: `ExportMeta` (serialize the change-graph → meta commit)

**Files:** create `internal/change/meta.go`, `internal/change/meta_test.go`.

- [ ] **Step 1: the meta model + test**
```go
const metaVersion = 1

type metaDoc struct {
	Version   int          `json:"version"`
	Lines     []metaLine   `json:"lines"`
	Changes   []metaChange `json:"changes"`
	Conflicts []metaConf   `json:"conflicts"`
}
type metaLine   struct { ID, Name, ParentLine, TipCommit, BaseCommit, Status string }
type metaChange struct { ID, LineID, Author, HeadCommit, Status string; Sealed, HasConflict bool }
type metaConf   struct { ID, ChangeID, Path, BaseBlob, ParentBlob, ChangeBlob, MarkedBlob, Status string }
```
(Use explicit `json` tags; `parent_line` may be NULL for the root → marshal as "" and restore as NULL on import.)

- [ ] **Step 2: `ExportMeta` (test first)**
```go
// ExportMeta serializes the cairn change-graph (lines, changes, conflicts) into a
// single git commit (refs/cairn/meta content) and returns its sha. Deterministic:
// unchanged catalogue → identical commit hash. The op-log/stash/bisect are NOT
// included (local-only).
func (e *Engine) ExportMeta() (string, error)
```
Read rows from `e.db` ORDER BY id (deterministic): lines (`SELECT id,name,COALESCE(parent_line,''),tip_commit,base_commit,status FROM line ORDER BY id`), changes (`SELECT id,line_id,author,head_commit,status,sealed,has_conflict FROM change ORDER BY id`), conflicts (`SELECT id,change_id,path,base_blob,parent_blob,change_blob,marked_blob,status FROM conflict ORDER BY id`). `json.MarshalIndent` (stable). `blob := writeBlob(jsonBytes)`; `tree := writeTreeRefs(map[string]TreeEntry{"meta.json": {SHA: blob, Mode: ModeRegular}})`; `commit := writeCommit(tree, "", "cairn-meta v1", nil)` — NOTE writeCommit appends a `Change-Id:` trailer using the changeID arg; pass a FIXED empty/sentinel so the meta commit is stable (or add a writeCommit variant without the trailer for meta — pin: pass changeID="" and confirm the trailer is deterministic, OR write the commit object directly without the trailer for determinism). Return the commit sha.
Test: `TestExportMetaDeterministic` — `ExportMeta()` twice on the same catalogue → identical sha. `TestExportMetaContent` — build a line tree (root → a → b) + a sealed change + a conflict row; ExportMeta; read back `meta.json` from the commit's tree (via `Files`/`readTree`); assert the JSON round-trips the lines (b.parent==a.id), changes (sealed/has_conflict), and the conflict.
(If writeCommit's `now`/identity makes the commit non-deterministic, write the meta commit with a FIXED zero timestamp + fixed identity so the hash is content-only — pin this; the meta commit is metadata, not history.)

- [ ] **Step 3: verify + commit** — `go test ./internal/change/ -run Meta -v` + full + vet/cross. Commit `feat(change): ExportMeta — serialize the change-graph to a refs/cairn/meta commit (fidelity task 1)`.

---

## Task 2: push writes the meta ref + fetch pulls refs/cairn/*

**Files:** modify `internal/change/push.go`, `internal/change/importer.go` (the fetch refspec); test `internal/change/meta_test.go` / a push test.

- [ ] **Step 1: fetch refspec includes refs/cairn/* (test first)**
In `fetchRemote`/`fetchTracking` (importer.go/sync.go — wherever the FetchOptions RefSpecs are built), add `+refs/cairn/*:refs/cairn/*` so a clone/fetch pulls the meta ref. (Confirm the existing refspec list and append.)

- [ ] **Step 2: push writes + pushes the meta for cairn remotes**
In `PushToRemote`, after resolving `kind := remoteKind(remoteName)`: if `kind == "cairn"`:
```go
	metaCommit, err := e.ExportMeta()
	if err != nil { return fmt.Errorf("change.PushToRemote: meta: %w", err) }
	// point the local ref at it
	if err := e.git.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName("refs/cairn/meta"), plumbing.NewHash(metaCommit))); err != nil { return ... }
	// add the meta refspec to the push
	refspecs = append(refspecs, config.RefSpec("refs/cairn/meta:refs/cairn/meta"))
```
(Match the REAL way PushToRemote builds its `[]config.RefSpec` + how it sets refs. The existing push pushes refs/heads/* + refs/tags/*; add the meta refspec only for cairn remotes. The meta commit's objects are pushed as part of the push.)

- [ ] **Step 3: test** — `TestPushWritesMetaForCairnRemote`: a cairn-kind remote (bare repo) → `PushToRemote` → the bare repo has `refs/cairn/meta`; a git-kind remote → NO `refs/cairn/meta` on the bare repo. (Use a local bare repo fixture like the existing push tests; `skipOnWindows`.)

- [ ] **Step 4: verify + commit** — full + vet/cross. Commit `feat(change): push refs/cairn/meta to cairn remotes; fetch refs/cairn/* (fidelity task 2)`.

---

## Task 3: import reconstructs the change-graph from the meta

**Files:** modify `internal/change/importer.go`; create `internal/change/meta_import_test.go`.

- [ ] **Step 1: `ImportMeta` reconstruct (test first)**
Add `func (e *Engine) importMeta(metaCommit string, tx *sql.Tx, ts string) error`: read `meta.json` from the meta commit's tree (`readTree`/`Files`); `json.Unmarshal`; **version guard** (`doc.Version != metaVersion` → "unsupported cairn metadata version N; upgrade cairn"); then, IN THE PASSED TX:
- `DELETE FROM change`; `DELETE FROM conflict`; `DELETE FROM line` (clear the init-created catalogue — the meta is authoritative for a fresh clone).
- INSERT each `metaLine` (parent_line = NULL when "" else the id; preserve tip/base/status).
- INSERT each `metaChange` (id, line_id, author, head_commit, status, sealed, has_conflict).
- INSERT each `metaConf` (the conflict columns).
Validate referenced commit shas resolve (`e.git.CommitObject` — at least the line tips) → a missing one is a clear error.

- [ ] **Step 2: wire into `ImportFromRemote`**
In `ImportFromRemote`, AFTER `fetchRemote`: check for `refs/cairn/meta` (resolve the ref; `e.git.Reference(plumbing.ReferenceName("refs/cairn/meta"), false)`); 
- if present → open the tx and call `importMeta(metaCommit, tx, ts)` INSTEAD of the flat-projection lines/changes block; then determine the default branch as the ROOT line's name (parent_line IS NULL) and proceed to the existing tail (express default etc.). 
- if absent → the existing flat projection (unchanged).
Return the default branch / root tip as the current code does. Keep it ONE atomic tx.

- [ ] **Step 3: tests** `internal/change/meta_import_test.go`:
`TestImportMetaFidelity`: build a source engine with a line tree (root → a → b) + a sealed change on each + an OPEN conflict on line `a`; `ExportMeta` → write the meta commit's objects into a SECOND fresh engine's git store (simulate the fetch — copy the objects, set refs/cairn/meta), then `importMeta` into the fresh engine; assert: the fresh engine's `line` rows reproduce the tree (b.parent_line == a.id, a.parent_line == root.id), the `change` rows reproduce ids+sealed+has_conflict, the `conflict` row is present with the right path/blobs. (If simulating the cross-engine fetch is hard at the unit level, do this as an e2e in Task 4 via real clone instead and keep Task 3's unit test to `importMeta` operating on a meta commit written into the same engine.)
`TestImportMetaVersionGuard`: a meta blob with `"version":2` → `importMeta` errors clearly.

- [ ] **Step 4: verify + commit** — full + vet/cross. Commit `feat(change): import reconstructs the change-graph from refs/cairn/meta (fidelity task 3)`.

---

## Task 4: clone round-trip e2e + gate

**Files:** create `cmd/cairn/fidelity_e2e_test.go`.

- [ ] **Step 1: round-trip e2e (test first)** (`skipOnWindows`): 
  - init a repo `A`; build a real line tree via the CLI: express `a` from root, express `b` from `a`; commit on root, `a`, and `b` (distinct content); force an OPEN conflict on `a` (commit on `a` and on root touching the same line so merge-forward conflicts — mirror an existing conflict e2e).
  - `cairn remote add --cairn origin <bare>` (a local bare repo); `cairn push --repo A origin`.
  - `cairn clone <bare> B` into a fresh dir.
  - Assert on `B`: `cairn tree --repo B` shows the REAL tree (b under a under root, not flattened); `cairn log --repo B a`/`b` show the right commits; `cairn status --repo B a` shows the open conflict (proves the conflict row round-tripped); the change-ids match (capture from A's `cairn log` and compare on B).
  - Contrast `TestCloneGitRemoteFlat`: add the SAME bare as a plain `git` remote (no `--cairn`), push, clone → the flat projection (a and b both children of root) — confirms the non-fidelity path is unchanged.
  - `TestNormalGitCloneStillWorks`: a plain `git clone` (or go-git PlainClone) of the cairn-pushed bare repo still sees sane `refs/heads`/`refs/tags` (the meta ref is inert).

- [ ] **Step 2: gate + usage note** — update any `remote add --cairn` help to note it enables full-fidelity push. Full `go test ./...` + `go vet ./...` + cross-compile darwin/windows. Commit `feat(cmd): cairn->cairn clone round-trip fidelity e2e + docs (fidelity task 4)`.

## Notes
- **The meta commit must be deterministic** (fixed timestamp/identity, no Change-Id trailer churn) so an unchanged catalogue produces an identical hash — pin this in Task 1; it makes re-pushes idempotent.
- **op-log/stash/bisect are NOT in the meta** (local-only, like git's reflog).
- Import is **clone-fidelity**: it CLEARS the init catalogue and installs the meta verbatim (origin IDs). Pull-meta-merge (reconciling into an existing local catalogue) is deferred.
- A plain git remote round-trips as today (flat projection) — fidelity strictly follows the presence of `refs/cairn/meta`.
- Atomic: the whole import is one tx; a failure leaves the clone uninitialized, not half-built.
- DRY, YAGNI, TDD.
