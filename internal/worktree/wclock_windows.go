//go:build windows

package worktree

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// openWCLock opens (creating if needed) .cairn/wc.lock and takes an EXCLUSIVE
// byte-range lock via LockFileEx, BLOCKING until it is acquired. Windows
// releases the lock when the handle is closed OR the owning process exits — so,
// exactly like POSIX flock in wclock_unix.go, a crashed or killed cairn can
// never leave a stale lock behind.
//
// This replaces the previous O_EXCL lock-FILE approach, whose lock persisted
// after a crash and had to be removed by hand (issue #90's Windows facet: a
// slow commit killed by a wrapper timeout wedged the working copy until a manual
// `Remove-Item wc.lock`). Like the POSIX path, this also serializes concurrent
// cairn processes sharing one working copy (#81).
func openWCLock(cairnDir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(cairnDir, "wc.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	// Lock a single nominal byte at offset 0; any consistent range serializes
	// all lockers. Without LOCKFILE_FAIL_IMMEDIATELY the call BLOCKS until the
	// lock is available — matching the POSIX LOCK_EX blocking semantics.
	var ol windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ol); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// closeWCLock releases the byte-range lock and closes the handle. Closing the
// handle alone releases the lock; the explicit UnlockFileEx is
// belt-and-suspenders. The lock FILE is left in place and reused — the lock
// lives on the handle, not the path.
func closeWCLock(f *os.File) {
	var ol windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
	_ = f.Close()
}
