package packread

import (
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

func git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// buildRepo makes a repository whose history gives a packer something to
// delta: a large text file edited a little in every commit, a binary file,
// nested directories, an executable, a symlink, an annotated tag.
func buildRepo(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	rng := rand.New(rand.NewSource(7))
	lines := make([]string, 3000)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d: %x", i, rng.Int63())
	}
	bin := make([]byte, 40000)
	rng.Read(bin)
	if err := os.MkdirAll(filepath.Join(dir, "a", "b", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	for c := 0; c < 40; c++ {
		for e := 0; e < 5; e++ {
			lines[rng.Intn(len(lines))] = fmt.Sprintf("edit %d.%d: %x", c, e, rng.Int63())
		}
		must(t, os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
		must(t, os.WriteFile(filepath.Join(dir, "a", "b", "c", fmt.Sprintf("f%d.txt", c%7)), []byte(fmt.Sprintf("content %d\n", c)), 0o644))
		if c%10 == 0 {
			copy(bin[rng.Intn(len(bin)-100):], []byte(fmt.Sprintf("patch-%d", c)))
			must(t, os.WriteFile(filepath.Join(dir, "a", "blob.bin"), bin, 0o644))
		}
		if c == 3 {
			must(t, os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\n"), 0o755))
			must(t, os.Symlink("big.txt", filepath.Join(dir, "link")))
			must(t, os.WriteFile(filepath.Join(dir, "empty"), nil, 0o644))
		}
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-q", "-m", fmt.Sprintf("commit %d", c))
	}
	git(t, dir, "tag", "-a", "v1", "-m", "annotated")
	return dir
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// allObjects lists every object id in the repository at gitDir.
func allObjects(t testing.TB, gitDir string) []plumbing.Hash {
	t.Helper()
	out := git(t, gitDir, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	var hs []plumbing.Hash
	for _, l := range strings.Fields(out) {
		hs = append(hs, plumbing.NewHash(l))
	}
	if len(hs) == 0 {
		t.Fatal("repository has no objects")
	}
	return hs
}

// checkAgainstGoGit reads every object through both packread and go-git and
// requires the same type and bytes — and that the bytes hash to the id they
// were asked for, so the two cannot be wrong together.
func checkAgainstGoGit(t *testing.T, gitDir string) {
	t.Helper()
	ref := filesystem.NewStorage(osfs.New(gitDir), cache.NewObjectLRUDefault())
	s := Open(filepath.Join(gitDir, "objects"))
	defer s.Close()
	for _, h := range allObjects(t, gitDir) {
		typ, data, ok := s.Get(h)
		if !ok {
			t.Fatalf("%s: packread could not read an object git lists", h)
		}
		if !s.Has(h) {
			t.Fatalf("%s: Has is false for an object Get returned", h)
		}
		if got := plumbing.ComputeHash(typ, data); got != h {
			t.Fatalf("%s: content hashes to %s (type %s, %d bytes)", h, got, typ, len(data))
		}
		obj, err := ref.EncodedObject(plumbing.AnyObject, h)
		if err != nil {
			t.Fatalf("%s: go-git: %v", h, err)
		}
		r, err := obj.Reader()
		must(t, err)
		want, err := io.ReadAll(r)
		must(t, err)
		_ = r.Close()
		if obj.Type() != typ || !bytes.Equal(want, data) {
			t.Fatalf("%s: packread (%s, %d bytes) != go-git (%s, %d bytes)", h, typ, len(data), obj.Type(), len(want))
		}
	}
	missing := plumbing.NewHash("0123456789abcdef0123456789abcdef01234567")
	if _, _, ok := s.Get(missing); ok || s.Has(missing) {
		t.Fatal("an object that does not exist was found")
	}
}

func TestLooseObjects(t *testing.T) {
	dir := buildRepo(t)
	checkAgainstGoGit(t, filepath.Join(dir, ".git"))
}

// git repack writes offset deltas (OFS_DELTA) — what a clone's pack is made of.
func TestPackedOffsetDeltas(t *testing.T) {
	dir := buildRepo(t)
	git(t, dir, "repack", "-adfq", "--depth=50", "--window=50")
	gitDir := filepath.Join(dir, ".git")
	if ofs, _ := deltaKinds(t, gitDir); ofs == 0 {
		t.Fatal("the pack has no offset deltas; the test would prove nothing")
	}
	checkAgainstGoGit(t, gitDir)
}

// Bare pack-objects names delta bases by hash (REF_DELTA) — the other
// encoding, which a fetched pack may use.
func TestPackedRefDeltas(t *testing.T) {
	dir := buildRepo(t)
	bare := t.TempDir()
	git(t, bare, "init", "-q", "--bare")
	cmd := exec.Command("sh", "-c",
		"git rev-list --objects --all | git pack-objects -q --window=50 --depth=50 '"+filepath.Join(bare, "objects", "pack", "pack")+"'")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pack-objects: %v\n%s", err, out)
	}
	if _, ref := deltaKinds(t, bare); ref == 0 {
		t.Fatal("the pack has no reference deltas; the test would prove nothing")
	}
	checkAgainstGoGit(t, bare)
}

// Several packs plus loose objects on top, as a repository that has been
// fetched into and committed to looks.
func TestSeveralPacksAndLoose(t *testing.T) {
	dir := buildRepo(t)
	git(t, dir, "repack", "-dq") // pack 1: everything so far
	must(t, os.WriteFile(filepath.Join(dir, "big.txt"), []byte("rewritten\n"), 0o644))
	git(t, dir, "commit", "-qam", "second pack")
	git(t, dir, "repack", "-dq") // pack 2: the new commit
	must(t, os.WriteFile(filepath.Join(dir, "loose.txt"), []byte("loose\n"), 0o644))
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "loose")
	gitDir := filepath.Join(dir, ".git")
	packs, _ := filepath.Glob(filepath.Join(gitDir, "objects", "pack", "*.pack"))
	if len(packs) < 2 {
		t.Fatalf("want at least 2 packs, have %d", len(packs))
	}
	checkAgainstGoGit(t, gitDir)
}

// A pack written after the store was first used is invisible until
// Invalidate; objects read before it stay readable after.
func TestInvalidateSeesNewPack(t *testing.T) {
	dir := buildRepo(t)
	git(t, dir, "repack", "-adq")
	gitDir := filepath.Join(dir, ".git")
	s := Open(filepath.Join(gitDir, "objects"))
	defer s.Close()
	before := allObjects(t, gitDir)
	if _, _, ok := s.Get(before[0]); !ok {
		t.Fatal("could not read from the first pack")
	}

	must(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644))
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "new")
	git(t, dir, "repack", "-dq")
	head := plumbing.NewHash(strings.TrimSpace(git(t, dir, "rev-parse", "HEAD")))
	// A pack another process wrote after this Store's scan is found by the
	// miss itself (it re-lists objects/pack once before giving up).
	if typ, _, why, ok := s.GetWhy(head); !ok || typ != plumbing.CommitObject {
		t.Fatalf("a pack written after the scan was not found on a miss: %s", why)
	}
	s.Invalidate()
	if typ, _, ok := s.Get(head); !ok || typ != plumbing.CommitObject {
		t.Fatal("the new pack was not picked up after Invalidate")
	}
	if _, _, ok := s.Get(before[0]); !ok {
		t.Fatal("the first pack was lost by Invalidate")
	}
}

func TestClosedStoreDeclines(t *testing.T) {
	dir := buildRepo(t)
	git(t, dir, "repack", "-adq")
	gitDir := filepath.Join(dir, ".git")
	s := Open(filepath.Join(gitDir, "objects"))
	h := allObjects(t, gitDir)[0]
	if _, _, ok := s.Get(h); !ok {
		t.Fatal("could not read before Close")
	}
	s.Close()
	if _, _, ok := s.Get(h); ok || s.Has(h) {
		t.Fatal("a closed store answered")
	}
	s.Close() // twice is fine
}

// Damage must come back as "not here" (so the caller asks go-git), never as
// a panic and never as bytes that do not hash to the id.
func TestDamagedPackIsDeclinedNotTrusted(t *testing.T) {
	dir := buildRepo(t)
	git(t, dir, "repack", "-adfq")
	gitDir := filepath.Join(dir, ".git")
	ids := allObjects(t, gitDir)
	pack := firstPack(t, gitDir)
	orig, err := os.ReadFile(pack)
	must(t, err)
	must(t, os.Chmod(pack, 0o644))
	rng := rand.New(rand.NewSource(3))

	damage := map[string]func() []byte{
		"truncated": func() []byte { return orig[:len(orig)/2] },
		"flipped bytes": func() []byte {
			d := append([]byte(nil), orig...)
			for i := 0; i < 200; i++ {
				d[12+rng.Intn(len(d)-12)] ^= byte(1 + rng.Intn(255))
			}
			return d
		},
		"empty": func() []byte { return nil },
	}
	for name, mk := range damage {
		t.Run(name, func(t *testing.T) {
			must(t, os.WriteFile(pack, mk(), 0o644))
			s := Open(filepath.Join(gitDir, "objects"))
			defer s.Close()
			read := 0
			for _, h := range ids {
				typ, data, ok := s.Get(h)
				if !ok {
					continue
				}
				read++
				if got := plumbing.ComputeHash(typ, data); got != h {
					// zlib's checksum and the delta bounds checks catch
					// nearly all damage; what they cannot catch is a flip
					// inside a stored (uncompressed) literal that keeps the
					// stream valid — vanishingly unlikely with 200 flips.
					t.Fatalf("%s: a damaged pack yielded content hashing to %s", h, got)
				}
			}
			t.Logf("%d of %d objects still readable", read, len(ids))
		})
	}
	must(t, os.WriteFile(pack, orig, 0o644))

	// A damaged index: the pack is left to go-git entirely.
	idx := strings.TrimSuffix(pack, ".pack") + ".idx"
	idxOrig, err := os.ReadFile(idx)
	must(t, err)
	must(t, os.Chmod(idx, 0o644))
	for name, d := range map[string][]byte{
		"truncated index": idxOrig[:len(idxOrig)/3],
		"empty index":     nil,
		"foreign index":   bytes.Repeat([]byte{0xAB}, len(idxOrig)),
	} {
		must(t, os.WriteFile(idx, d, 0o644))
		s := Open(filepath.Join(gitDir, "objects"))
		for _, h := range ids {
			if typ, data, ok := s.Get(h); ok && plumbing.ComputeHash(typ, data) != h {
				t.Fatalf("%s: %s yielded wrong content", name, h)
			}
		}
		s.Close()
	}
}

func TestApplyDeltaRejectsMalformed(t *testing.T) {
	base := []byte("0123456789")
	for name, delta := range map[string][]byte{
		"empty":                 {},
		"wrong base size":       {9, 3, 0x90, 3},
		"copy past base":        {10, 5, 0x91, 8, 5},
		"copy past result":      {10, 2, 0x90, 5},
		"insert past delta":     {10, 5, 5, 'a'},
		"insert past result":    {10, 1, 2, 'a', 'b'},
		"reserved instruction":  {10, 1, 0},
		"short result":          {10, 5, 0x90, 2},
		"truncated copy offset": {10, 5, 0x91},
		"unterminated size":     {0x8a},
	} {
		if out, err := applyDelta(base, delta); err == nil {
			t.Errorf("%s: accepted, produced %q", name, out)
		}
	}
	// And a well-formed one: copy base[2:5], insert "xy", copy base[0:1].
	out, err := applyDelta(base, []byte{10, 6, 0x91, 2, 3, 2, 'x', 'y', 0x90, 1})
	if err != nil || string(out) != "234xy0" {
		t.Fatalf("applyDelta = %q, %v; want \"234xy0\"", out, err)
	}
}

func firstPack(t testing.TB, gitDir string) string {
	t.Helper()
	packs, _ := filepath.Glob(filepath.Join(gitDir, "objects", "pack", "*.pack"))
	if len(packs) == 0 {
		t.Fatal("no pack")
	}
	return packs[0]
}

// deltaKinds counts the entries of the first pack stored as offset deltas
// and as reference deltas, by reading each entry's header.
func deltaKinds(t testing.TB, gitDir string) (ofs, ref int) {
	t.Helper()
	p := openPack(strings.TrimSuffix(firstPack(t, gitDir), ".pack"))
	if p == nil {
		t.Fatal("pack index not usable")
	}
	defer p.close()
	must(t, p.open())
	for i := 0; i < p.n; i++ {
		r, release, err := p.reader(p.offsetAt(i))
		must(t, err)
		typ, _, err := readHeader(r)
		release()
		must(t, err)
		switch typ {
		case typeOFSDelta:
			ofs++
		case typeREFDelta:
			ref++
		}
	}
	return ofs, ref
}

// TestFixture reads EVERY object of a real repository and checks that it
// hashes to its id. Point CAIRN_PACKREAD_FIXTURE at a git directory.
func TestFixture(t *testing.T) {
	gitDir := os.Getenv("CAIRN_PACKREAD_FIXTURE")
	if gitDir == "" {
		t.Skip("CAIRN_PACKREAD_FIXTURE not set")
	}
	s := Open(filepath.Join(gitDir, "objects"))
	defer s.Close()
	ids := allObjects(t, gitDir)
	byType := map[plumbing.ObjectType]int{}
	for _, h := range ids {
		typ, data, ok := s.Get(h)
		if !ok {
			t.Fatalf("%s: not readable", h)
		}
		if got := plumbing.ComputeHash(typ, data); got != h {
			t.Fatalf("%s: content hashes to %s", h, got)
		}
		byType[typ]++
	}
	t.Logf("%d objects verified: %v", len(ids), byType)
}

func BenchmarkGet(b *testing.B) {
	dir := buildRepo(b)
	git(b, dir, "repack", "-adfq")
	gitDir := filepath.Join(dir, ".git")
	ids := allObjects(b, gitDir)
	b.Run("packread", func(b *testing.B) {
		s := Open(filepath.Join(gitDir, "objects"))
		defer s.Close()
		for i := 0; i < b.N; i++ {
			if _, _, ok := s.Get(ids[i%len(ids)]); !ok {
				b.Fatal("miss")
			}
		}
	})
	b.Run("go-git", func(b *testing.B) {
		ref := filesystem.NewStorage(osfs.New(gitDir), cache.NewObjectLRUDefault())
		for i := 0; i < b.N; i++ {
			if _, err := ref.EncodedObject(plumbing.AnyObject, ids[i%len(ids)]); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// gitIndex is the index `git index-pack` writes for pack.
func gitIndex(t testing.TB, pack string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "git.idx")
	git(t, filepath.Dir(pack), "index-pack", "-o", out, pack)
	data, err := os.ReadFile(out)
	must(t, err)
	return data
}

// An index is fully determined by its pack, so IndexPack is held to the
// strictest standard there is: the same bytes git writes.
func TestIndexPackMatchesGit(t *testing.T) {
	dir := buildRepo(t)
	packs := map[string]string{}

	git(t, dir, "repack", "-adfq", "--depth=50", "--window=50")
	packs["offset deltas"] = firstPack(t, filepath.Join(dir, ".git"))

	refDir := t.TempDir()
	cmd := exec.Command("sh", "-c", "git rev-list --objects --all | git pack-objects -q --window=50 --depth=50 '"+filepath.Join(refDir, "pack")+"'")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pack-objects: %v\n%s", err, out)
	}
	refPacks, _ := filepath.Glob(filepath.Join(refDir, "*.pack"))
	packs["reference deltas"] = refPacks[0]

	plainDir := t.TempDir()
	cmd = exec.Command("sh", "-c", "git rev-list --objects --all | git pack-objects -q --window=0 '"+filepath.Join(plainDir, "pack")+"'")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pack-objects: %v\n%s", err, out)
	}
	plainPacks, _ := filepath.Glob(filepath.Join(plainDir, "*.pack"))
	packs["no deltas"] = plainPacks[0]

	for name, pack := range packs {
		t.Run(name, func(t *testing.T) {
			idxPath := filepath.Join(t.TempDir(), "cairn.idx")
			sum, err := IndexPack(pack, idxPath)
			must(t, err)
			if want := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(pack), "pack-"), ".pack"); sum.String() != want {
				t.Fatalf("checksum %s, the pack is named %s", sum, want)
			}
			got, err := os.ReadFile(idxPath)
			must(t, err)
			if want := gitIndex(t, pack); !bytes.Equal(got, want) {
				t.Fatalf("index differs from git's (%d bytes vs %d)", len(got), len(want))
			}
		})
	}
}

// What it cannot index completely it must refuse, writing nothing — the
// caller hands those to go-git.
func TestIndexPackDeclines(t *testing.T) {
	dir := buildRepo(t)
	git(t, dir, "repack", "-adfq")
	orig, err := os.ReadFile(firstPack(t, filepath.Join(dir, ".git")))
	must(t, err)

	// A thin pack: the last commit's objects, as deltas against objects the
	// pack does not contain.
	thinDir := t.TempDir()
	cmd := exec.Command("sh", "-c", "printf 'HEAD\\n^HEAD~1\\n' | git pack-objects -q --thin --stdout --revs > '"+filepath.Join(thinDir, "thin.pack")+"'")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pack-objects --thin: %v\n%s", err, out)
	}
	thin, err := os.ReadFile(filepath.Join(thinDir, "thin.pack"))
	must(t, err)

	rng := rand.New(rand.NewSource(5))
	flipped := append([]byte(nil), orig...)
	flipped[12+rng.Intn(len(flipped)-40)] ^= 0x04
	empty := append([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x00"), make([]byte, 20)...)

	for name, data := range map[string][]byte{
		"thin pack":       thin,
		"truncated":       orig[:len(orig)*2/3],
		"one flipped bit": flipped,
		"no trailer":      orig[:len(orig)-20],
		"not a pack":      []byte("definitely not a packfile, though long enough to have a header"),
		"zero objects":    empty,
		"empty file":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			d := t.TempDir()
			pack, idx := filepath.Join(d, "p.pack"), filepath.Join(d, "p.idx")
			must(t, os.WriteFile(pack, data, 0o644))
			if _, err := IndexPack(pack, idx); err == nil {
				t.Fatal("indexed without error")
			}
			if _, err := os.Stat(idx); err == nil {
				t.Fatal("an index was written for a pack that was declined")
			}
		})
	}
}

// TestIndexPackFixture indexes a real pack and compares with the index
// already beside it. Point CAIRN_PACKREAD_PACK at a .pack file.
func TestIndexPackFixture(t *testing.T) {
	pack := os.Getenv("CAIRN_PACKREAD_PACK")
	if pack == "" {
		t.Skip("CAIRN_PACKREAD_PACK not set")
	}
	idxPath := filepath.Join(t.TempDir(), "cairn.idx")
	if _, err := IndexPack(pack, idxPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(idxPath)
	must(t, err)
	want, err := os.ReadFile(strings.TrimSuffix(pack, ".pack") + ".idx")
	must(t, err)
	if !bytes.Equal(got, want) {
		t.Fatalf("index differs from the existing one (%d bytes vs %d)", len(got), len(want))
	}
}

// A miss on an object that exists nowhere must say what was consulted, and
// repeated misses must not keep re-listing a directory that has not changed.
func TestMissExplainsItself(t *testing.T) {
	dir := buildRepo(t)
	git(t, dir, "repack", "-adq")
	s := Open(filepath.Join(dir, ".git", "objects"))
	defer s.Close()
	_, _, why, ok := s.GetWhy(plumbing.NewHash("1111111111111111111111111111111111111111"))
	if ok {
		t.Fatal("found an object that does not exist")
	}
	for _, want := range []string{"pack(s)", "re-listed", "loose"} {
		if !strings.Contains(why, want) {
			t.Fatalf("miss explanation %q lacks %q", why, want)
		}
	}
}
