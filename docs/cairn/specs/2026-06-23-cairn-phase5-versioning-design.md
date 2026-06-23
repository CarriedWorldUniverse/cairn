# cairn Phase 5 — versioning & publishing (derived versions, no fat-fingering)

**Status:** draft for approval · 2026-06-23
**Goal:** every artifact cairn publishes gets a **deterministically derived** semantic version computed from the change-graph — never hand-typed — consistent across npm/NuGet/PyPI/OCI/Go, monotonic, collision-free; plus an **atomic `cairn release`** (tag + manifest stamp + publish) with guardrails so a tag, a manifest, and a published artifact can never disagree.
**Builds on:** Phase 1 only — tags (`ListTags`/`Tag`), the change-graph (`e.git` ancestry, `mergeBase`), lines (`RootLine`/`GetLineage`), and the `config` store (reused for bump intent). **No** dependency on porter/ledger/privacy.
**Canon:** convergence-core spec §12 (operator-approved). This slice implements **NEX-717, 718, 719, 720, 721**. **NEX-722 (two-path embargoed/public) is OUT** — it needs the Phase-3 privacy projection + a cairn server, neither built.

---

## 1. Scope

**IN (Slice 1):**
1. **Version-derivation engine** (`internal/version`, pure): `Derive(input) → Canonical` — a pure function of (graph position, reachable tags, config, bump intent). (NEX-717)
2. **`cairn.version` config** (`internal/version/config.go`): GitVersion.yml-shaped YAML — tag prefix, default increment, line→pre-release conventions, per-ecosystem rendering map; sensible defaults when absent. (NEX-718)
3. **Per-ecosystem rendering** (`internal/version/render.go`): one canonical semver rendered per target — npm/NuGet (semver2), PyPI (PEP 440), OCI tag, Go module tag. (NEX-719)
4. **`cairn version` / `cairn version bump`** CLI (NEX-720): `cairn version [--target <eco>]` prints the derived version (machine-readable, CI-consumable, no human types a version); `cairn version bump major|minor|patch` records explicit bump intent for the next release (persisted in the `config` store; consumed + cleared by `cairn release`).
5. **`cairn release`** (`internal/release`, NEX-721): one **atomic** op = guardrails → stamp manifest → tag → **publish (last, irreversible)**, with rollback of the local steps on any pre-publish failure. `--target npm|nuget|pypi|oci`, `--dry-run`. Guardrails: refuse a version that already exists (registry **and** tags), enforce monotonicity per line, refuse a dirty/uncommitted tree.

**Bump-intent source (Slice 1, operator decision):** explicit `cairn version bump <level>` + a configured **default increment** (patch). **Conventional-commit auto-parsing** (`feat:`/`fix:`/`feat!:`/`BREAKING CHANGE`) is **deferred** to a follow-up — the derivation engine takes a `PendingBump` input so adding an auto-detector later is a new caller, not an engine change.

**OUT (deferred):**
- **NEX-722** two-path embargoed/public release (Phase-3 privacy + server).
- Conventional-commit auto-increment (follow-up; engine seam left open).
- Real network probes against live registries in tests (a `RegistryProbe` interface is mocked; the exec/real impl ships but is unit-tested by argv construction, not by hitting a registry).
- Monorepo / multi-package per-repo versioning (one canonical version per repo for now).

---

## 2. Derivation (the pure core) — NEX-717

`Derive` is a **pure function**; all graph access happens in an engine helper that feeds it. Same commit + same config ⇒ same version, always.

```go
// internal/version
type Canonical struct {
    Major, Minor, Patch int
    PreRelease []string // e.g. ["exp-idea","5"]; empty on a trunk release
    Build      []string // e.g. ["5","g1a2b3c4"] (height + short-sha); never affects precedence
}

type DeriveInput struct {
    BaseTag     string // nearest reachable tag, e.g. "v1.4.0" ("" if none)
    Distance    int    // commits from the target commit back to BaseTag's commit (0 = on the tag)
    LineName    string // the line being versioned
    IsTrunk     bool   // target line is the structural root
    LineDistance int   // commits since the line's branch point (height on the line)
    PendingBump string // "major"|"minor"|"patch"|"" (explicit intent; else config default)
    ShortSHA    string // target commit short sha, for build metadata
    Config      Config
}

func Derive(in DeriveInput) (Canonical, error)
```

**Rules (pin, matching §12 examples `1.4.1` trunk / `1.4.1-exp-idea.5` line):**
- Parse `BaseTag` minus `Config.TagPrefix` → `(maj,min,pat)`; absent ⇒ `0.0.0`.
- `bump = PendingBump` if set, else `Config.DefaultIncrement` (default `patch`).
- **Distance == 0 (target IS a tagged commit):** the **release** version = the tag as-is, **no bump, no pre-release** (`1.4.0`). This is the canonical released artifact.
- **Distance > 0:** apply `bump` to the base core → next core (`patch`: `1.4.0`→`1.4.1`).
  - **Trunk (`IsTrunk`):** render the next core as a **release** (`1.4.1`) per §12, with **height + sha in Build metadata** (`+<Distance>.g<ShortSHA>`) for traceability/uniqueness. (Trunk precedence can tie between two off-tag commits; a real release is cut by tagging. A future `trunk-prerelease` config mode can switch this to a precedence-ordered pre-release — out of scope.)
  - **Experiment line:** a true **pre-release** `1.4.1-<label>.<LineDistance>` (`label` = sanitized `LineName`), precedence-ordered and **collision-free** across lines (distinct label) and vs trunk. Build metadata `+g<ShortSHA>`.

**DoD:** deterministic across runs; two lines never collide; monotonic per line as distance grows; on-tag commit yields the exact release version.

### Engine helper (graph access) — `internal/change/describe.go` (new)
```go
// DescribeVersion walks first-parent ancestry from `commit` to the nearest commit
// carrying a tag, returning that tag's name and the commit distance (0 = `commit`
// itself is tagged). No tag found ⇒ ("", <total ancestry length>, nil).
func (e *Engine) DescribeVersion(commit string) (baseTag string, distance int, err error)
```
- Build a `map[commitSHA]tagName` from `ListTags()`; walk `e.git.CommitObject(...).Parents()` first-parent, counting until a tagged commit is hit. Linear Phase-1 ancestry (single-parent except merge commits — take parent[0]) makes this unambiguous; cap the walk at a sane bound and surface a clear error if exceeded.
- `LineDistance`: `mergeBase(lineTip, parentLineTip)` then count tip→base (reuse the same first-parent walk); trunk ⇒ `LineDistance` from root.

---

## 3. Config — NEX-718  (`internal/version/config.go`)

GitVersion.yml-shaped YAML, loaded from `cairn.version` at the repo root; **all fields optional** with defaults.

```yaml
tag-prefix: v                 # default "v"
default-increment: patch      # major|minor|patch, default patch
lines:                        # optional per-line-name overrides (glob or exact)
  "exp-*": { prerelease: true }
ecosystems:                   # rendering targets present in this repo
  npm:    { manifest: package.json }
  pypi:   { manifest: pyproject.toml }
  nuget:  { manifest: "*.csproj" }
  oci:    {}
```

```go
type Config struct {
    TagPrefix        string
    DefaultIncrement string            // validated ∈ {major,minor,patch}
    Lines            []LineRule        // name-glob → prerelease bool
    Ecosystems       map[string]EcoCfg // eco name → { Manifest string }
}
func LoadConfig(repoRoot string) (Config, error) // missing file ⇒ Defaults(), nil
func (Config) Validate() error
func DefaultConfig() Config
```
- Parser: `gopkg.in/yaml.v3` (add dep). Unknown keys ⇒ ignored (forward-compat) but **invalid enum values ⇒ error**. Missing file ⇒ defaults, no error.
- **DoD:** parsed + validated; sensible defaults absent; rule changes reflected deterministically in `Derive`.

---

## 4. Per-ecosystem rendering — NEX-719  (`internal/version/render.go`)

```go
func Render(v Canonical, eco string) (string, error) // eco ∈ {npm,nuget,pypi,oci,go}
```
- **npm / nuget** → **semver2**: `MAJOR.MINOR.PATCH[-pre.parts][+build.parts]`, e.g. `1.4.1`, `1.4.1-exp-idea.5`.
- **pypi** → **PEP 440**: release `1.4.1`; a `<label>.<n>` pre-release → **dev release** `1.4.1.dev5` (n = last numeric pre-release part); an `rc`-labelled pre-release → `1.4.1rc1`; build metadata → PEP 440 **local** segment `+g1a2b3c4` (normalized: `_`→`.`, lowercase). (PEP 440 is the one real divergence — covered by dedicated tests.)
- **oci** (container tag) → semver2 string with `+`→`_` (OCI tags forbid `+`); `/` never produced.
- **go** (module tag) → `vMAJOR.MINOR.PATCH[-pre]`, **build metadata dropped** (Go module versions disallow `+meta`).
- **DoD:** round-trip canonical→target per ecosystem; pre-release/build mapped correctly (esp. PEP 440 divergence) — table-driven tests.

---

## 5. `cairn version` / `cairn version bump` — NEX-720  (CLI)

- `cairn version [--repo DIR] [--target npm|nuget|pypi|oci|go]`: resolve the current line (from the expressed folder / `--repo`), call `DescribeVersion` + `Derive`, then `Render` for `--target` (default: the canonical semver2 string). **Stdout = the version only** (scriptable; CI consumes `$(cairn version)`); diagnostics to stderr. Exit 0.
- `cairn version bump major|minor|patch [--repo DIR]`: `SetConfig("version.pending_bump", level)`. Printed confirmation to stderr. The next `cairn release` (or `cairn version`) reads it as `PendingBump`; **`cairn release` clears it** on success (`SetConfig("version.pending_bump","")`).
- **Reuse** the existing `config` store (`GetConfig`/`SetConfig`) — no new table.
- **DoD:** `cairn version` output stable + scriptable; bump intent honoured by `Derive` on next release, then cleared.

---

## 6. `cairn release` — NEX-721  (`internal/release`)

One **atomic** operation. **Publish is last because it is the only irreversible step** — every local mutation before it is rolled back on failure.

```
cairn release [--target npm|nuget|pypi|oci] [--repo DIR] [--dry-run]
```
**Order:**
1. **Derive** the version (§2) + **Render** for the target (§4). Resolve the manifest path from config.
2. **Guardrails** (all must pass, else abort with a clear message, no mutation):
   - **dirty tree** — the expressed working folder has no uncommitted changes (reuse worktree `Status`/`Scan`); refuse if dirty.
   - **monotonicity** — derived version strictly greater (semver precedence) than the latest tag reachable on this line; refuse otherwise.
   - **already-exists** — the version is not already a tag, and not already on the target registry (`RegistryProbe.Exists(eco, name, version)`); refuse if either.
3. **Stamp** the manifest — write the rendered version into `package.json` / `*.csproj` / `pyproject.toml` (ecosystem-specific stamper). Local, reversible (keep original bytes).
4. **Tag** — `engine.Tag(prefix+canonical, lineTip, author)`. Local, reversible (track it).
5. **Publish** — `Publisher.Publish(eco, manifestDir)` execs the registry tool. **Last. Irreversible.**
6. **On failure** at step 3/4 → restore manifest, delete tag, abort. At step 5 (publish) → restore manifest + delete tag, report a clean partial-failure (nothing local left inconsistent; the registry simply didn't receive the artifact).
7. **Success** → clear `version.pending_bump`. Print the released version + target.

`--dry-run`: run steps 1–2 and **print the plan** (version, rendered string, tag name, manifest edit, publish command) — **no mutation, no publish**.

### Seams (real publish, hermetic tests)
```go
type Publisher interface { Publish(eco, dir string) error }     // ExecPublisher = real (npm publish / twine upload / dotnet nuget push / docker push)
type RegistryProbe interface { Exists(eco, name, version string) (bool, error) } // ExecProbe = real
```
- **`ExecPublisher`** builds + runs the ecosystem's native publish argv (`npm publish`, `python -m twine upload`, `dotnet nuget push`, `docker push <ref>`). Shipped + used in production.
- Tests inject a **`fakePublisher`** (captures argv, can be made to fail) and a **`fakeProbe`** — CI never touches a live registry. `ExecPublisher`/`ExecProbe` are unit-tested by **argv construction** (the command they *would* run), not by execution.
- Manifest stampers are pure (bytes→bytes), table-tested per ecosystem.
- **DoD:** atomicity (no partial release — publish last, rollback proven via a failing fakePublisher leaving tag+manifest reverted); all three guardrails enforced (unit tests each); `--dry-run` shows the plan and mutates nothing.

---

## 7. File structure

```
internal/version/         (pure — no I/O beyond config file read)
  semver.go      Canonical type, Parse, String, Compare (precedence)
  derive.go      DeriveInput, Derive (the pure function)
  config.go      Config, LoadConfig, Validate, DefaultConfig
  render.go      Render(Canonical, eco)
  *_test.go
internal/change/
  describe.go    Engine.DescribeVersion(commit) (baseTag, distance, err) + line height
  describe_test.go
internal/release/         (side-effecting, all behind seams)
  release.go     Release(opts) orchestration + rollback
  manifest.go    ecosystem manifest stampers (package.json/.csproj/pyproject.toml)
  publish.go     Publisher + ExecPublisher (real argv)
  guardrails.go  RegistryProbe + ExecProbe + monotonicity + dirty-tree
  *_test.go
cmd/cairn/
  main.go        + cases "version", "release"; cmdVersion / cmdRelease
  version_e2e_test.go / release_e2e_test.go
```
New dep: `gopkg.in/yaml.v3`. Reuse: `internal/change` (tags, graph, config store), `internal/worktree` (Status/Scan for dirty-tree, line resolution).

---

## 8. Build sequence (for the plan)

1. **`semver.go`** — Canonical + Parse + String + Compare. TDD (precedence incl. pre-release < release, build ignored).
2. **`describe.go`** — `DescribeVersion` engine helper (nearest tag + distance + line height). TDD on a built graph.
3. **`config.go`** — Config + LoadConfig + Validate + defaults. TDD (missing file → defaults; bad enum → error).
4. **`derive.go`** — `Derive`. TDD (on-tag release; trunk off-tag; line pre-release; pending-bump override; two-lines-no-collide; monotonic).
5. **`render.go`** — per-ecosystem rendering. TDD table (semver2/PEP440/OCI/Go).
6. **`cairn version` + `version bump`** CLI + e2e. TDD.
7. **`internal/release`** — manifest stampers (TDD pure), Publisher/Probe seams, guardrails (TDD each), `Release` orchestration + rollback (TDD with fakes). 
8. **`cairn release`** CLI (`--target`, `--dry-run`) + e2e (dry-run plan; success with fakePublisher; failing-publish rollback). TDD.

`skipOnWindows` on local-fixture/exec tests as established; full + cross-compile gate; all prior phases green.

**DoD (slice):** `cairn version` prints a deterministic, CI-consumable derived version per ecosystem, never hand-typed; `cairn release` atomically tags + stamps + publishes (publish last, full rollback on failure) with all three guardrails; `--dry-run` shows the plan; CI green linux/mac/win.

---

## 9. Open questions (small, non-blocking)

- **Trunk off-tag precedence:** Slice 1 renders trunk as the release core + height-in-build-metadata (§2) per §12's literal `1.4.1`. A `trunk-prerelease` config mode (precedence-ordered `-ci.N`) is a later toggle. Pinned: build-metadata height for now.
- **Pre-release label sanitization:** `LineName` → semver pre-release identifier (`[0-9A-Za-z-]`, `/`→`-`, lowercase). Pin in step 4.
- **Multi-target release:** Slice 1 is one `--target` per invocation (CI loops). A single-call multi-target fan-out is a later convenience.
- **`pending_bump` scope:** stored repo-global in the `config` store (one canonical version per repo). Per-line bump intent is a later refinement if monorepo lands.
