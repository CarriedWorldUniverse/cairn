package worktree

import (
	"os"

	"github.com/CarriedWorldUniverse/cairn/internal/winretry"
)

// renameWithRetry and removeAllWithRetry retry transient Windows file locks (an
// antivirus scanner or the search indexer briefly holding a handle) via
// winretry.Do; on non-Windows they are a single os.Rename / os.RemoveAll. Used by
// Materialize's swap and the atomic writes — see internal/winretry.
func renameWithRetry(oldpath, newpath string) error {
	return winretry.Do(func() error { return os.Rename(oldpath, newpath) })
}

func removeAllWithRetry(path string) error {
	return winretry.Do(func() error { return os.RemoveAll(path) })
}
