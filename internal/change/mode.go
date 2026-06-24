package change

// EntryMode is a file's kind/permission as carried alongside content. The zero
// value (ModeRegular) is the default, so a sparse map (absent ⇒ regular) suffices.
type EntryMode int

const (
	ModeRegular EntryMode = iota
	ModeExecutable
	ModeSymlink
)
