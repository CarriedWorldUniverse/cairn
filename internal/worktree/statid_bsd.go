//go:build darwin || freebsd || netbsd

package worktree

import (
	"io/fs"
	"syscall"
)

// statIdentity: see statid_linux.go.
func statIdentity(info fs.FileInfo) (ctimeNs int64, ino uint64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return int64(st.Ctimespec.Sec)*1e9 + int64(st.Ctimespec.Nsec), uint64(st.Ino)
}
