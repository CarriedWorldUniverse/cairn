package change

// EntryMode is a file's kind/permission as carried alongside content. The zero
// value (ModeRegular) is the default, so a sparse map (absent ⇒ regular) suffices.
type EntryMode int

const (
	ModeRegular EntryMode = iota
	ModeExecutable
	ModeSymlink
	// ModeGitlink is a gitlink entry (git mode 160000): a nested checkout
	// recorded in the tree, whether or not the repo registers it in
	// .gitmodules. Its SHA names a COMMIT IN ANOTHER REPOSITORY, so unlike
	// every other mode there is no object in this store to read — any content
	// path (materialize, scan, diff, merge) must resolve it by reference alone
	// and never call readBlob on it (#140).
	ModeGitlink
)
