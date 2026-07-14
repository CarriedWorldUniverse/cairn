//go:build !windows

package worktree

import (
	"os"
	"path/filepath"
	"syscall"
)

// openLockFile opens (creating if needed) cairnDir/name and takes an EXCLUSIVE
// advisory flock, BLOCKING until it is acquired. The kernel drops the lock when
// the returned file is closed or the process exits, so a crashed cairn can
// never leave a stale lock. This is the shared primitive behind both wc.lock
// (issue #81) and the per-remote remote.lock (issue #98 Phase B): both are
// plain flock'd files, differing only in name and in which state they guard.
func openLockFile(cairnDir, name string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(cairnDir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// closeLockFile releases the advisory lock. Closing the fd alone releases it;
// the explicit LOCK_UN is belt-and-suspenders. The lock FILE is left in place
// and reused — the flock lives on the open file description, not the path.
func closeLockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
}

// openWCLock opens (creating if needed) .cairn/wc.lock and takes an EXCLUSIVE
// advisory flock, BLOCKING until it is acquired. See openLockFile; this is the
// fix for issue #81 on POSIX (the deployment target): concurrent cairn
// processes sharing one working copy serialize their wc.json read-modify-write
// instead of clobbering each other.
func openWCLock(cairnDir string) (*os.File, error) {
	return openLockFile(cairnDir, "wc.lock")
}

// closeWCLock releases the wc.lock. See closeLockFile.
func closeWCLock(f *os.File) {
	closeLockFile(f)
}
