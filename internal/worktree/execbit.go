package worktree

import "os"

// execBitSupported reports whether dir's filesystem can represent the
// executable bit at all. On Windows it cannot: Go reports every file as
// 0o666/0o444 and os.Chmod only toggles the read-only attribute, so an
// executable entry in the tree can never be observed as executable on disk.
//
// Without this probe the working copy trusted the filesystem's bit (#161): on
// Windows every 100755 entry was rewritten on each materialize, showed as
// modified on a clean checkout, and had its mode silently flipped to 100644 by
// the next commit — history rewritten for everyone. git avoids this with
// core.fileMode=false; this is cairn's equivalent, probed rather than assumed
// so a FAT volume on Linux gets the same treatment as NTFS.
//
// Probed by creating a file, chmod'ing it 0o755 and reading the mode back,
// once per scan/materialize — never per file.
func execBitSupported(dir string) bool {
	f, err := os.CreateTemp(dir, ".execbit-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	defer os.Remove(name)
	if err := os.Chmod(name, 0o755); err != nil {
		return false
	}
	st, err := os.Lstat(name)
	if err != nil {
		return false
	}
	return st.Mode()&0o111 != 0
}

// execBitProbe is the seam tests use to simulate a filesystem without the bit
// (a Windows working copy) on a filesystem that has one.
var execBitProbe = execBitSupported
