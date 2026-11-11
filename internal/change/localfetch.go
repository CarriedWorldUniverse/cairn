package change

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/winretry"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// fetchLocal is the shortcut for cloning a repository that lives on the same
// filesystem, the way `git clone` (without --no-local) does it: instead of
// asking git-upload-pack to enumerate, delta-compress and stream the history
// back so it can be indexed again, link the remote's existing packs and their
// indexes straight into this store and write the refs the fetch would have
// written. On a 724k-object repository that is the difference between ~20
// seconds and well under one.
//
// It is deliberately narrow and declines (false, nil) whenever anything is
// not exactly the plain case, leaving the normal protocol fetch to do the
// work: the remote is not a local directory; this store already has objects
// (so this is an update, where the protocol's want/have negotiation is what
// keeps it small); the remote has loose objects, alternates, a shallow file
// or promisor packs (its history is not wholly in its packs); a pack has no
// index; or after linking, a ref's target is not readable. Anything it
// declines on after linking leaves only extra, unreferenced objects behind.
//
// CAIRN_NO_LOCAL_CLONE=1 turns it off.
func (e *Engine) fetchLocal(url string, specs []config.RefSpec) (bool, error) {
	if os.Getenv("CAIRN_NO_LOCAL_CLONE") == "1" || e.store == nil {
		return false, nil
	}
	remoteDir, ok := localGitDir(url)
	if !ok {
		return false, nil
	}
	if empty, err := e.objectsEmpty(); err != nil || !empty {
		return false, err
	}
	packs, ok := linkablePacks(remoteDir)
	if !ok || len(packs) == 0 {
		return false, nil
	}

	// The refs the fetch would write, resolved BEFORE anything is linked so a
	// remote we cannot read is declined without touching this store.
	rst := filesystem.NewStorage(osfs.New(remoteDir), cache.NewObjectLRUDefault())
	defer func() { _ = rst.Close() }()
	it, err := rst.IterReferences()
	if err != nil {
		return false, nil
	}
	var refs []*plumbing.Reference
	err = it.ForEach(func(r *plumbing.Reference) error {
		if r.Type() != plumbing.HashReference {
			return nil
		}
		for _, sp := range specs {
			if sp.Match(r.Name()) {
				refs = append(refs, plumbing.NewHashReference(sp.Dst(r.Name()), r.Hash()))
			}
		}
		return nil
	})
	it.Close()
	if err != nil {
		return false, nil
	}

	dstPack := filepath.Join(e.store.gitDir, "objects", "pack")
	if err := os.MkdirAll(dstPack, 0o755); err != nil {
		return false, fmt.Errorf("change.fetchLocal: %w", err)
	}
	// A pack first, then its index, as packWriter does: a reader finds a pack
	// by its index, so it never sees one that is not whole.
	for _, base := range packs {
		for _, ext := range []string{".pack", ".idx"} {
			if err := linkOrCopy(base+ext, filepath.Join(dstPack, filepath.Base(base)+ext)); err != nil {
				return false, fmt.Errorf("change.fetchLocal: %w", err)
			}
		}
	}
	e.store.fast.Invalidate()
	e.store.mu.Lock()
	e.store.Storage.ObjectStorage.Reindex()
	e.store.mu.Unlock()

	for _, r := range refs {
		if !e.store.fast.Has(r.Hash()) {
			return false, nil // not wholly in the packs: let the protocol sort it out
		}
	}
	for _, r := range refs {
		if err := e.git.Storer.SetReference(r); err != nil {
			return false, fmt.Errorf("change.fetchLocal: %w", err)
		}
	}
	e.Progressf("cairn: local remote: linked %d pack(s), %d ref(s)\n", len(packs), len(refs))
	return true, nil
}

var scpLike = regexp.MustCompile(`^[A-Za-z0-9._-]+@?[A-Za-z0-9._-]*:`)

// localGitDir resolves url to the git directory of a repository on this
// filesystem: a bare repository, or a work tree's .git. A URL with a scheme
// (other than file://) or scp-style host:path is not local.
func localGitDir(url string) (string, bool) {
	p := strings.TrimPrefix(url, "file://")
	if p == url && (strings.Contains(url, "://") || (scpLike.MatchString(url) && !filepath.IsAbs(url) && !isWindowsDrive(url))) {
		return "", false
	}
	for _, d := range []string{p, filepath.Join(p, ".git")} {
		if st, err := os.Stat(filepath.Join(d, "objects")); err == nil && st.IsDir() {
			if st, err := os.Stat(filepath.Join(d, "HEAD")); err == nil && !st.IsDir() {
				return d, true
			}
		}
	}
	return "", false
}

func isWindowsDrive(s string) bool {
	return len(s) >= 2 && s[1] == ':' && (s[0]|0x20) >= 'a' && (s[0]|0x20) <= 'z'
}

var looseDir = regexp.MustCompile(`^[0-9a-f]{2}$`)

// objectsEmpty reports whether this store holds no objects at all: no pack and
// no loose-object directory.
func (e *Engine) objectsEmpty() (bool, error) {
	objects := filepath.Join(e.store.gitDir, "objects")
	ents, err := os.ReadDir(objects)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	for _, ent := range ents {
		if ent.IsDir() && looseDir.MatchString(ent.Name()) {
			return false, nil
		}
	}
	packs, err := os.ReadDir(filepath.Join(objects, "pack"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, ent := range packs {
		if strings.HasSuffix(ent.Name(), ".pack") {
			return false, nil
		}
	}
	return true, nil
}

// linkablePacks returns the extension-less path of every pack in the remote's
// object directory, or ok=false when that directory is not exactly "some
// packs and their indexes" (loose objects, alternates, shallow, a promisor
// pack, a pack without an index).
func linkablePacks(gitDir string) ([]string, bool) {
	for _, f := range []string{"shallow", filepath.Join("objects", "info", "alternates")} {
		if _, err := os.Lstat(filepath.Join(gitDir, f)); err == nil {
			return nil, false
		}
	}
	ents, err := os.ReadDir(filepath.Join(gitDir, "objects"))
	if err != nil {
		return nil, false
	}
	for _, ent := range ents {
		if ent.IsDir() && looseDir.MatchString(ent.Name()) {
			return nil, false
		}
	}
	pents, err := os.ReadDir(filepath.Join(gitDir, "objects", "pack"))
	if err != nil {
		return nil, false
	}
	var packs []string
	for _, ent := range pents {
		n := ent.Name()
		switch {
		case strings.HasSuffix(n, ".promisor"):
			return nil, false
		case strings.HasPrefix(n, "pack-") && strings.HasSuffix(n, ".pack"):
			base := filepath.Join(gitDir, "objects", "pack", strings.TrimSuffix(n, ".pack"))
			if st, err := os.Stat(base + ".idx"); err != nil || st.IsDir() {
				return nil, false
			}
			packs = append(packs, base)
		}
	}
	return packs, true
}

// linkOrCopy makes dst a hard link to src (what `git clone` does) and falls
// back to a copy when the two are on different filesystems or links are not
// allowed. An existing dst is left alone: packs are named by their content.
func linkOrCopy(src, dst string) error {
	if _, err := os.Lstat(dst); err == nil {
		return nil
	}
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "tmp_link_")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return winretry.Do(func() error { return os.Rename(tmp.Name(), dst) })
}
