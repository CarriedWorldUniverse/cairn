//go:build !windows

// Package winretry retries filesystem operations that transiently fail on Windows
// when antivirus, the search indexer, or Explorer briefly holds a handle on a
// just-written file (ERROR_SHARING_VIOLATION / ERROR_ACCESS_DENIED). On every
// other platform the operations are reliable, so Do runs the op once.
package winretry

// Do runs op exactly once on non-Windows platforms.
func Do(op func() error) error { return op() }
