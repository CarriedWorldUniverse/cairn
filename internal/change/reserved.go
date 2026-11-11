package change

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// reservedMetaName reports whether a single tree-entry name would, on SOME
// filesystem, resolve to repository metadata (".cairn" or ".git"). It ports
// git's verify_path rules (is_ntfs_dotgit / is_hfs_dotgit) to both names, and
// applies them on every OS so a tree that is unsafe on one platform is refused
// everywhere (git does the same for ".git").
//
// A tree that materializes ".cairn/cairn.db" inside a line folder plants a fake
// catalogue that cairn's repo discovery could pick up (and ".git" does the same
// for git run in that folder), so these names are reserved as path components.
//
// Matched forms, for meta in {"git","cairn"}:
//   - "." + meta, case-insensitively, optionally followed by trailing '.'/' '
//     (NTFS strips them) or an NTFS stream suffix ':...';
//   - the NTFS 8.3 short name meta+"~N" (e.g. "GIT~1", "CAIRN~1"), likewise
//     optionally followed by trailing dots/spaces;
//   - the HFS+ form: Unicode "ignorable" code points are dropped by HFS+
//     normalization, so ".g‌it" is ".git" there.
func reservedMetaName(name string) (string, bool) {
	for _, meta := range [...]string{"cairn", "git"} {
		if isNTFSDotMeta(name, meta) || isHFSDotMeta(name, meta) {
			return "." + meta, true
		}
	}
	return "", false
}

// onlySpacesAndDots reports whether s is empty, a run of ' ' / '.', or such a
// run followed by an NTFS stream marker ':'.
func onlySpacesAndDots(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '.':
		case ':':
			return true
		default:
			return false
		}
	}
	return true
}

func isNTFSDotMeta(name, meta string) bool {
	if len(name) > len(meta) && name[0] == '.' && strings.EqualFold(name[1:1+len(meta)], meta) {
		return onlySpacesAndDots(name[1+len(meta):])
	}
	// 8.3 short name: meta~<digits>
	if len(name) > len(meta)+1 && strings.EqualFold(name[:len(meta)], meta) && name[len(meta)] == '~' {
		rest := name[len(meta)+1:]
		i := 0
		for i < len(rest) && rest[i] >= '0' && rest[i] <= '9' {
			i++
		}
		if i > 0 && rest[0] != '0' {
			return onlySpacesAndDots(rest[i:])
		}
	}
	return false
}

// hfsIgnorable reports the code points HFS+ drops during normalization
// (git's next_hfs_char table).
func hfsIgnorable(r rune) bool {
	switch {
	case r >= 0x200c && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x206a && r <= 0x206f, r == 0xfeff:
		return true
	}
	return false
}

func isHFSDotMeta(name, meta string) bool {
	want := "." + meta
	pos := 0
	for _, w := range want {
		var r rune
		for {
			if pos >= len(name) {
				return false
			}
			c, sz := utf8.DecodeRuneInString(name[pos:])
			pos += sz
			if c == utf8.RuneError && sz == 1 {
				return false
			}
			if hfsIgnorable(c) {
				continue
			}
			r = c
			break
		}
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r != w {
			return false
		}
	}
	for pos < len(name) {
		c, sz := utf8.DecodeRuneInString(name[pos:])
		if !(hfsIgnorable(c) && !(c == utf8.RuneError && sz == 1)) {
			return false
		}
		pos += sz
	}
	return true
}

// SafeName renders an externally supplied name for a terminal: control bytes
// (including ESC) and invalid UTF-8 are escaped, and the result is quoted
// only when something had to be escaped.
func SafeName(name string) string {
	clean := true
	for _, r := range name {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == utf8.RuneError {
			clean = false
			break
		}
	}
	if clean {
		return name
	}
	return strconv.QuoteToASCII(name)
}
