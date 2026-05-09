// Package-level Service handle for the review-policy filter. Hooks read this
// without locking; SetGlobal during init / config reload.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package reviewpolicy

import "sync/atomic"

var globalService atomic.Pointer[Service]

// SetGlobal installs the process-wide Service. Pass nil to clear (useful in tests).
func SetGlobal(s *Service) { globalService.Store(s) }

// Global returns the installed Service, or nil if none set.
func Global() *Service { return globalService.Load() }
