//go:build windows

package worktree

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// On Windows, a file or directory cannot be renamed or removed while another
// process holds an open handle to it — and antivirus scanners, the search
// indexer, and Explorer routinely open files moments after they are written.
// os.Rename/os.RemoveAll then fail with ERROR_SHARING_VIOLATION or
// ERROR_ACCESS_DENIED. These locks clear on their own within milliseconds, so the
// fix (as in git and go-git) is to retry briefly. This is the rename race seen
// with `express --from`, whose Materialize swaps a freshly-built temp dir into
// place with RemoveAll + Rename.

// Windows error codes for the two transient file-lock failures. Go's stdlib
// syscall package does not export ERROR_SHARING_VIOLATION, so define both locally
// (as syscall.Errno values, which is what os.LinkError/PathError wrap).
const (
	errSharingViolation = syscall.Errno(32) // ERROR_SHARING_VIOLATION
	errAccessDenied     = syscall.Errno(5)  // ERROR_ACCESS_DENIED
)

// transientWindows reports whether err is a retryable Windows file-lock error.
func transientWindows(err error) bool {
	return errors.Is(err, errSharingViolation) || errors.Is(err, errAccessDenied)
}

// retryWindows runs op, retrying on transient file-lock errors with exponential
// backoff (~0.5s total across 10 attempts) before returning the last error. A
// non-transient error (or success) returns immediately.
func retryWindows(op func() error) error {
	delay := time.Millisecond
	var err error
	for i := 0; i < 10; i++ {
		if err = op(); err == nil || !transientWindows(err) {
			return err
		}
		time.Sleep(delay)
		if delay < 128*time.Millisecond {
			delay *= 2
		}
	}
	return err
}

// renameWithRetry renames oldpath to newpath, retrying transient Windows locks.
func renameWithRetry(oldpath, newpath string) error {
	return retryWindows(func() error { return os.Rename(oldpath, newpath) })
}

// removeAllWithRetry removes path and any children, retrying transient locks.
func removeAllWithRetry(path string) error {
	return retryWindows(func() error { return os.RemoveAll(path) })
}
