# cairn release packaging — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** A `v*`-tag push cross-builds `cmd/cairn` for linux/darwin/windows × amd64/arm64, packages per-OS archives (`.tar.gz`/`.zip`) + `checksums.txt`, and publishes them on a GitHub Release; the binary reports its build version via `cairn --version`.

**Architecture:** GoReleaser (`.goreleaser.yaml`) builds+archives+checksums+releases on one `ubuntu-latest` runner (pure-Go CGO-free cross-compile). A new `.github/workflows/release.yml` runs it. A `buildVersion` package var in `cmd/cairn/main.go`, ldflag-injected, backs a new top-level `--version` flag (distinct from the existing `version` subcommand).

**Tech:** Go 1.26.3, GoReleaser v2, GitHub Actions. Spec: `docs/cairn/specs/2026-06-25-release-packaging.md`.

**Conventions:** errors `pkg.Func: %w`; one concern per commit; full gate (`go test ./...` + `go vet ./...` + cross-compile darwin/windows) before each commit.

---

## Task 1: `cairn --version` build stamp

**Files:**
- Modify: `cmd/cairn/main.go` (add `buildVersion` var + `--version`/`-v` handling in `run()`).
- Test: `cmd/cairn/main_test.go` (or the existing cmd test file — append).

- [ ] **Step 1: Write the failing test.** `run()` writes to stdout via `fmt.Println`; to test without capturing process stdout, assert on the dispatch path. Add a test that calls `run([]string{"--version"})` and expects `nil` error, AND a test that `buildVersion` defaults to `"dev"`. If stdout capture is already a pattern in the cmd tests, capture it and assert the line is `cairn dev`. Minimal:
```go
func TestVersionFlag(t *testing.T) {
	if err := run([]string{"--version"}); err != nil {
		t.Fatalf("run(--version) = %v, want nil", err)
	}
	if err := run([]string{"-v"}); err != nil {
		t.Fatalf("run(-v) = %v, want nil", err)
	}
	if buildVersion != "dev" {
		t.Fatalf("buildVersion = %q, want dev (default)", buildVersion)
	}
}
```
- [ ] **Step 2: Run it, watch it fail** — `go test ./cmd/cairn/ -run TestVersionFlag -v` → FAIL (`buildVersion` undefined / `--version` returns "no subcommand" error).
- [ ] **Step 3: Implement.** In `cmd/cairn/main.go`:
  - Add near the other package vars: `// buildVersion is the release version, injected at link time by GoReleaser\n// (-ldflags "-X main.buildVersion=..."). Defaults to "dev" for go build/run.\nvar buildVersion = "dev"`.
  - In `run()`, immediately after `sub, rest := args[0], args[1:]` (before `switch sub`), add:
```go
	if sub == "--version" || sub == "-v" {
		fmt.Println("cairn", buildVersion)
		return nil
	}
```
  (Placed before the switch so it can't collide with the `version` subcommand.)
- [ ] **Step 4: Run the test** → PASS. Then full gate: `go test ./... && go vet ./... && GOOS=darwin go build ./... && GOOS=windows go build ./...`.
- [ ] **Step 5: Commit** — `feat(cmd): cairn --version reports the ldflag-injected build version (packaging task 1)`.

---

## Task 2: `.goreleaser.yaml`

**Files:**
- Create: `.goreleaser.yaml` (repo root).

- [ ] **Step 1: Write the config** (GoReleaser v2 schema) exactly:
```yaml
version: 2
project_name: cairn

before:
  hooks:
    - go mod tidy

builds:
  - id: cairn
    main: ./cmd/cairn
    binary: cairn
    env:
      - CGO_ENABLED=0
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    mod_timestamp: "{{ .CommitTimestamp }}"
    ldflags:
      - -s -w -X main.buildVersion={{ .Version }}

archives:
  - id: cairn
    formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
    name_template: "cairn_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    files:
      - LICENSE
      - README.md

checksum:
  name_template: "checksums.txt"

snapshot:
  version_template: "{{ incpatch .Version }}-snapshot"

changelog:
  use: github
  sort: asc
  groups:
    - title: Features
      regexp: '^.*?feat(\(.+\))??!?:.+$'
      order: 0
    - title: Fixes
      regexp: '^.*?fix(\(.+\))??!?:.+$'
      order: 1
    - title: Others
      order: 999

release:
  footer: |
    ## Install

    **Linux / macOS** — download the matching `.tar.gz`, then:
    ```
    tar xzf cairn_*_<os>_<arch>.tar.gz
    sudo mv cairn /usr/local/bin/
    cairn --version
    ```
    **macOS Gatekeeper** (binaries are unsigned): clear quarantine once —
    ```
    xattr -d com.apple.quarantine /usr/local/bin/cairn
    ```
    or right-click the binary → Open.

    **Windows** — download the `.zip`, extract `cairn.exe`, add its folder to `PATH`, then `cairn --version`.

    Verify your download against `checksums.txt` (SHA-256).
```
- [ ] **Step 2: Validate.** If `goreleaser` is installed: `goreleaser check` → no errors; `goreleaser release --snapshot --clean` → `dist/` has 6 archives + `checksums.txt`; untar one and run `./cairn --version` → prints `cairn <X>-snapshot`. If goreleaser is NOT available locally, validate the YAML parses (`python3 -c "import yaml,sys; yaml.safe_load(open('.goreleaser.yaml'))"`) and rely on the workflow's `workflow_dispatch` snapshot run.
- [ ] **Step 3: Commit** — `build: goreleaser config for standalone cairn CLI archives (packaging task 2)`.

---

## Task 3: `.github/workflows/release.yml`

**Files:**
- Create: `.github/workflows/release.yml`.

- [ ] **Step 1: Write the workflow** exactly:
```yaml
# Build + publish the standalone cairn CLI as per-OS archives on a version tag.
# Runs alongside release-image.yml (which ships the cairn-server container).
name: release
on:
  push:
    tags: ['v*']
  workflow_dispatch: {}
permissions:
  contents: write
jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'
      - uses: goreleaser/goreleaser-action@v6
        with:
          version: '~> v2'
          args: >-
            ${{ github.event_name == 'workflow_dispatch'
                && 'release --snapshot --clean'
                || 'release --clean' }}
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```
- [ ] **Step 2: Validate YAML** parses (`python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release.yml'))"`). Confirm the existing `.github/workflows/release-image.yml` is unchanged.
- [ ] **Step 3: Commit** — `ci: tag-triggered release workflow packaging the cairn CLI (packaging task 3)`.

---

## Task 4: README install section + gate

**Files:**
- Modify: `README.md` (a short "Install" section pointing at the Releases page + `cairn --version`).

- [ ] **Step 1:** Add a concise Install section to `README.md`: download from the GitHub Releases page, the `.tar.gz`/`.zip` per OS, the macOS unquarantine note, and `cairn --version` to verify. Keep it to ~10 lines; do not duplicate the full goreleaser footer.
- [ ] **Step 2: Full gate** — `go test ./... && go vet ./... && GOOS=darwin go build ./... && GOOS=windows go build ./...` all green.
- [ ] **Step 3: Commit** — `docs: README install section for the standalone CLI (packaging task 4)`.

## Notes
- The leading `v` is stripped from `{{ .Version }}` by GoReleaser, so `cairn --version` on a `v0.1.0` release prints `cairn 0.1.0`.
- `workflow_dispatch` is a safe dry run: `--snapshot` never publishes a Release, so the matrix/config can be exercised before cutting `v0.1.0`.
- First real release: `git tag v0.1.0 && git push origin v0.1.0` → the workflow publishes the archives.
- DRY, YAGNI, TDD.
