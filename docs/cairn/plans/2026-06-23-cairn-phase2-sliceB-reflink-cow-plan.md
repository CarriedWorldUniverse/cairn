# cairn Phase 2 Slice B — reflink CoW Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** swap `internal/worktree`'s plain materialization for reflink CoW from a content-addressed blob cache — unchanged content shares disk blocks, edits CoW-diverge, transparent to the `Repo`/CLI surface.

**Architecture:** a `reflinkOrCopy` helper (build-tagged: Linux FICLONE via `golang.org/x/sys/unix`, plain-copy fallback elsewhere) + a `.cairn/cache/blobs/<sha256>` content cache. `Materialize` writes each unique content to the cache once, then reflinks it into the branch folder. No daemon, no mount, no new module dep (x/sys already present).

**Tech Stack:** Go 1.26.3 · `golang.org/x/sys/unix` (already in module, v0.42.0) · `crypto/sha256` · stdlib `os`/`io`/`path/filepath`. Builds on Slice A.

**Spec:** `docs/cairn/specs/2026-06-23-cairn-phase2-sliceB-reflink-cow-design.md`.

## File Structure

| File | Responsibility |
|---|---|
| `internal/worktree/reflink_linux.go` | `reflinkOrCopy` (FICLONE + copy fallback), `reflinkSupported` — `//go:build linux` |
| `internal/worktree/reflink_other.go` | `reflinkOrCopy` (plain copy), `reflinkSupported`=false — `//go:build !linux` |
| `internal/worktree/reflink_test.go` | round-trip + fallback tests |
| `internal/worktree/fs.go` (modify) | `Materialize` gains a `cacheDir` param; writes blob cache + reflinks |
| `internal/worktree/worktree.go` (modify) | pass `cacheDir` at the 3–4 `Materialize` call sites |
| `internal/worktree/cache_test.go` | dedup + CoW-isolation + reflink-gated block-sharing |

---

## Task 1: reflinkOrCopy + reflinkSupported (NEX-728)

**Files:** Create `internal/worktree/reflink_linux.go`, `internal/worktree/reflink_other.go`, `internal/worktree/reflink_test.go`

- [ ] **Step 1: Failing test** (`reflink_test.go`) — platform-agnostic (works whether clone or copy)

```go
package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReflinkOrCopyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	want := []byte("hello reflink\n")
	if err := os.WriteFile(src, want, 0o644); err != nil { t.Fatal(err) }
	if err := reflinkOrCopy(src, dst); err != nil { t.Fatalf("reflinkOrCopy: %v", err) }
	got, err := os.ReadFile(dst)
	if err != nil { t.Fatalf("read dst: %v", err) }
	if string(got) != string(want) { t.Fatalf("dst = %q, want %q", got, want) }
}

func TestReflinkOrCopyIndependentAfterWrite(t *testing.T) {
	// After reflink/copy, overwriting dst must NOT change src (CoW or plain-copy both satisfy this).
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("original\n"), 0o644); err != nil { t.Fatal(err) }
	if err := reflinkOrCopy(src, dst); err != nil { t.Fatalf("reflinkOrCopy: %v", err) }
	if err := os.WriteFile(dst, []byte("changed\n"), 0o644); err != nil { t.Fatal(err) }
	got, _ := os.ReadFile(src)
	if string(got) != "original\n" { t.Fatalf("src mutated to %q", got) }
}
```

- [ ] **Step 2: Run, verify fail.** `go test ./internal/worktree/ -run TestReflink -v` → FAIL (undefined).

- [ ] **Step 3: Implement `reflink_linux.go`**

```go
//go:build linux

package worktree

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// reflinkOrCopy clones src→dst sharing blocks (CoW) via FICLONE when the
// filesystem supports it (Btrfs/XFS); otherwise it falls back to a plain copy.
func reflinkOrCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}
	defer out.Close()

	err = unix.IoctlFileClone(int(out.Fd()), int(in.Fd()))
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EOPNOTSUPP) &&
		!errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.EINVAL) {
		return fmt.Errorf("worktree.reflinkOrCopy: ficlone: %w", err)
	}
	// Unsupported FS / cross-device: plain copy.
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}
	if err := out.Truncate(0); err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: copy: %w", err)
	}
	return nil
}

// reflinkSupported reports whether dir's filesystem supports reflinks (for
// gating block-sharing assertions in tests).
func reflinkSupported(dir string) bool {
	a, err := os.CreateTemp(dir, "rfsrc")
	if err != nil {
		return false
	}
	defer os.Remove(a.Name())
	defer a.Close()
	_, _ = a.WriteString("x")
	b, err := os.OpenFile(a.Name()+".clone", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return false
	}
	defer os.Remove(b.Name())
	defer b.Close()
	return unix.IoctlFileClone(int(b.Fd()), int(a.Fd())) == nil
}
```

- [ ] **Step 4: Implement `reflink_other.go`**

```go
//go:build !linux

package worktree

import (
	"fmt"
	"io"
	"os"
)

func reflinkOrCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: %w", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("worktree.reflinkOrCopy: copy: %w", err)
	}
	return nil
}

func reflinkSupported(string) bool { return false }
```

- [ ] **Step 5: Run, verify pass.** `go test ./internal/worktree/ -run TestReflink -v` → PASS. `go vet ./internal/worktree/`. Also confirm `GOOS=windows go build ./internal/worktree/` and `GOOS=darwin go build ./internal/worktree/` compile (the `_other.go` path).

- [ ] **Step 6: Commit**

```bash
git add internal/worktree/reflink_linux.go internal/worktree/reflink_other.go internal/worktree/reflink_test.go
git commit -m "feat(worktree): reflinkOrCopy (FICLONE + copy fallback) (NEX-728)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 2: blob cache + Materialize revision (NEX-729)

**Files:** Modify `internal/worktree/fs.go`, `internal/worktree/worktree.go`, `internal/worktree/fs_test.go`; Create `internal/worktree/cache_test.go`

- [ ] **Step 1: Update the existing `fs_test.go` calls + add a dedup test.** `Materialize` gains a `cacheDir` param: `Materialize(eng, cacheDir, commitSha, dir)`. Update the two existing `fs_test.go` tests to pass a `cacheDir := filepath.Join(t.TempDir(), "cache")`. Then add to a new `cache_test.go`:

```go
package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

func TestMaterializeCachesBlobsDeduped(t *testing.T) {
	eng, err := change.Open(t.TempDir())
	if err != nil { t.Fatalf("Open: %v", err) }
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")
	// two files with IDENTICAL content + one distinct
	same := []byte("shared\n")
	r, err := eng.Commit(ch.ID, map[string][]byte{"a.txt": same, "b.txt": same, "c.txt": []byte("other\n")})
	if err != nil { t.Fatalf("Commit: %v", err) }

	cacheDir := filepath.Join(t.TempDir(), "cache")
	dir := filepath.Join(t.TempDir(), "wc")
	if err := Materialize(eng, cacheDir, r.HeadCommit, dir); err != nil { t.Fatalf("Materialize: %v", err) }

	// cache/blobs has exactly 2 entries (shared content once + the distinct one)
	entries, err := os.ReadDir(filepath.Join(cacheDir, "blobs"))
	if err != nil { t.Fatalf("read cache: %v", err) }
	if len(entries) != 2 { t.Fatalf("cache blobs = %d, want 2 (dedup)", len(entries)) }
	// the shared blob key exists
	sum := sha256.Sum256(same)
	if _, err := os.Stat(filepath.Join(cacheDir, "blobs", hex.EncodeToString(sum[:]))); err != nil {
		t.Fatalf("shared blob missing: %v", err)
	}
}

func TestMaterializeCoWIsolation(t *testing.T) {
	eng, _ := change.Open(t.TempDir())
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")
	content := []byte("v1\n")
	r, err := eng.Commit(ch.ID, map[string][]byte{"f.txt": content})
	if err != nil { t.Fatalf("Commit: %v", err) }
	cacheDir := filepath.Join(t.TempDir(), "cache")
	a := filepath.Join(t.TempDir(), "A")
	b := filepath.Join(t.TempDir(), "B")
	if err := Materialize(eng, cacheDir, r.HeadCommit, a); err != nil { t.Fatal(err) }
	if err := Materialize(eng, cacheDir, r.HeadCommit, b); err != nil { t.Fatal(err) }
	// overwrite A's file → B + cache blob unchanged
	if err := os.WriteFile(filepath.Join(a, "f.txt"), []byte("CHANGED\n"), 0o644); err != nil { t.Fatal(err) }
	gotB, _ := os.ReadFile(filepath.Join(b, "f.txt"))
	if string(gotB) != "v1\n" { t.Fatalf("B mutated to %q (CoW isolation broken)", gotB) }
	sum := sha256.Sum256(content)
	gotCache, _ := os.ReadFile(filepath.Join(cacheDir, "blobs", hex.EncodeToString(sum[:])))
	if string(gotCache) != "v1\n" { t.Fatalf("cache blob mutated to %q", gotCache) }
}
```

- [ ] **Step 2: Run, verify fail** (Materialize signature mismatch / new behavior).

- [ ] **Step 3: Revise `Materialize` in `fs.go`**

```go
import (
	"crypto/sha256"
	"encoding/hex"
	// ... existing
)

// Materialize writes the tree at commitSha into dir, reflinking each file from a
// content-addressed blob cache under cacheDir (so identical content shares disk
// blocks across branches/commits where the FS supports reflinks; plain copy
// otherwise). Replaces dir's current contents.
func Materialize(eng *change.Engine, cacheDir, commitSha, dir string) error {
	files, err := eng.Files(commitSha)
	if err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	blobs := filepath.Join(cacheDir, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		return fmt.Errorf("worktree.Materialize: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("worktree.Materialize: clear: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("worktree.Materialize: mkdir: %w", err)
	}
	for p, data := range files {
		sum := sha256.Sum256(data)
		key := hex.EncodeToString(sum[:])
		cacheBlob := filepath.Join(blobs, key)
		if _, err := os.Stat(cacheBlob); errors.Is(err, os.ErrNotExist) {
			if err := writeFileAtomic(cacheBlob, data); err != nil {
				return fmt.Errorf("worktree.Materialize: cache write: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("worktree.Materialize: cache stat: %w", err)
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("worktree.Materialize: %w", err)
		}
		if err := reflinkOrCopy(cacheBlob, full); err != nil {
			return err
		}
	}
	return nil
}

// writeFileAtomic writes data to path via a temp file + rename.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
```
Add `errors` to imports if not present.

- [ ] **Step 4: Update `worktree.go` call sites** — every `Materialize(r.eng, X, dir)` becomes `Materialize(r.eng, r.cacheDir(), X, dir)` where `cacheDir()` returns `filepath.Join(r.root, ".cairn", "cache")`. Add the `cacheDir()` helper. There are call sites in `Express`, `Commit`, `Fold`, `Resolve` — update all.

- [ ] **Step 5: Run, verify pass.** `go test ./internal/worktree/ -v` (the two new cache tests + ALL existing worktree tests pass — the converge/resolve/abandon tests must stay green, proving transparency). `go vet ./internal/worktree/`.

- [ ] **Step 6: Confirm the CLI e2e still passes.** `go test ./cmd/cairn/ -v` (Materialize is called transitively; the signature change is internal to worktree so the CLI is unaffected). `go build ./...`.

- [ ] **Step 7: Commit**

```bash
git add internal/worktree/fs.go internal/worktree/worktree.go internal/worktree/fs_test.go internal/worktree/cache_test.go
git commit -m "feat(worktree): blob cache + reflink Materialize (NEX-729)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Task 3: block-sharing proof + full gate (NEX-730)

**Files:** Modify `internal/worktree/cache_test.go`

- [ ] **Step 1: Add a reflink-gated block-sharing test.** Only asserts real sharing where the FS supports it; skips otherwise (ext4 CI, macOS, Windows) so CI stays green.

```go
import "syscall"

func TestMaterializeSharesBlocksWhenReflinkSupported(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "cache")
	if !reflinkSupported(filepath.Dir(cacheDir)) {
		t.Skip("filesystem does not support reflinks")
	}
	eng, _ := change.Open(t.TempDir())
	t.Cleanup(func() { _ = eng.Close() })
	main, _ := eng.LineByName("main")
	ch, _ := eng.CreateChange(main.ID, "t")
	// a reasonably large file so block sharing is measurable
	big := make([]byte, 1<<20) // 1 MiB of zeros (or fill)
	for i := range big { big[i] = byte(i % 251) }
	r, err := eng.Commit(ch.ID, map[string][]byte{"big.bin": big})
	if err != nil { t.Fatalf("Commit: %v", err) }
	a := filepath.Join(t.TempDir(), "A")
	b := filepath.Join(t.TempDir(), "B")
	if err := Materialize(eng, cacheDir, r.HeadCommit, a); err != nil { t.Fatal(err) }
	if err := Materialize(eng, cacheDir, r.HeadCommit, b); err != nil { t.Fatal(err) }
	// Both files + the cache blob are reflinks → total allocated blocks ≈ one copy,
	// not three. Assert A and B together do not allocate ~2x the file size.
	var sa, sb syscall.Stat_t
	if err := syscall.Stat(filepath.Join(a, "big.bin"), &sa); err != nil { t.Fatal(err) }
	if err := syscall.Stat(filepath.Join(b, "big.bin"), &sb); err != nil { t.Fatal(err) }
	// st_blocks is 512-byte units; reflinked files still REPORT their full apparent
	// blocks individually, so a per-file st_blocks check won't prove sharing.
	// Instead assert filesystem free space barely moved across the second Materialize.
	// (Implement via unix.Statfs before/after the 2nd Materialize; require the
	//  free-block delta to be far less than the file size when reflink is supported.)
}
```
NOTE for the implementer: per-file `st_blocks` does NOT reveal reflink sharing (each file reports its full size). Prove sharing instead by measuring **filesystem free space** (`unix.Statfs`) immediately before and after materializing the *second* copy of a large file: with reflink, free space drops by ~0 (blocks shared); without, by ~the file size. Gate the whole test on `reflinkSupported`. Choose a file size (e.g. 4–8 MiB) comfortably above filesystem noise, and assert the free-space delta for the second Materialize is `< fileSize/2`. If this proves flaky against background FS activity, fall back to the documented `FIDEDUPERANGE`/extent-map check, or assert via `cp --reflink` parity — but keep it gated and non-flaky. If you cannot make it robust, leave the dedup + CoW-isolation tests (Task 2) as the proof and add this as `t.Skip` with a clear reason rather than a flaky assertion.

- [ ] **Step 2: Run on this Btrfs host.** `go test ./internal/worktree/ -run TestMaterializeSharesBlocks -v` → should run (not skip) here and PASS.

- [ ] **Step 3: Full gate.** `go test ./... && go vet ./... && go build ./...` green. Cross-compile check: `GOOS=darwin go build ./... && GOOS=windows go build ./...`.

- [ ] **Step 4: Commit**

```bash
git add internal/worktree/cache_test.go
git commit -m "test(worktree): reflink-gated block-sharing proof (NEX-730)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:** reflinkOrCopy+fallback (§3) → T1; blob cache + Materialize (§2,§4) → T2; block-sharing + transparency (§7) → T2/T3. ✓
**Out of scope:** cache GC (§6 deferred), remotes (Slice C), OverlayFS (rejected). ✓
**Type/signature consistency:** `Materialize(eng, cacheDir, commitSha, dir)` is the one signature change — all call sites are inside `internal/worktree` (Express/Commit/Fold/Resolve + fs_test helpers); the `Repo`/CLI surface is unchanged, so Slice-A's `worktree`/`cmd/cairn` tests must keep passing (the transparency proof). `reflinkOrCopy`/`reflinkSupported` defined once per build-tag.
**Sharp edges:** (1) build tags — `_other.go` must compile + pass on macOS/Windows CI (plain copy). (2) cache and branch folders must be same-FS (both under repo root) for reflink — true by construction. (3) block-sharing test must be reflink-gated + non-flaky (free-space delta on a large file), else skip. (4) atomic cache writes so concurrent identical-content writers don't corrupt.
