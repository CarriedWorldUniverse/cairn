//go:build windows

// Package winretry retries filesystem operations that transiently fail on Windows
// when antivirus, the search indexer, or Explorer briefly holds a handle on a
// just-written file. The probability scales with the number of files written, so
// on a large repo a single snapshot writes thousands of objects and a rename into
// the store is near-certain to hit a transient lock — the failures the maintainer
// observed at scale on Windows. The locks clear within milliseconds, so retry.
package winretry

import (
	"errors"
	"syscall"
	"time"
)

// Windows error codes for the transient file-lock failures. Go's stdlib syscall
// does not export ERROR_SHARING_VIOLATION, so define both locally (as
// syscall.Errno values, which is what os.LinkError / os.PathError wrap).
const (
	errSharingViolation = syscall.Errno(32) // ERROR_SHARING_VIOLATION
	errAccessDenied     = syscall.Errno(5)  // ERROR_ACCESS_DENIED
)

func transient(err error) bool {
	return errors.Is(err, errSharingViolation) || errors.Is(err, errAccessDenied)
}

// Do runs op, retrying on transient file-lock errors with exponential backoff
// (~1.6s across 12 attempts) before returning the last error. A non-transient
// error (or success) returns immediately.
func Do(op func() error) error {
	delay := time.Millisecond
	var err error
	for i := 0; i < 12; i++ {
		if err = op(); err == nil || !transient(err) {
			return err
		}
		time.Sleep(delay)
		if delay < 128*time.Millisecond {
			delay *= 2
		}
	}
	return err
}
