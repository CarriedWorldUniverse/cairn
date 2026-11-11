//go:build !(linux || darwin || freebsd || netbsd || dragonfly)

package worktree

import "io/fs"

// statIdentity: on platforms without a usable ctime/inode in os.FileInfo
// (Windows: Go exposes neither without opening the file) both are 0, so the
// cache degrades to the (mtime, size, mode) test plus the racy-write rule.
func statIdentity(info fs.FileInfo) (ctimeNs int64, ino uint64) { return 0, 0 }
