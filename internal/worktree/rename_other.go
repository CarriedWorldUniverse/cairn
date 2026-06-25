//go:build !windows

package worktree

import "os"

// renameWithRetry renames oldpath to newpath. On POSIX platforms os.Rename is
// atomic and replaces the destination, so a single call suffices. The Windows
// build (rename_windows.go) retries transient file-lock errors.
func renameWithRetry(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }

// removeAllWithRetry removes path and any children. See rename_windows.go for the
// Windows retry rationale; on POSIX this is a plain os.RemoveAll.
func removeAllWithRetry(path string) error { return os.RemoveAll(path) }
