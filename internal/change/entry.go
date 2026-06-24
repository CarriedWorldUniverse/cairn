package change

// TreeEntry is one path's content reference for tree-building: a pre-stored git
// blob SHA plus its mode. (The blob is guaranteed already in the object store.)
type TreeEntry struct {
	SHA  string
	Mode EntryMode
}
