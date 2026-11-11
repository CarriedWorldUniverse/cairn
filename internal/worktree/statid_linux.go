//go:build linux

package worktree

import (
	"io/fs"
	"syscall"
)

// statIdentity returns the change time (ns) and inode number of a stat result.
// Unlike mtime, neither can be set by a user-space tool: `cp -p`, `touch -r`,
// rsync -a and os.Chtimes all restore mtime, but every write still bumps ctime
// and a replace-by-rename brings a new inode. ok is false when the platform
// gives no such data, in which case both values are 0.
func statIdentity(info fs.FileInfo) (ctimeNs int64, ino uint64) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0
	}
	return int64(st.Ctim.Sec)*1e9 + int64(st.Ctim.Nsec), uint64(st.Ino)
}
