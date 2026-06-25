# cairn — standalone CLI release packaging (CI/CD)

**Status:** approved · 2026-06-25
**Goal:** Ship the `cairn` command-line tool as downloadable standalone archives for every environment, so the operator can install it on a work machine and dogfood cairn outside this dev tree. A version-tag push builds the cross-platform matrix, packages per-OS archives + checksums, and publishes them on a GitHub Release.

**Decisions (pinned):**
- **Archives, not native installers.** `.tar.gz` for linux/darwin, `.zip` for windows — the universal CLI-distribution format (`brew`/`scoop`/`curl | install` consume exactly this). No `.dmg`/`.msi` (a CLI still has to land on `PATH`; native installers add runner + signing complexity for no real benefit at dogfood scale).
- **GoReleaser** drives build+archive+checksum+release from one `.goreleaser.yaml`. cairn is pure-Go CGO-free, so the whole matrix cross-compiles on a single `ubuntu-latest` runner — no per-OS runners.
- **CLI only.** Package `cmd/cairn`. `cmd/cairn-server` ships as the container image (`release-image.yml`), unchanged.
- **No code-signing / notarization.** Correct for solo dogfooding; proper signing needs an Apple Developer ID + a Windows cert + CI secrets — out of scope. The Release notes carry the one-time macOS unquarantine line.
- **Sits alongside** the existing `release-image.yml` (also `v*`-tag-triggered). A new `release.yml` adds the standalone-CLI half; they don't conflict.

---

## 1. Build-version stamp (`cairn --version`)
cairn already has a `version` **subcommand** that derives the *repo's* semver from the change graph — that stays as-is. This adds a top-level `--version` (and `-v`) **flag** reporting the *build* version of the binary itself, so a dogfooder can identify exactly which release they run.

- `cmd/cairn/main.go`: a package var `var buildVersion = "dev"`. GoReleaser overrides it at link time via `-ldflags "-X main.buildVersion=<tag>"`.
- In `run()`, before the subcommand switch: if the first arg is `--version` or `-v`, print `cairn <buildVersion>` to stdout and return nil. (Placed so it can never be shadowed by a subcommand; `version` the subcommand is untouched.)
- Default `dev` for `go build`/`go install` without ldflags (and for `go run`).

## 2. `.goreleaser.yaml` (repo root)
GoReleaser v2 schema (`version: 2`).
- `project_name: cairn`.
- `before.hooks`: `go mod tidy`.
- `builds`: a single build —
  - `id: cairn`, `main: ./cmd/cairn`, `binary: cairn`.
  - `env: [CGO_ENABLED=0]`.
  - `goos: [linux, darwin, windows]`, `goarch: [amd64, arm64]` → 6 targets (no excludes; all six are valid).
  - `ldflags: ["-s -w -X main.buildVersion={{.Version}}"]` (`-s -w` strips debug info → smaller binary; `{{.Version}}` is the tag minus the leading `v`).
  - `mod_timestamp: "{{ .CommitTimestamp }}"` for reproducible builds.
- `archives`: one archive set —
  - `formats: [tar.gz]`, with `format_overrides: [{ goos: windows, formats: [zip] }]`.
  - `name_template: "cairn_{{ .Version }}_{{ .Os }}_{{ .Arch }}"`.
  - `files: [LICENSE, README.md]` bundled alongside the binary.
- `checksum`: `name_template: "checksums.txt"` (SHA-256 over every archive).
- `changelog`: `use: github`, sort `asc`; group `Features`/`Fixes`/`Others` by conventional-commit prefix.
- `release`: GitHub release; `footer` carries the install snippet + the macOS unquarantine note (see §4).
- `snapshot.version_template: "{{ incpatch .Version }}-snapshot"` so local `--snapshot` runs work off an untagged tree.

## 3. `.github/workflows/release.yml`
- `name: release`.
- `on: { push: { tags: ['v*'] }, workflow_dispatch: {} }` (tag push publishes; manual dispatch is a dry run — see below).
- `permissions: { contents: write }` (GoReleaser creates the Release + uploads assets via the default `GITHUB_TOKEN`).
- One job `goreleaser` on `ubuntu-latest`:
  1. `actions/checkout@v4` with `fetch-depth: 0` (GoReleaser needs full history + tags for the changelog/version).
  2. `actions/setup-go@v5` with `go-version: '1.26'`.
  3. `goreleaser/goreleaser-action@v6` —
     - on a tag push: `args: release --clean` (real publish).
     - on `workflow_dispatch`: `args: release --snapshot --clean` (build + archive, **no** publish) so the matrix can be validated without cutting a release.
     - `env: { GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }} }`.

## 4. Release notes footer (install instructions)
Bundled into `release.footer` so every Release page tells the dogfooder how to install:
- **Linux/macOS:** download the matching `.tar.gz`, `tar xzf`, move `cairn` onto `PATH` (e.g. `/usr/local/bin`).
- **macOS Gatekeeper:** `xattr -d com.apple.quarantine ./cairn` once (binary is unsigned) — or right-click → Open.
- **Windows:** download the `.zip`, extract `cairn.exe`, add its folder to `PATH`.
- **Verify:** `cairn --version` should print the release tag; check the download against `checksums.txt`.

## 5. Out of scope (later, if dogfooding sticks)
- Code-signing / notarization (Apple Developer ID, Windows cert).
- Homebrew tap, scoop bucket, `.deb`/`.rpm` (`nfpms`), a `curl | sh` installer.
- arm64 Windows is included but untested hardware-side; drop it if it ever fails to build.
- Auto-bumping the tag / release-please style automation — tags are cut by hand for now.

## 6. Testing / DoD
- `cairn --version` prints `cairn dev` on a plain `go build`; prints the injected value when built with `-ldflags -X main.buildVersion=X` (unit-testable by building, or by a small test that the flag is wired — covered by a Go test on the flag dispatch returning the version string via an injectable writer).
- `go test ./...`, `go vet ./...`, and cross-compile (`GOOS=darwin/windows go build ./...`) stay green.
- `goreleaser check` passes on `.goreleaser.yaml` (run in CI-equivalent locally if goreleaser is available; otherwise the workflow's `workflow_dispatch` snapshot run validates it end-to-end).
- A local `goreleaser release --snapshot --clean` (when available) produces the six archives + `checksums.txt` under `dist/`, each containing a runnable `cairn` reporting the snapshot version, plus `LICENSE`/`README.md`.
- The existing `release-image.yml` is unaffected (no edits); both trigger on `v*` independently.
