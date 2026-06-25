package winretry

import "github.com/go-git/go-billy/v5"

// fsWrap wraps a billy.Filesystem so that Rename retries transient Windows file
// locks (Do is a single call on non-Windows). go-git writes every git object to a
// temp file and then renames it into the store; on a large tree that rename races
// antivirus/indexer handles and fails with "Access is denied". Giving go-git this
// filesystem makes those renames retry. Chroot is wrapped too so sub-filesystems
// (go-git chroots into the object store) keep the retry behaviour.
type fsWrap struct {
	billy.Filesystem
}

// FS returns inner wrapped so its Rename retries transient Windows file locks.
func FS(inner billy.Filesystem) billy.Filesystem { return &fsWrap{inner} }

func (f *fsWrap) Rename(from, to string) error {
	return Do(func() error { return f.Filesystem.Rename(from, to) })
}

func (f *fsWrap) Chroot(path string) (billy.Filesystem, error) {
	sub, err := f.Filesystem.Chroot(path)
	if err != nil {
		return nil, err
	}
	return &fsWrap{sub}, nil
}
