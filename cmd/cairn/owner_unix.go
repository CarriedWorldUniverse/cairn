//go:build !windows

package main

import (
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether path is a file owned by the invoking user.
func ownedByCurrentUser(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return int(st.Uid) == os.Geteuid()
}
