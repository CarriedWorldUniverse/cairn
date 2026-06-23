# Cairn Phase 5 — Versioning & Publishing (Slice 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deterministically derive a semantic version from the change-graph (never hand-typed), render it per ecosystem (npm/NuGet/PyPI/OCI/Go), expose `cairn version`/`version bump`, and ship an atomic `cairn release` (tag + manifest stamp + publish-last + rollback) with guardrails.

**Architecture:** A pure `internal/version` package (semver type, derivation, config, rendering — no I/O beyond reading the config file). A new `internal/change/describe.go` engine helper walks the graph for nearest-tag/distance/line-height. A side-effecting `internal/release` package does manifest stamping + publishing behind `Publisher`/`RegistryProbe` interfaces (real exec impls, mockable in tests). `internal/worktree` gains thin accessors and a `DeriveInput` assembler. `cmd/cairn` gains `version` and `release` subcommands.

**Tech Stack:** Go 1.26.3, go-git v5.13.2, modernc sqlite, `gopkg.in/yaml.v3` (new). Reuses `internal/change` (tags/graph/config store) + `internal/worktree`.

**Spec:** `docs/cairn/specs/2026-06-23-cairn-phase5-versioning-design.md` (NEX-717..721; NEX-722 deferred).

**Conventions (match existing code):** errors wrapped `pkg.Func: %w`; tests table-driven where natural; `skipOnWindows(t)` on local-fixture/exec tests; one tx per catalogue mutation (engine already does this — describe.go is read-only). Commit after each task.

---

## Task 1: Canonical semver type (`internal/version/semver.go`)

**Files:**
- Create: `internal/version/semver.go`
- Test: `internal/version/semver_test.go`

The pure value type + parse + render + precedence compare. No graph, no I/O.

- [ ] **Step 1: Write the failing test**

```go
package version

import "testing"

func TestCanonicalString(t *testing.T) {
	for _, tc := range []struct {
		v    Canonical
		want string
	}{
		{Canonical{Major: 1, Minor: 4, Patch: 1}, "1.4.1"},
		{Canonical{Major: 1, Minor: 4, Patch: 1, PreRelease: []string{"exp-idea", "5"}}, "1.4.1-exp-idea.5"},
		{Canonical{Major: 1, Minor: 4, Patch: 1, Build: []string{"5", "g1a2b3c4"}}, "1.4.1+5.g1a2b3c4"},
		{Canonical{Major: 0, Minor: 0, Patch: 1, PreRelease: []string{"x", "2"}, Build: []string{"gabc"}}, "0.0.1-x.2+gabc"},
	} {
		if got := tc.v.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}

func TestParseCanonical(t *testing.T) {
	v, err := Parse("v1.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if v.Major != 1 || v.Minor != 4 || v.Patch != 0 {
		t.Fatalf("got %+v", v)
	}
	if _, err := Parse("not.a.version"); err == nil {
		t.Error("expected error for bad version")
	}
}

func TestComparePrecedence(t *testing.T) {
	// release > its own pre-release; build metadata ignored; numeric ordering.
	mk := func(s string) Canonical { v, _ := Parse(s); return v }
	cases := []struct {
		a, b string
		sign int // sign of Compare(a,b)
	}{
		{"1.4.1", "1.4.0", 1},
		{"1.4.1", "1.4.1", 0},
		{"1.4.1-exp.5", "1.4.1", -1},   // pre-release < release
		{"1.4.1-exp.5", "1.4.1-exp.4", 1},
		{"1.4.1+aaa", "1.4.1+bbb", 0},  // build metadata ignored in precedence
		{"2.0.0", "1.9.9", 1},
	}
	for _, tc := range cases {
		got := Compare(mk(tc.a), mk(tc.b))
		if sign(got) != tc.sign {
			t.Errorf("Compare(%q,%q)=%d, want sign %d", tc.a, tc.b, got, tc.sign)
		}
	}
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	default:
		return 0
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/version/ -run 'TestCanonicalString|TestParseCanonical|TestComparePrecedence' -v`
Expected: FAIL (package/type not defined).

- [ ] **Step 3: Implement**

```go
// Package version derives deterministic semantic versions from the cairn
// change-graph and renders them per packaging ecosystem. Pure: the only I/O is
// reading the cairn.version config file (config.go).
package version

import (
	"fmt"
	"strconv"
	"strings"
)

// Canonical is one logical semantic version. PreRelease and Build are dot-joined
// identifier lists (semver2). Build metadata never affects precedence.
type Canonical struct {
	Major, Minor, Patch int
	PreRelease          []string
	Build               []string
}

func (v Canonical) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.PreRelease) > 0 {
		s += "-" + strings.Join(v.PreRelease, ".")
	}
	if len(v.Build) > 0 {
		s += "+" + strings.Join(v.Build, ".")
	}
	return s
}

// Parse reads a semver core (with optional "v" prefix, pre-release, build). It is
// lenient on the prefix and strict on the X.Y.Z core.
func Parse(s string) (Canonical, error) {
	orig := s
	s = strings.TrimPrefix(s, "v")
	var v Canonical
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if s[i+1:] != "" {
			v.Build = strings.Split(s[i+1:], ".")
		}
		s = s[:i]
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		if s[i+1:] != "" {
			v.PreRelease = strings.Split(s[i+1:], ".")
		}
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Canonical{}, fmt.Errorf("version.Parse: %q is not MAJOR.MINOR.PATCH", orig)
	}
	var err error
	if v.Major, err = atoi(parts[0]); err != nil {
		return Canonical{}, fmt.Errorf("version.Parse %q: %w", orig, err)
	}
	if v.Minor, err = atoi(parts[1]); err != nil {
		return Canonical{}, fmt.Errorf("version.Parse %q: %w", orig, err)
	}
	if v.Patch, err = atoi(parts[2]); err != nil {
		return Canonical{}, fmt.Errorf("version.Parse %q: %w", orig, err)
	}
	return v, nil
}

func atoi(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid numeric segment %q", s)
	}
	return n, nil
}

// Compare returns -1/0/1 by semver2 precedence. Build metadata is ignored; a
// pre-release sorts below the same core release; pre-release identifiers compare
// numeric<numeric numerically, otherwise lexically, numeric < alphanumeric.
func Compare(a, b Canonical) int {
	if c := cmpInt(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpInt(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := cmpInt(a.Patch, b.Patch); c != 0 {
		return c
	}
	// A version with a pre-release has LOWER precedence than one without.
	if len(a.PreRelease) == 0 && len(b.PreRelease) == 0 {
		return 0
	}
	if len(a.PreRelease) == 0 {
		return 1
	}
	if len(b.PreRelease) == 0 {
		return -1
	}
	return cmpIdents(a.PreRelease, b.PreRelease)
}

func cmpIdents(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		an, aerr := strconv.Atoi(a[i])
		bn, berr := strconv.Atoi(b[i])
		switch {
		case aerr == nil && berr == nil:
			if c := cmpInt(an, bn); c != 0 {
				return c
			}
		case aerr == nil:
			return -1 // numeric < alphanumeric
		case berr == nil:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(a), len(b))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/version/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/version/semver.go internal/version/semver_test.go
git commit -m "feat(version): Canonical semver type — String/Parse/Compare (NEX-717)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: Graph describe helper (`internal/change/describe.go`)

**Files:**
- Create: `internal/change/describe.go`
- Test: `internal/change/describe_test.go`

Read-only first-parent walks: nearest tag + distance, and line height. Uses `e.git` (go-git) + `ListTags()`.

- [ ] **Step 1: Write the failing test**

Study an existing engine test (e.g. `internal/change/tag_test.go` or `merge_test.go`) for the harness that opens an Engine and creates commits/lines, and reuse its helpers. The test builds: root line, three commits, a tag on the first, then asserts describe results.

```go
package change

import "testing"

func TestDescribeVersion(t *testing.T) {
	e := newTestEngine(t)            // reuse the existing test constructor
	root, _ := e.RootLine()
	c1 := commitOnLine(t, e, root.Name, "a.txt", "1") // reuse existing commit helper
	if err := e.Tag("v1.0.0", c1, "tester"); err != nil {
		t.Fatal(err)
	}
	commitOnLine(t, e, root.Name, "b.txt", "2")
	c3 := commitOnLine(t, e, root.Name, "c.txt", "3")

	tag, dist, err := e.DescribeVersion(c3)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v1.0.0" || dist != 2 {
		t.Fatalf("DescribeVersion(c3) = %q, %d; want v1.0.0, 2", tag, dist)
	}

	// On the tagged commit itself: distance 0.
	tag0, d0, err := e.DescribeVersion(c1)
	if err != nil {
		t.Fatal(err)
	}
	if tag0 != "v1.0.0" || d0 != 0 {
		t.Fatalf("DescribeVersion(c1) = %q, %d; want v1.0.0, 0", tag0, d0)
	}
}

func TestDescribeVersionNoTag(t *testing.T) {
	e := newTestEngine(t)
	root, _ := e.RootLine()
	commitOnLine(t, e, root.Name, "a.txt", "1")
	c2 := commitOnLine(t, e, root.Name, "b.txt", "2")
	tag, dist, err := e.DescribeVersion(c2)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "" || dist < 1 {
		t.Fatalf("no-tag DescribeVersion = %q, %d; want empty tag, dist>=1", tag, dist)
	}
}
```

> NOTE to implementer: the helper names above (`newTestEngine`, `commitOnLine`) are placeholders for whatever the existing `internal/change` tests already use. Open the test files first and call the real helpers; do not invent a parallel harness.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/change/ -run TestDescribeVersion -v`
Expected: FAIL (DescribeVersion undefined).

- [ ] **Step 3: Implement**

```go
package change

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const describeWalkCap = 100000 // backstop against a malformed graph

// DescribeVersion walks first-parent ancestry from commit to the nearest commit
// that carries a tag. It returns that tag's name and the commit distance
// (0 = commit itself is tagged). If no ancestor is tagged it returns
// ("", totalDepth, nil). First-parent is used so a merge commit's mainline is
// followed, matching how lines adopt their parent.
func (e *Engine) DescribeVersion(commit string) (string, int, error) {
	if commit == "" {
		return "", 0, nil
	}
	tags, err := e.ListTags()
	if err != nil {
		return "", 0, fmt.Errorf("change.DescribeVersion: %w", err)
	}
	byCommit := make(map[string]string, len(tags))
	for _, t := range tags {
		// First tag wins for a given commit; deterministic via ListTags order.
		if _, ok := byCommit[t.Commit]; !ok {
			byCommit[t.Commit] = t.Name
		}
	}
	cur := commit
	for dist := 0; dist < describeWalkCap; dist++ {
		if name, ok := byCommit[cur]; ok {
			return name, dist, nil
		}
		next, err := e.firstParent(cur)
		if err != nil {
			return "", 0, fmt.Errorf("change.DescribeVersion: %w", err)
		}
		if next == "" {
			return "", dist + 1, nil // reached a root with no tag in ancestry
		}
		cur = next
	}
	return "", 0, fmt.Errorf("change.DescribeVersion: ancestry exceeded %d commits", describeWalkCap)
}

// LineHeight returns the number of commits on line since its base (branch point):
// the first-parent distance from TipCommit back to BaseCommit. A line with no
// commits beyond its base has height 0.
func (e *Engine) LineHeight(line Line) (int, error) {
	if line.TipCommit == "" || line.TipCommit == line.BaseCommit {
		return 0, nil
	}
	cur := line.TipCommit
	for h := 0; h < describeWalkCap; h++ {
		if cur == line.BaseCommit {
			return h, nil
		}
		next, err := e.firstParent(cur)
		if err != nil {
			return 0, fmt.Errorf("change.LineHeight: %w", err)
		}
		if next == "" {
			return h + 1, nil // base unreachable (no base set) → full depth
		}
		cur = next
	}
	return 0, fmt.Errorf("change.LineHeight: ancestry exceeded %d commits", describeWalkCap)
}

// firstParent returns the first parent hash of commit, or "" if it has none.
func (e *Engine) firstParent(commit string) (string, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return "", fmt.Errorf("commit %s: %w", commit, err)
	}
	if c.NumParents() == 0 {
		return "", nil
	}
	var first *object.Commit
	first, err = c.Parent(0)
	if err != nil {
		return "", fmt.Errorf("parent of %s: %w", commit, err)
	}
	return first.Hash.String(), nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/change/ -run 'TestDescribeVersion|TestDescribeVersionNoTag' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/change/describe.go internal/change/describe_test.go
git commit -m "feat(change): DescribeVersion + LineHeight graph helpers (NEX-717)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: Config (`internal/version/config.go`)

**Files:**
- Create: `internal/version/config.go`
- Test: `internal/version/config_test.go`
- Modify: `go.mod` / `go.sum` (add `gopkg.in/yaml.v3`)

- [ ] **Step 1: Add the yaml dependency**

Run: `go get gopkg.in/yaml.v3@latest`
Expected: go.mod gains the require; `go mod tidy` clean.

- [ ] **Step 2: Write the failing test**

```go
package version

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(t.TempDir()) // no cairn.version file
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TagPrefix != "v" || cfg.DefaultIncrement != "patch" {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
}

func TestLoadConfigParsesAndValidates(t *testing.T) {
	dir := t.TempDir()
	yml := "tag-prefix: rel-\ndefault-increment: minor\necosystems:\n  npm: { manifest: package.json }\n"
	if err := os.WriteFile(filepath.Join(dir, "cairn.version"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TagPrefix != "rel-" || cfg.DefaultIncrement != "minor" {
		t.Fatalf("parse wrong: %+v", cfg)
	}
	if cfg.Ecosystems["npm"].Manifest != "package.json" {
		t.Fatalf("ecosystem manifest not parsed: %+v", cfg.Ecosystems)
	}
}

func TestLoadConfigRejectsBadIncrement(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cairn.version"), []byte("default-increment: huge\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(dir); err == nil {
		t.Error("expected validation error for bad increment")
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/version/ -run TestLoadConfig -v`
Expected: FAIL.

- [ ] **Step 4: Implement**

```go
package version

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config drives derivation + rendering. All fields are optional; LoadConfig
// supplies DefaultConfig values when the file or a field is absent.
type Config struct {
	TagPrefix        string            `yaml:"tag-prefix"`
	DefaultIncrement string            `yaml:"default-increment"` // major|minor|patch
	Lines            []LineRule        `yaml:"-"`                 // reserved (line overrides); parsed below
	Ecosystems       map[string]EcoCfg `yaml:"ecosystems"`
}

type LineRule struct {
	Name       string `yaml:"name"`
	PreRelease bool   `yaml:"prerelease"`
}

type EcoCfg struct {
	Manifest string `yaml:"manifest"`
}

func DefaultConfig() Config {
	return Config{TagPrefix: "v", DefaultIncrement: "patch", Ecosystems: map[string]EcoCfg{}}
}

// LoadConfig reads cairn.version from repoRoot. A missing file yields
// DefaultConfig() with no error. Present fields override defaults; the result is
// validated.
func LoadConfig(repoRoot string) (Config, error) {
	cfg := DefaultConfig()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "cairn.version"))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return Config{}, fmt.Errorf("version.LoadConfig: %w", err)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("version.LoadConfig: parse: %w", err)
	}
	if cfg.TagPrefix == "" {
		cfg.TagPrefix = "v"
	}
	if cfg.DefaultIncrement == "" {
		cfg.DefaultIncrement = "patch"
	}
	if cfg.Ecosystems == nil {
		cfg.Ecosystems = map[string]EcoCfg{}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	switch c.DefaultIncrement {
	case "major", "minor", "patch":
	default:
		return fmt.Errorf("version.Config: default-increment %q must be major|minor|patch", c.DefaultIncrement)
	}
	return nil
}
```

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/version/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/version/config.go internal/version/config_test.go go.mod go.sum
git commit -m "feat(version): cairn.version config load/validate + yaml.v3 dep (NEX-718)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 4: Derivation (`internal/version/derive.go`)

**Files:**
- Create: `internal/version/derive.go`
- Test: `internal/version/derive_test.go`

The pure function. Inputs are facts (already gathered from the graph); no engine import.

- [ ] **Step 1: Write the failing test**

```go
package version

import "testing"

func di(base string, dist int, line string, trunk bool, lineDist int, bump, sha string) DeriveInput {
	return DeriveInput{BaseTag: base, Distance: dist, LineName: line, IsTrunk: trunk,
		LineDistance: lineDist, PendingBump: bump, ShortSHA: sha, Config: DefaultConfig()}
}

func TestDeriveOnTagIsRelease(t *testing.T) {
	v, err := Derive(di("v1.4.0", 0, "main", true, 0, "", "abc1234"))
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != "1.4.0" {
		t.Fatalf("on-tag = %q, want 1.4.0", v.String())
	}
}

func TestDeriveTrunkOffTag(t *testing.T) {
	v, err := Derive(di("v1.4.0", 3, "main", true, 3, "", "abc1234"))
	if err != nil {
		t.Fatal(err)
	}
	// patch bump, release core, height+sha in build metadata.
	if v.Major != 1 || v.Minor != 4 || v.Patch != 1 || len(v.PreRelease) != 0 {
		t.Fatalf("trunk core wrong: %+v", v)
	}
	if v.String() != "1.4.1+3.gabc1234" {
		t.Fatalf("trunk off-tag = %q, want 1.4.1+3.gabc1234", v.String())
	}
}

func TestDeriveExperimentLine(t *testing.T) {
	v, err := Derive(di("v1.4.0", 6, "exp-idea", false, 5, "", "abc1234"))
	if err != nil {
		t.Fatal(err)
	}
	if v.String() != "1.4.1-exp-idea.5+gabc1234" {
		t.Fatalf("line = %q, want 1.4.1-exp-idea.5+gabc1234", v.String())
	}
}

func TestDerivePendingBumpOverridesDefault(t *testing.T) {
	v, err := Derive(di("v1.4.0", 2, "main", true, 2, "minor", "abc1234"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Major != 1 || v.Minor != 5 || v.Patch != 0 {
		t.Fatalf("minor bump wrong: %+v", v)
	}
}

func TestDeriveTwoLinesNeverCollide(t *testing.T) {
	a, _ := Derive(di("v1.0.0", 2, "exp-a", false, 1, "", "aaa"))
	b, _ := Derive(di("v1.0.0", 2, "exp-b", false, 1, "", "bbb"))
	if a.String() == b.String() {
		t.Fatalf("two lines collided: %q", a.String())
	}
}

func TestDeriveNoBaseTag(t *testing.T) {
	v, err := Derive(di("", 1, "main", true, 1, "", "abc1234"))
	if err != nil {
		t.Fatal(err)
	}
	if v.Major != 0 || v.Minor != 0 || v.Patch != 1 {
		t.Fatalf("no-tag base wrong: %+v", v)
	}
}

func TestDeriveLabelSanitized(t *testing.T) {
	v, _ := Derive(di("v1.0.0", 2, "Feature/Big_Idea", false, 1, "", "abc"))
	// '/' and '_' -> '-', lowercased
	if v.PreRelease[0] != "feature-big-idea" {
		t.Fatalf("label not sanitized: %+v", v.PreRelease)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/version/ -run TestDerive -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
package version

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DeriveInput is the full set of facts the pure derivation needs. The caller
// gathers BaseTag/Distance/LineDistance from the change-graph; Derive performs no
// I/O.
type DeriveInput struct {
	BaseTag      string // nearest reachable tag, e.g. "v1.4.0" ("" if none)
	Distance     int    // commits from target back to BaseTag's commit (0 = on the tag)
	LineName     string // line being versioned
	IsTrunk      bool   // target line is the structural root
	LineDistance int    // commits since the line's branch point
	PendingBump  string // "major"|"minor"|"patch"|"" (explicit; else config default)
	ShortSHA     string // target commit short sha (build metadata)
	Config       Config
}

// Derive computes the canonical version. Deterministic: same input → same output.
func Derive(in DeriveInput) (Canonical, error) {
	base := Canonical{} // 0.0.0 when no tag
	if in.BaseTag != "" {
		parsed, err := Parse(strings.TrimPrefix(in.BaseTag, in.Config.TagPrefix))
		if err != nil {
			return Canonical{}, fmt.Errorf("version.Derive: base tag: %w", err)
		}
		base = Canonical{Major: parsed.Major, Minor: parsed.Minor, Patch: parsed.Patch}
	}

	// On the tagged commit itself: the release version, verbatim core.
	if in.Distance == 0 && in.BaseTag != "" {
		return base, nil
	}

	bump := in.PendingBump
	if bump == "" {
		bump = in.Config.DefaultIncrement
	}
	core, err := applyBump(base, bump)
	if err != nil {
		return Canonical{}, err
	}

	if in.IsTrunk {
		// Release core; height + sha in build metadata (string-unique, traceable).
		core.Build = []string{strconv.Itoa(in.Distance), "g" + in.ShortSHA}
		return core, nil
	}
	// Experiment line: a true pre-release, collision-free across lines.
	core.PreRelease = []string{sanitizeLabel(in.LineName), strconv.Itoa(in.LineDistance)}
	core.Build = []string{"g" + in.ShortSHA}
	return core, nil
}

func applyBump(b Canonical, bump string) (Canonical, error) {
	switch bump {
	case "major":
		return Canonical{Major: b.Major + 1}, nil
	case "minor":
		return Canonical{Major: b.Major, Minor: b.Minor + 1}, nil
	case "patch":
		return Canonical{Major: b.Major, Minor: b.Minor, Patch: b.Patch + 1}, nil
	default:
		return Canonical{}, fmt.Errorf("version.Derive: invalid bump %q", bump)
	}
}

var labelStrip = regexp.MustCompile(`[^0-9a-z-]+`)

// sanitizeLabel turns a line name into a valid semver pre-release identifier.
func sanitizeLabel(name string) string {
	s := strings.ToLower(name)
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = labelStrip.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "line"
	}
	return s
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/version/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/version/derive.go internal/version/derive_test.go
git commit -m "feat(version): pure Derive — release/trunk/line, pending-bump, sanitized label (NEX-717)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 5: Per-ecosystem rendering (`internal/version/render.go`)

**Files:**
- Create: `internal/version/render.go`
- Test: `internal/version/render_test.go`

- [ ] **Step 1: Write the failing test**

```go
package version

import "testing"

func TestRender(t *testing.T) {
	rel := Canonical{Major: 1, Minor: 4, Patch: 1}
	pre := Canonical{Major: 1, Minor: 4, Patch: 1, PreRelease: []string{"exp-idea", "5"}, Build: []string{"gabc1234"}}
	rc := Canonical{Major: 1, Minor: 4, Patch: 1, PreRelease: []string{"rc", "1"}}

	cases := []struct {
		v    Canonical
		eco  string
		want string
	}{
		{rel, "npm", "1.4.1"},
		{pre, "npm", "1.4.1-exp-idea.5+gabc1234"},
		{rel, "nuget", "1.4.1"},
		{rel, "pypi", "1.4.1"},
		{pre, "pypi", "1.4.1.dev5+gabc1234"},
		{rc, "pypi", "1.4.1rc1"},
		{pre, "oci", "1.4.1-exp-idea.5_gabc1234"}, // '+' -> '_'
		{rel, "go", "v1.4.1"},
		{pre, "go", "v1.4.1-exp-idea.5"}, // build metadata dropped
	}
	for _, tc := range cases {
		got, err := Render(tc.v, tc.eco)
		if err != nil {
			t.Fatalf("Render(%v,%s): %v", tc.v, tc.eco, err)
		}
		if got != tc.want {
			t.Errorf("Render(%s) = %q, want %q", tc.eco, got, tc.want)
		}
	}
	if _, err := Render(rel, "bogus"); err == nil {
		t.Error("expected error for unknown ecosystem")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/version/ -run TestRender -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
package version

import (
	"fmt"
	"strings"
)

// Render maps the canonical version to a packaging ecosystem's version string.
func Render(v Canonical, eco string) (string, error) {
	switch eco {
	case "npm", "nuget", "":
		return v.String(), nil // semver2
	case "oci":
		return strings.ReplaceAll(v.String(), "+", "_"), nil // OCI tags forbid '+'
	case "go":
		core := Canonical{Major: v.Major, Minor: v.Minor, Patch: v.Patch, PreRelease: v.PreRelease}
		return "v" + core.String(), nil // build metadata dropped
	case "pypi":
		return renderPEP440(v), nil
	default:
		return "", fmt.Errorf("version.Render: unknown ecosystem %q", eco)
	}
}

// renderPEP440 maps to PEP 440. A pre-release whose first identifier is an
// rc/alpha/beta stage renders as that stage; any other pre-release renders as a
// dev release using its last numeric identifier; build metadata becomes a local
// segment.
func renderPEP440(v Canonical) string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.PreRelease) > 0 {
		stage := strings.ToLower(v.PreRelease[0])
		num := lastNumeric(v.PreRelease)
		switch stage {
		case "rc", "a", "alpha", "b", "beta":
			canon := map[string]string{"alpha": "a", "beta": "b"}
			if c, ok := canon[stage]; ok {
				stage = c
			}
			s += fmt.Sprintf("%s%d", stage, num)
		default:
			s += fmt.Sprintf(".dev%d", num)
		}
	}
	if len(v.Build) > 0 {
		local := strings.Join(v.Build, ".")
		local = strings.ToLower(strings.ReplaceAll(local, "_", "."))
		s += "+" + local
	}
	return s
}

func lastNumeric(parts []string) int {
	for i := len(parts) - 1; i >= 0; i-- {
		var n int
		if _, err := fmt.Sscanf(parts[i], "%d", &n); err == nil {
			return n
		}
	}
	return 0
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/version/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/version/render.go internal/version/render_test.go
git commit -m "feat(version): per-ecosystem rendering (semver2/PEP440/OCI/Go) (NEX-719)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 6: Worktree DeriveInput assembler + accessors (`internal/worktree`)

**Files:**
- Modify: `internal/worktree/worktree.go`
- Test: `internal/worktree/version_test.go` (new)

Bridge the engine facts into a `version.DeriveInput`. worktree imports the pure `internal/version` (no cycle).

- [ ] **Step 1: Write the failing test**

Mirror an existing worktree test that builds a Repo, expresses a branch, commits, and tags. Then:

```go
package worktree

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/version"
)

func TestDeriveInputTrunk(t *testing.T) {
	skipOnWindows(t)
	r := newTestRepo(t)            // reuse existing helper
	def, _ := r.DefaultBranch()
	writeAndCommit(t, r, def, "a.txt", "1") // reuse existing helper
	c1tip := tipOf(t, r, def)
	if err := r.Tag("v1.0.0", c1tip); err != nil {  // see Step 3 for Repo.Tag
		t.Fatal(err)
	}
	writeAndCommit(t, r, def, "b.txt", "2")

	in, err := r.DeriveInput(def, version.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if in.BaseTag != "v1.0.0" || in.Distance != 1 || !in.IsTrunk || in.LineName != def {
		t.Fatalf("DeriveInput = %+v", in)
	}
	if in.ShortSHA == "" {
		t.Error("ShortSHA empty")
	}
}
```

> NOTE: use the real helper names from existing worktree tests (`newTestRepo`, etc.). If a "tip of branch" or "tag via Repo" helper doesn't exist, add the minimal accessor in Step 3.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/worktree/ -run TestDeriveInput -v`
Expected: FAIL.

- [ ] **Step 3: Implement (add to worktree.go)**

```go
// (add import) "github.com/CarriedWorldUniverse/cairn/internal/version"

// Root returns the working-copy root directory (for config file resolution).
func (r *Repo) Root() string { return r.root }

// Tag names the tip of branch with tag name (pass-through to the engine).
func (r *Repo) Tag(name, branch string) error {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return fmt.Errorf("worktree.Tag: %w", err)
	}
	return r.eng.Tag(name, line.TipCommit, r.author)
}

// PendingBump returns the recorded explicit bump intent ("" if none).
func (r *Repo) PendingBump() (string, error) {
	v, _, err := r.eng.GetConfig("version.pending_bump")
	return v, err
}

// SetPendingBump records explicit bump intent for the next release.
func (r *Repo) SetPendingBump(level string) error {
	return r.eng.SetConfig("version.pending_bump", level)
}

// DeriveInput assembles the facts the pure version.Derive needs for branch.
func (r *Repo) DeriveInput(branch string, cfg version.Config) (version.DeriveInput, error) {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return version.DeriveInput{}, fmt.Errorf("worktree.DeriveInput: %w", err)
	}
	tag, dist, err := r.eng.DescribeVersion(line.TipCommit)
	if err != nil {
		return version.DeriveInput{}, fmt.Errorf("worktree.DeriveInput: %w", err)
	}
	height, err := r.eng.LineHeight(line)
	if err != nil {
		return version.DeriveInput{}, fmt.Errorf("worktree.DeriveInput: %w", err)
	}
	bump, err := r.PendingBump()
	if err != nil {
		return version.DeriveInput{}, fmt.Errorf("worktree.DeriveInput: %w", err)
	}
	short := line.TipCommit
	if len(short) > 7 {
		short = short[:7]
	}
	return version.DeriveInput{
		BaseTag:      tag,
		Distance:     dist,
		LineName:     branch,
		IsTrunk:      line.ParentLine == "",
		LineDistance: height,
		PendingBump:  bump,
		ShortSHA:     short,
		Config:       cfg,
	}, nil
}
```

If `LineByName` / `Tag` need exporting beyond what exists, confirm the engine method names against `internal/change/engine.go` and `tag.go` (they exist: `LineByName`, `Tag`).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/worktree/ -run TestDeriveInput -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/worktree/worktree.go internal/worktree/version_test.go
git commit -m "feat(worktree): DeriveInput assembler + Root/Tag/PendingBump accessors (NEX-720)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 7: `cairn version` / `cairn version bump` CLI (`cmd/cairn`)

**Files:**
- Modify: `cmd/cairn/main.go`
- Test: `cmd/cairn/version_e2e_test.go` (new)

- [ ] **Step 1: Write the failing e2e test**

Mirror existing e2e helpers (`mustRun`, `soleExpressedDir`, etc.). Use the testable `run([]string) error` entrypoint if present (the other e2e tests show the pattern).

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2E_Version(t *testing.T) {
	skipOnWindows(t)
	dir := filepath.Join(t.TempDir(), "repo")
	mustRun(t, "init", dir)
	def := soleExpressedDir(t, dir)
	// first commit + tag via CLI
	if err := os.WriteFile(filepath.Join(dir, def, "a.txt"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dir, def)
	mustRun(t, "tag", "--repo", dir, "v1.0.0") // confirm `cairn tag` spelling in main.go

	// on the tag → release version
	out := mustRunOut(t, "version", "--repo", dir) // mustRunOut captures stdout; add if absent
	if strings.TrimSpace(out) != "1.0.0" {
		t.Fatalf("version on tag = %q, want 1.0.0", out)
	}

	// advance, expect a patch bump with build metadata on trunk
	if err := os.WriteFile(filepath.Join(dir, def, "b.txt"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dir, def)
	out = mustRunOut(t, "version", "--repo", dir)
	if !strings.HasPrefix(strings.TrimSpace(out), "1.0.1+") {
		t.Fatalf("trunk off-tag version = %q, want 1.0.1+...", out)
	}

	// pypi target rendering
	out = mustRunOut(t, "version", "--repo", dir, "--target", "pypi")
	if !strings.HasPrefix(strings.TrimSpace(out), "1.0.1") {
		t.Fatalf("pypi version = %q", out)
	}

	// bump intent → minor next
	mustRun(t, "version", "bump", "minor", "--repo", dir)
	out = mustRunOut(t, "version", "--repo", dir)
	if !strings.HasPrefix(strings.TrimSpace(out), "1.1.0") {
		t.Fatalf("after minor bump = %q, want 1.1.0...", out)
	}
}
```

> If `cairn tag` requires a commit arg, check `cmdTag` in main.go and pass what it expects (it tags a commit/line tip). If no `mustRunOut` helper exists, add one that runs `run` with stdout captured.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/cairn/ -run TestE2E_Version -v`
Expected: FAIL (unknown subcommand "version").

- [ ] **Step 3: Implement**

Add to the dispatch switch (near `case "config":`):

```go
	case "version":
		return cmdVersion(rest)
```

Add the command (model it on `cmdConfig`):

```go
func cmdVersion(args []string) error {
	// Subcommand: cairn version bump <level>
	if len(args) > 0 && args[0] == "bump" {
		return cmdVersionBump(args[1:])
	}
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	target := fs.String("target", "", "render for ecosystem: npm|nuget|pypi|oci|go")
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, err := r.DefaultBranch()
	if err != nil {
		return mapErr(err)
	}
	cfg, err := version.LoadConfig(r.Root())
	if err != nil {
		return mapErr(err)
	}
	in, err := r.DeriveInput(branch, cfg)
	if err != nil {
		return mapErr(err)
	}
	v, err := version.Derive(in)
	if err != nil {
		return mapErr(err)
	}
	out, err := version.Render(v, *target)
	if err != nil {
		return mapErr(err)
	}
	fmt.Println(out) // stdout = version only (CI consumes)
	return nil
}

func cmdVersionBump(args []string) error {
	fs := flag.NewFlagSet("version bump", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cairn version bump major|minor|patch")
	}
	level := fs.Arg(0)
	switch level {
	case "major", "minor", "patch":
	default:
		return fmt.Errorf("bump level %q must be major|minor|patch", level)
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if err := r.SetPendingBump(level); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: next release bump set to %s\n", level)
	return nil
}
```

Add `"github.com/CarriedWorldUniverse/cairn/internal/version"` to imports, and add `version` to the usage string. If a `--repo`-before-subcommand parsing issue arises with `bump`, keep `bump` detection as `args[0]=="bump"` before flag parsing (shown above).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./cmd/cairn/ -run TestE2E_Version -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/cairn/main.go cmd/cairn/version_e2e_test.go
git commit -m "feat(cmd): cairn version + cairn version bump (CI-consumable derived version) (NEX-720)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 8: Manifest stampers (`internal/release/manifest.go`)

**Files:**
- Create: `internal/release/manifest.go`
- Test: `internal/release/manifest_test.go`

Pure bytes→bytes version stamping per ecosystem. (Done first; the orchestration in Task 10 composes it.)

- [ ] **Step 1: Write the failing test**

```go
package release

import (
	"strings"
	"testing"
)

func TestStampManifest(t *testing.T) {
	npm := `{
  "name": "x",
  "version": "0.0.0"
}`
	out, err := StampManifest("npm", []byte(npm), "1.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"version": "1.4.1"`) {
		t.Fatalf("npm not stamped: %s", out)
	}

	csproj := "<Project>\n  <PropertyGroup>\n    <Version>0.0.0</Version>\n  </PropertyGroup>\n</Project>"
	out, err = StampManifest("nuget", []byte(csproj), "1.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<Version>1.4.1</Version>") {
		t.Fatalf("csproj not stamped: %s", out)
	}

	pyproject := "[project]\nname = \"x\"\nversion = \"0.0.0\"\n"
	out, err = StampManifest("pypi", []byte(pyproject), "1.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `version = "1.4.1"`) {
		t.Fatalf("pyproject not stamped: %s", out)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/release/ -run TestStampManifest -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// Package release performs atomic cairn releases: stamp manifests, tag, and
// publish (publish last, the only irreversible step). External effects sit behind
// the Publisher and RegistryProbe interfaces so the package is testable offline.
package release

import (
	"fmt"
	"regexp"
)

var (
	reNpmVersion   = regexp.MustCompile(`("version"\s*:\s*)"[^"]*"`)
	reCsprojVersion = regexp.MustCompile(`(<Version>)[^<]*(</Version>)`)
	rePyVersion    = regexp.MustCompile(`(?m)^(version\s*=\s*)"[^"]*"`)
)

// StampManifest replaces the version field in a manifest's bytes for the given
// ecosystem, returning the new bytes. It does not write to disk.
func StampManifest(eco string, src []byte, version string) ([]byte, error) {
	switch eco {
	case "npm":
		return replaceOne(reNpmVersion, src, `${1}"`+version+`"`, "npm package.json version")
	case "nuget":
		return replaceOne(reCsprojVersion, src, `${1}`+version+`${2}`, "csproj <Version>")
	case "pypi":
		return replaceOne(rePyVersion, src, `${1}"`+version+`"`, "pyproject version")
	case "oci", "go":
		return src, nil // no manifest to stamp (tag-only ecosystems)
	default:
		return nil, fmt.Errorf("release.StampManifest: unknown ecosystem %q", eco)
	}
}

func replaceOne(re *regexp.Regexp, src []byte, repl, what string) ([]byte, error) {
	if !re.Match(src) {
		return nil, fmt.Errorf("release.StampManifest: %s not found", what)
	}
	return re.ReplaceAll(src, []byte(repl)), nil
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/release/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/release/manifest.go internal/release/manifest_test.go
git commit -m "feat(release): per-ecosystem manifest version stampers (NEX-721)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 9: Publisher + RegistryProbe seams (`internal/release/publish.go`, `guardrails.go`)

**Files:**
- Create: `internal/release/publish.go`
- Create: `internal/release/guardrails.go`
- Test: `internal/release/publish_test.go`

Interfaces + real exec impls (argv construction tested, not executed) + the pure guardrail checks.

- [ ] **Step 1: Write the failing test**

```go
package release

import "testing"

func TestExecPublisherArgv(t *testing.T) {
	for _, tc := range []struct {
		eco  string
		want []string
	}{
		{"npm", []string{"npm", "publish"}},
		{"pypi", []string{"python", "-m", "twine", "upload", "dist/*"}},
		{"nuget", []string{"dotnet", "nuget", "push"}},
	} {
		argv, err := publishArgv(tc.eco, "/repo", "1.4.1")
		if err != nil {
			t.Fatal(err)
		}
		if argv[0] != tc.want[0] || argv[1] != tc.want[1] {
			t.Errorf("%s argv = %v, want prefix %v", tc.eco, argv, tc.want)
		}
	}
	if _, err := publishArgv("bogus", "/repo", "1.4.1"); err == nil {
		t.Error("expected error for unknown ecosystem")
	}
}

func TestGuardMonotonic(t *testing.T) {
	mk := func(s string) string { return s }
	// new must be strictly greater than latest.
	if err := guardMonotonic(mk("1.4.1"), mk("1.4.0")); err != nil {
		t.Errorf("1.4.1 > 1.4.0 should pass: %v", err)
	}
	if err := guardMonotonic(mk("1.4.0"), mk("1.4.0")); err == nil {
		t.Error("equal version should fail monotonicity")
	}
	if err := guardMonotonic(mk("1.3.0"), mk("1.4.0")); err == nil {
		t.Error("lower version should fail monotonicity")
	}
	if err := guardMonotonic(mk("1.0.0"), ""); err != nil {
		t.Errorf("no prior tag should pass: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/release/ -run 'TestExecPublisherArgv|TestGuardMonotonic' -v`
Expected: FAIL.

- [ ] **Step 3: Implement publish.go**

```go
package release

import (
	"fmt"
	"os/exec"
)

// Publisher publishes the artifact in dir to the eco registry. ExecPublisher is
// the real implementation; tests inject a fake.
type Publisher interface {
	Publish(eco, dir, version string) error
}

type ExecPublisher struct{}

func (ExecPublisher) Publish(eco, dir, version string) error {
	argv, err := publishArgv(eco, dir, version)
	if err != nil {
		return err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("release.Publish %s: %w: %s", eco, err, out)
	}
	return nil
}

// publishArgv builds the native publish command for an ecosystem.
func publishArgv(eco, dir, version string) ([]string, error) {
	switch eco {
	case "npm":
		return []string{"npm", "publish"}, nil
	case "pypi":
		return []string{"python", "-m", "twine", "upload", "dist/*"}, nil
	case "nuget":
		return []string{"dotnet", "nuget", "push", fmt.Sprintf("bin/Release/*.%s.nupkg", version)}, nil
	case "oci":
		return []string{"docker", "push", version}, nil // version = full image ref for oci
	default:
		return nil, fmt.Errorf("release.publishArgv: unknown ecosystem %q", eco)
	}
}
```

- [ ] **Step 4: Implement guardrails.go**

```go
package release

import (
	"fmt"

	"github.com/CarriedWorldUniverse/cairn/internal/version"
)

// RegistryProbe reports whether name@version already exists on the eco registry.
// ExecProbe is the real implementation; tests inject a fake.
type RegistryProbe interface {
	Exists(eco, name, version string) (bool, error)
}

// guardMonotonic fails unless newV is strictly greater (semver precedence) than
// latestTag. An empty latestTag (no prior release) always passes.
func guardMonotonic(newV, latestTag string) error {
	if latestTag == "" {
		return nil
	}
	a, err := version.Parse(newV)
	if err != nil {
		return fmt.Errorf("release.guardMonotonic: new %q: %w", newV, err)
	}
	b, err := version.Parse(latestTag)
	if err != nil {
		return fmt.Errorf("release.guardMonotonic: latest %q: %w", latestTag, err)
	}
	if version.Compare(a, b) <= 0 {
		return fmt.Errorf("release: version %s is not greater than latest %s", newV, latestTag)
	}
	return nil
}
```

(`ExecProbe` real impl: a thin type whose `Exists` shells out to `npm view`/`pip index`/etc.; for Slice 1 ship a minimal `ExecProbe` that returns `(false, nil)` with a TODO comment noting per-registry probes are wired per ecosystem in a follow-up — the **tag** existence guard in Task 10 is the always-on protection. Keep the interface so the orchestrator depends on the seam, not the impl.)

- [ ] **Step 5: Run to verify pass**

Run: `go test ./internal/release/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/release/publish.go internal/release/guardrails.go internal/release/publish_test.go
git commit -m "feat(release): Publisher/RegistryProbe seams + monotonicity guard (NEX-721)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 10: Atomic Release orchestration (`internal/release/release.go`)

**Files:**
- Create: `internal/release/release.go`
- Test: `internal/release/release_test.go`

Composes derive→render→guardrails→stamp→tag→publish-last→rollback against an injected `RepoPort` (so the engine/worktree stays decoupled and the orchestration is unit-testable with a fake).

- [ ] **Step 1: Write the failing test**

```go
package release

import (
	"errors"
	"testing"
)

// fakeRepo implements RepoPort in-memory.
type fakeRepo struct {
	dirty       bool
	latestTag   string
	manifest    []byte
	manifestEco string
	tagged      string
	bumpCleared bool
}

func (f *fakeRepo) Dirty() (bool, error)                 { return f.dirty, nil }
func (f *fakeRepo) LatestTag() (string, error)           { return f.latestTag, nil }
func (f *fakeRepo) ReadManifest(eco string) ([]byte, string, error) {
	return f.manifest, "manifest." + eco, nil
}
func (f *fakeRepo) WriteManifest(path string, b []byte) error { f.manifest = b; return nil }
func (f *fakeRepo) CreateTag(name string) error          { f.tagged = name; return nil }
func (f *fakeRepo) DeleteTag(name string) error          { f.tagged = ""; return nil }
func (f *fakeRepo) ClearPendingBump() error              { f.bumpCleared = true; return nil }

type fakePub struct{ called bool; fail bool }

func (p *fakePub) Publish(eco, dir, version string) error {
	p.called = true
	if p.fail {
		return errors.New("publish boom")
	}
	return nil
}

type okProbe struct{}

func (okProbe) Exists(eco, name, version string) (bool, error) { return false, nil }

func TestReleaseSuccess(t *testing.T) {
	fr := &fakeRepo{manifest: []byte(`{"version":"0.0.0"}`)}
	pub := &fakePub{}
	err := Release(Options{
		Eco: "npm", Version: "1.4.1", TagName: "v1.4.1", Dir: "/repo",
	}, fr, pub, okProbe{})
	if err != nil {
		t.Fatal(err)
	}
	if !pub.called || fr.tagged != "v1.4.1" || !fr.bumpCleared {
		t.Fatalf("release incomplete: pub=%v tag=%q cleared=%v", pub.called, fr.tagged, fr.bumpCleared)
	}
	if string(fr.manifest) != `{"version":"1.4.1"}` {
		t.Fatalf("manifest not stamped: %s", fr.manifest)
	}
}

func TestReleaseRollbackOnPublishFailure(t *testing.T) {
	fr := &fakeRepo{manifest: []byte(`{"version":"0.0.0"}`)}
	pub := &fakePub{fail: true}
	err := Release(Options{Eco: "npm", Version: "1.4.1", TagName: "v1.4.1", Dir: "/repo"}, fr, pub, okProbe{})
	if err == nil {
		t.Fatal("expected publish failure")
	}
	if fr.tagged != "" {
		t.Errorf("tag not rolled back: %q", fr.tagged)
	}
	if string(fr.manifest) != `{"version":"0.0.0"}` {
		t.Errorf("manifest not rolled back: %s", fr.manifest)
	}
}

func TestReleaseDirtyTreeRefused(t *testing.T) {
	fr := &fakeRepo{dirty: true, manifest: []byte(`{"version":"0.0.0"}`)}
	pub := &fakePub{}
	if err := Release(Options{Eco: "npm", Version: "1.4.1", TagName: "v1.4.1", Dir: "/repo"}, fr, pub, okProbe{}); err == nil {
		t.Fatal("dirty tree must be refused")
	}
	if pub.called {
		t.Error("must not publish a dirty tree")
	}
}

func TestReleaseExistingTagRefused(t *testing.T) {
	fr := &fakeRepo{latestTag: "v1.4.1", manifest: []byte(`{"version":"0.0.0"}`)}
	pub := &fakePub{}
	// new version equals latest tag → monotonicity guard fires.
	if err := Release(Options{Eco: "npm", Version: "1.4.1", TagName: "v1.4.1", Dir: "/repo"}, fr, pub, okProbe{}); err == nil {
		t.Fatal("non-monotonic version must be refused")
	}
}

func TestReleaseDryRun(t *testing.T) {
	fr := &fakeRepo{manifest: []byte(`{"version":"0.0.0"}`)}
	pub := &fakePub{}
	plan, err := Plan(Options{Eco: "npm", Version: "1.4.1", TagName: "v1.4.1", Dir: "/repo"}, fr, okProbe{})
	if err != nil {
		t.Fatal(err)
	}
	if plan == "" {
		t.Error("plan should be non-empty")
	}
	if pub.called || fr.tagged != "" || string(fr.manifest) != `{"version":"0.0.0"}` {
		t.Error("dry-run must not mutate or publish")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/release/ -run TestRelease -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
package release

import (
	"fmt"
	"strings"
)

// RepoPort is the repo capability surface Release needs. worktree.Repo provides a
// concrete adapter (Task 11); tests use a fake.
type RepoPort interface {
	Dirty() (bool, error)
	LatestTag() (string, error)
	ReadManifest(eco string) (content []byte, path string, err error)
	WriteManifest(path string, content []byte) error
	CreateTag(name string) error
	DeleteTag(name string) error
	ClearPendingBump() error
}

type Options struct {
	Eco     string // npm|nuget|pypi|oci
	Version string // rendered version for Eco
	TagName string // e.g. "v1.4.1"
	Name    string // package name (registry-exists probe); optional
	Dir     string // publish working dir
}

// runGuards runs all three guardrails (dirty / monotonic / already-exists).
func runGuards(o Options, repo RepoPort, probe RegistryProbe) (latestTag string, err error) {
	dirty, err := repo.Dirty()
	if err != nil {
		return "", err
	}
	if dirty {
		return "", fmt.Errorf("release: working tree has uncommitted changes")
	}
	latestTag, err = repo.LatestTag()
	if err != nil {
		return "", err
	}
	if err := guardMonotonic(o.Version, latestTag); err != nil {
		return "", err
	}
	exists, err := probe.Exists(o.Eco, o.Name, o.Version)
	if err != nil {
		return "", err
	}
	if exists {
		return "", fmt.Errorf("release: %s %s already exists on the %s registry", o.Name, o.Version, o.Eco)
	}
	return latestTag, nil
}

// Plan validates and returns a human-readable dry-run plan without mutating.
func Plan(o Options, repo RepoPort, probe RegistryProbe) (string, error) {
	if _, err := runGuards(o, repo, probe); err != nil {
		return "", err
	}
	content, path, err := repo.ReadManifest(o.Eco)
	if err != nil {
		return "", err
	}
	stamped := "(tag-only ecosystem, no manifest)"
	if len(content) > 0 {
		stamped = path
	}
	argv, _ := publishArgv(o.Eco, o.Dir, o.Version)
	return fmt.Sprintf("release plan:\n  version: %s\n  tag:     %s\n  manifest: %s\n  publish: %s",
		o.Version, o.TagName, stamped, strings.Join(argv, " ")), nil
}

// Release performs the atomic release. Publish is last (the only irreversible
// step); any failure before it rolls back manifest + tag.
func Release(o Options, repo RepoPort, pub Publisher, probe RegistryProbe) error {
	if _, err := runGuards(o, repo, probe); err != nil {
		return err
	}

	// 1. Stamp the manifest (reversible — keep original bytes).
	content, path, err := repo.ReadManifest(o.Eco)
	if err != nil {
		return err
	}
	original := append([]byte(nil), content...)
	manifestWritten := false
	if len(content) > 0 {
		stamped, err := StampManifest(o.Eco, content, o.Version)
		if err != nil {
			return err
		}
		if err := repo.WriteManifest(path, stamped); err != nil {
			return err
		}
		manifestWritten = true
	}
	rollback := func() {
		if manifestWritten {
			_ = repo.WriteManifest(path, original)
		}
	}

	// 2. Tag (reversible).
	if err := repo.CreateTag(o.TagName); err != nil {
		rollback()
		return err
	}

	// 3. Publish — last, irreversible.
	if err := pub.Publish(o.Eco, o.Dir, o.Version); err != nil {
		_ = repo.DeleteTag(o.TagName)
		rollback()
		return fmt.Errorf("release: publish failed, rolled back tag+manifest: %w", err)
	}

	return repo.ClearPendingBump()
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/release/ -v`
Expected: PASS (all release tests).

- [ ] **Step 5: Commit**

```bash
git add internal/release/release.go internal/release/release_test.go
git commit -m "feat(release): atomic Release + Plan (guardrails, publish-last, rollback) (NEX-721)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 11: `cairn release` CLI + RepoPort adapter

**Files:**
- Modify: `internal/worktree/worktree.go` (add `ReleasePort` adapter methods)
- Modify: `cmd/cairn/main.go` (`cmdRelease`)
- Test: `cmd/cairn/release_e2e_test.go` (new)

- [ ] **Step 1: Write the failing e2e test**

The CLI must allow injecting a fake publisher/probe for the test. Add a package-level seam in `cmd/cairn` (e.g. `var newPublisher = func() release.Publisher { return release.ExecPublisher{} }`) that the test overrides; same for the probe. Then:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/release"
)

func TestE2E_ReleaseDryRun(t *testing.T) {
	skipOnWindows(t)
	dir := filepath.Join(t.TempDir(), "repo")
	mustRun(t, "init", dir)
	def := soleExpressedDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, def, "package.json"), []byte(`{"version":"0.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dir, def)
	out := mustRunOut(t, "release", "--repo", dir, "--target", "npm", "--dry-run")
	if !strings.Contains(out, "release plan") || !strings.Contains(out, "publish: npm publish") {
		t.Fatalf("dry-run plan missing: %s", out)
	}
}

func TestE2E_ReleaseSuccessWithFakePublisher(t *testing.T) {
	skipOnWindows(t)
	fp := &capturePub{}
	newPublisher = func() release.Publisher { return fp }
	newProbe = func() release.RegistryProbe { return okProbe{} }
	t.Cleanup(func() { newPublisher = defaultPublisher; newProbe = defaultProbe })

	dir := filepath.Join(t.TempDir(), "repo")
	mustRun(t, "init", dir)
	def := soleExpressedDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, def, "package.json"), []byte(`{"version":"0.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "commit", "--repo", dir, def)
	mustRun(t, "release", "--repo", dir, "--target", "npm")
	if !fp.called {
		t.Fatal("publisher not called")
	}
	// tag now exists → a second release at the same version must be refused.
	if err := run([]string{"release", "--repo", dir, "--target", "npm"}); err == nil {
		t.Fatal("second release at same version should be refused")
	}
}

type capturePub struct{ called bool }
func (p *capturePub) Publish(eco, dir, version string) error { p.called = true; return nil }
type okProbe struct{}
func (okProbe) Exists(eco, name, version string) (bool, error) { return false, nil }
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/cairn/ -run TestE2E_Release -v`
Expected: FAIL.

- [ ] **Step 3: Implement the worktree adapter**

Add to `worktree.go` a `ReleasePort` returning an adapter that implements `release.RepoPort` for a branch. It needs the manifest path inside the expressed folder, dirty detection (reuse `Status().Conflicts`/a scan-vs-tip diff — if a clean/dirty helper exists use it; otherwise compare `Scan` to the committed tree), latest tag on the line (via `DescribeVersion` distance 0 check or `ListTags` filtered to line ancestry — for Slice 1 use the nearest reachable tag from `DescribeVersion`), tag create/delete (engine `Tag`; for delete add `Engine.DeleteTag(name)` — a one-line catalogue delete + the export prune already exists), and `SetPendingBump("")`.

```go
// ReleasePort adapts a branch's working copy to release.RepoPort.
func (r *Repo) ReleasePort(branch, eco string) (release.RepoPort, error) {
	line, err := r.eng.LineByName(branch)
	if err != nil {
		return nil, fmt.Errorf("worktree.ReleasePort: %w", err)
	}
	return &releaseAdapter{r: r, branch: branch, line: line, eco: eco}, nil
}

type releaseAdapter struct {
	r      *Repo
	branch string
	line   change.Line
	eco    string
}

func (a *releaseAdapter) Dirty() (bool, error) {
	// Reuse the scan-vs-committed comparison Commit uses; expose a helper if needed.
	return a.r.isDirty(a.branch)
}
func (a *releaseAdapter) LatestTag() (string, error) {
	tag, _, err := a.r.eng.DescribeVersion(a.line.TipCommit)
	return tag, err
}
func (a *releaseAdapter) ReadManifest(eco string) ([]byte, string, error) {
	name := manifestName(eco) // package.json / *.csproj / pyproject.toml; "" for oci/go
	if name == "" {
		return nil, "", nil
	}
	p := filepath.Join(a.r.root, a.branch, name)
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, p, nil // tolerated: tag-only / no manifest
		}
		return nil, "", err
	}
	return b, p, nil
}
func (a *releaseAdapter) WriteManifest(path string, b []byte) error { return os.WriteFile(path, b, 0o644) }
func (a *releaseAdapter) CreateTag(name string) error               { return a.r.Tag(name, a.branch) }
func (a *releaseAdapter) DeleteTag(name string) error               { return a.r.eng.DeleteTag(name) }
func (a *releaseAdapter) ClearPendingBump() error                   { return a.r.SetPendingBump("") }

func manifestName(eco string) string {
	switch eco {
	case "npm":
		return "package.json"
	case "pypi":
		return "pyproject.toml"
	case "nuget":
		return "" // *.csproj glob; resolve first match in Slice 1 follow-up
	default:
		return ""
	}
}
```

Add `Engine.DeleteTag(name string) error` to `internal/change/tag.go` (delete the row + leave export prune to handle refs) and a `Repo.isDirty(branch)` helper (extract from `Commit`'s scan, or compare `Scan` to the tip tree). Keep these minimal and tested via the e2e.

- [ ] **Step 4: Implement `cmdRelease`**

```go
// package-level seams (overridable in tests)
var defaultPublisher = func() release.Publisher { return release.ExecPublisher{} }
var defaultProbe = func() release.RegistryProbe { return release.ExecProbe{} }
var newPublisher = defaultPublisher
var newProbe = defaultProbe

func cmdRelease(args []string) error {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	target := fs.String("target", "", "ecosystem: npm|nuget|pypi|oci")
	dryRun := fs.Bool("dry-run", false, "show the plan without tagging or publishing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *target == "" {
		return errors.New("usage: cairn release --target npm|nuget|pypi|oci [--dry-run]")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	branch, err := r.DefaultBranch()
	if err != nil {
		return mapErr(err)
	}
	cfg, err := version.LoadConfig(r.Root())
	if err != nil {
		return mapErr(err)
	}
	in, err := r.DeriveInput(branch, cfg)
	if err != nil {
		return mapErr(err)
	}
	v, err := version.Derive(in)
	if err != nil {
		return mapErr(err)
	}
	rendered, err := version.Render(v, *target)
	if err != nil {
		return mapErr(err)
	}
	port, err := r.ReleasePort(branch, *target)
	if err != nil {
		return mapErr(err)
	}
	opts := release.Options{
		Eco: *target, Version: rendered, TagName: cfg.TagPrefix + v.String(),
		Dir: filepath.Join(*repo, branch),
	}
	if *dryRun {
		plan, err := release.Plan(opts, port, newProbe())
		if err != nil {
			return mapErr(err)
		}
		fmt.Println(plan)
		return nil
	}
	if err := release.Release(opts, port, newPublisher(), newProbe()); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: released %s (%s) tagged %s\n", rendered, *target, opts.TagName)
	return nil
}
```

Add `case "release": return cmdRelease(rest)` to the dispatch switch, add `internal/release` + `path/filepath` imports if missing, and add `release` to usage. Add a minimal `release.ExecProbe` type (`Exists` returns `(false,nil)` for Slice 1) so `defaultProbe` compiles.

- [ ] **Step 5: Run to verify pass**

Run: `go test ./cmd/cairn/ -run TestE2E_Release -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/worktree/worktree.go internal/change/tag.go cmd/cairn/main.go cmd/cairn/release_e2e_test.go
git commit -m "feat(cmd,worktree): cairn release — atomic tag+stamp+publish with guardrails (NEX-721)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 12: Final gate + docs

**Files:**
- Modify: `cmd/cairn/main.go` usage string (ensure `version`, `release` documented)
- Possibly: `README` / CLI reference if one exists

- [ ] **Step 1: Full suite + vet + cross-compile**

Run:
```bash
go test ./... && go vet ./... && go build ./... && GOOS=darwin go build ./... && GOOS=windows go build ./...
```
Expected: all green; all prior phases unaffected.

- [ ] **Step 2: Manual smoke (optional, documents the flow)**

```bash
cd $(mktemp -d) && <path>/cairn init . && echo '{"version":"0.0.0"}' > main/package.json && <path>/cairn commit --repo . main && <path>/cairn tag --repo . v1.0.0 && <path>/cairn version --repo . && <path>/cairn release --repo . --target npm --dry-run
```
Expected: `1.0.0` then a release plan.

- [ ] **Step 3: Commit any usage/docs tweaks**

```bash
git add -A
git commit -m "docs(cmd): document version + release in CLI usage (NEX-720,721)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Notes for the implementer

- **Reuse existing test harnesses** in `internal/change`, `internal/worktree`, and `cmd/cairn`. The helper names in this plan (`newTestEngine`, `commitOnLine`, `newTestRepo`, `writeAndCommit`, `mustRun`, `mustRunOut`, `soleExpressedDir`, `run`, `skipOnWindows`) are stand-ins — open the test files and call the real ones; add `mustRunOut`/a stdout-capturing helper only if none exists.
- **No silent caps:** if `ExecProbe` can't actually probe a registry in Slice 1, the **tag-existence + monotonicity** guards are the real protection and must be exercised by tests; the registry probe is a seam to fill per-ecosystem later.
- **Publish is last and irreversible** — never reorder it before the tag/stamp; the rollback contract depends on it.
- DRY, YAGNI, TDD, frequent commits. Each task ends green before the next begins.
