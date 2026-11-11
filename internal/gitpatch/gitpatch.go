// Package gitpatch parses unified diffs in the form `git diff` and
// `git format-patch` emit, and applies them to file content. It is pure: it
// knows nothing about cairn's object store or working copy — `cairn apply` and
// `cairn am` feed it bytes and write the result back themselves.
package gitpatch

import (
	"bytes"
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Line is one line of a hunk. Text carries its own line terminator, so a final
// line without one (git's "\ No newline at end of file") is simply a Text with
// no trailing "\n" — matching and applying need no special case for it.
type Line struct {
	Op   byte // ' ' context, '+' added, '-' removed
	Text string
}

// Hunk is one @@ block.
type Hunk struct {
	OldStart, OldLines int
	NewStart, NewLines int
	Lines              []Line
}

// File is the patch for one path.
type File struct {
	OldPath, NewPath string // "" for the /dev/null side
	IsNew, IsDelete  bool
	IsRename         bool
	OldMode, NewMode string // git octal modes ("100644", "100755", "120000"); "" when the patch does not say
	Binary           bool
	Hunks            []Hunk
}

// Path is the path the patch is "about": the new path, or the old one for a
// deletion.
func (f File) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// Stat returns the number of added and removed lines.
func (f File) Stat() (added, removed int) {
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			switch l.Op {
			case '+':
				added++
			case '-':
				removed++
			}
		}
	}
	return added, removed
}

// Reverse returns the patch that undoes f (`git apply -R`).
func (f File) Reverse() File {
	r := File{
		OldPath: f.NewPath, NewPath: f.OldPath,
		IsNew: f.IsDelete, IsDelete: f.IsNew,
		IsRename: f.IsRename,
		OldMode:  f.NewMode, NewMode: f.OldMode,
		Binary: f.Binary,
	}
	for _, h := range f.Hunks {
		rh := Hunk{OldStart: h.NewStart, OldLines: h.NewLines, NewStart: h.OldStart, NewLines: h.OldLines}
		for _, l := range h.Lines {
			switch l.Op {
			case '+':
				l.Op = '-'
			case '-':
				l.Op = '+'
			}
			rh.Lines = append(rh.Lines, l)
		}
		r.Hunks = append(r.Hunks, rh)
	}
	return r
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// Parse reads every file patch out of data. strip is git's -p<n>: the number
// of leading path components removed from each name (1 for the usual a/ b/
// prefixes). Text that is not part of a patch — a mail header, a commit
// message, a diffstat, a trailing signature — is skipped, and hunk bodies are
// read by their declared line counts, so such text can never be mistaken for
// hunk content (nor the reverse).
func Parse(data []byte, strip int) ([]File, error) {
	lines := splitKeepNL(data)
	var files []File
	var cur *File
	flush := func() {
		if cur != nil {
			files = append(files, *cur)
			cur = nil
		}
	}
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r\n")
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			cur = &File{}
			if a, b, ok := splitGitHeader(line[len("diff --git "):]); ok {
				cur.OldPath, cur.NewPath = stripPath(a, strip), stripPath(b, strip)
			}
		case cur != nil && len(cur.Hunks) == 0 && strings.HasPrefix(line, "old mode "):
			cur.OldMode = strings.TrimSpace(line[len("old mode "):])
		case cur != nil && len(cur.Hunks) == 0 && strings.HasPrefix(line, "new mode "):
			cur.NewMode = strings.TrimSpace(line[len("new mode "):])
		case cur != nil && len(cur.Hunks) == 0 && strings.HasPrefix(line, "new file mode "):
			cur.IsNew = true
			cur.NewMode = strings.TrimSpace(line[len("new file mode "):])
		case cur != nil && len(cur.Hunks) == 0 && strings.HasPrefix(line, "deleted file mode "):
			cur.IsDelete = true
			cur.OldMode = strings.TrimSpace(line[len("deleted file mode "):])
		case cur != nil && len(cur.Hunks) == 0 && strings.HasPrefix(line, "rename from "):
			cur.IsRename = true
			cur.OldPath = stripPath(unquote(line[len("rename from "):]), strip-1)
		case cur != nil && len(cur.Hunks) == 0 && strings.HasPrefix(line, "rename to "):
			cur.IsRename = true
			cur.NewPath = stripPath(unquote(line[len("rename to "):]), strip-1)
		case cur != nil && len(cur.Hunks) == 0 && (strings.HasPrefix(line, "copy from ") || strings.HasPrefix(line, "copy to ")):
			return nil, errors.New("copy patches are not supported")
		case cur != nil && (strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch"):
			cur.Binary = true
		case strings.HasPrefix(line, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ "):
			// A file header. With no preceding "diff --git" (a plain `diff -u`)
			// this is what opens the file patch.
			if cur == nil || len(cur.Hunks) > 0 {
				flush()
				cur = &File{}
			}
			oldName := headerName(line[4:])
			newName := headerName(strings.TrimRight(lines[i+1], "\r\n")[4:])
			if oldName == "/dev/null" {
				cur.IsNew = true
				cur.OldPath = ""
			} else if !cur.IsRename {
				cur.OldPath = stripPath(oldName, strip)
			}
			if newName == "/dev/null" {
				cur.IsDelete = true
				cur.NewPath = ""
			} else if !cur.IsRename {
				cur.NewPath = stripPath(newName, strip)
			}
			i++
		case cur != nil && strings.HasPrefix(line, "@@ "):
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("malformed hunk header: %q", line)
			}
			h := Hunk{OldLines: 1, NewLines: 1}
			h.OldStart, _ = strconv.Atoi(m[1])
			if m[2] != "" {
				h.OldLines, _ = strconv.Atoi(m[2])
			}
			h.NewStart, _ = strconv.Atoi(m[3])
			if m[4] != "" {
				h.NewLines, _ = strconv.Atoi(m[4])
			}
			oldLeft, newLeft := h.OldLines, h.NewLines
			for oldLeft > 0 || newLeft > 0 {
				i++
				if i >= len(lines) {
					return nil, fmt.Errorf("patch for %s ends inside a hunk", cur.Path())
				}
				raw := lines[i]
				if raw == "\n" || raw == "\r\n" {
					raw = " " + raw // an empty context line whose leading space a mailer stripped
				}
				op, text := raw[0], raw[1:]
				switch op {
				case ' ':
					oldLeft--
					newLeft--
				case '-':
					oldLeft--
				case '+':
					newLeft--
				case '\\':
					stripLastEOL(&h)
					continue
				default:
					return nil, fmt.Errorf("patch for %s: unexpected line in hunk: %q", cur.Path(), strings.TrimRight(raw, "\r\n"))
				}
				if oldLeft < 0 || newLeft < 0 {
					return nil, fmt.Errorf("patch for %s: hunk has more lines than its header declares", cur.Path())
				}
				h.Lines = append(h.Lines, Line{Op: op, Text: text})
			}
			// The marker for the hunk's very last line follows the counted body.
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], `\`) {
				i++
				stripLastEOL(&h)
			}
			cur.Hunks = append(cur.Hunks, h)
		}
	}
	flush()
	// Drop headers that carried nothing (e.g. a "diff --git" with no body and
	// no mode/rename information).
	out := files[:0]
	for _, f := range files {
		if f.OldPath == "" && f.NewPath == "" {
			continue
		}
		if f.IsNew {
			f.OldPath = ""
		}
		if f.IsDelete {
			f.NewPath = ""
		}
		out = append(out, f)
	}
	return out, nil
}

// stripLastEOL applies a "\ No newline at end of file" marker: the line just
// read has no terminator.
func stripLastEOL(h *Hunk) {
	if n := len(h.Lines); n > 0 {
		h.Lines[n-1].Text = strings.TrimSuffix(strings.TrimSuffix(h.Lines[n-1].Text, "\n"), "\r")
	}
}

// headerName extracts the file name from a "--- "/"+++ " line's remainder:
// anything after a tab is a timestamp (plain diff -u), and a quoted name is
// C-unquoted.
func headerName(s string) string {
	if i := strings.IndexByte(s, '\t'); i >= 0 {
		s = s[:i]
	}
	return unquote(strings.TrimRight(s, " "))
}

func unquote(s string) string {
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

// splitGitHeader splits the "a/x b/x" remainder of a "diff --git" line. The
// form is ambiguous when names contain spaces, so it is only trusted for the
// common case — both names equal behind their one-component prefixes — and
// otherwise left to the ---/+++ or rename lines that follow.
func splitGitHeader(s string) (a, b string, ok bool) {
	if strings.HasPrefix(s, `"`) {
		// "a/x y" "b/x y"
		parts := strings.SplitN(s, `" `, 2)
		if len(parts) == 2 {
			return unquote(parts[0] + `"`), unquote(parts[1]), true
		}
		return "", "", false
	}
	// Unquoted and symmetric: the two halves have equal length.
	if len(s)%2 == 1 && s[len(s)/2] == ' ' {
		a, b = s[:len(s)/2], s[len(s)/2+1:]
		if tail(a) == tail(b) {
			return a, b, true
		}
	}
	if i := strings.Index(s, " "); i >= 0 && strings.Count(s, " ") == 1 {
		return s[:i], s[i+1:], true
	}
	return "", "", false
}

func tail(p string) string {
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// stripPath removes n leading path components (git apply -p<n>).
func stripPath(p string, n int) string {
	for ; n > 0; n-- {
		i := strings.IndexByte(p, '/')
		if i < 0 {
			return p
		}
		p = p[i+1:]
	}
	return p
}

// splitKeepNL splits b into lines, each keeping its terminator.
func splitKeepNL(b []byte) []string {
	var out []string
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			out = append(out, string(b))
			break
		}
		out = append(out, string(b[:i+1]))
		b = b[i+1:]
	}
	return out
}

// Apply applies f's hunks to old and returns the new content. Like `git
// apply`, every hunk's context must match exactly; a hunk may sit at a
// different line than its header says (the file moved under it), in which case
// the nearest exact match at or after the previous hunk is used.
func Apply(old []byte, f File) ([]byte, error) {
	if f.Binary {
		return nil, fmt.Errorf("%s: binary patches are not supported", f.Path())
	}
	src := splitKeepNL(old)
	var out []string
	pos := 0 // next unconsumed line of src
	for n, h := range f.Hunks {
		var want, repl []string
		for _, l := range h.Lines {
			if l.Op != '+' {
				want = append(want, l.Text)
			}
			if l.Op != '-' {
				repl = append(repl, l.Text)
			}
		}
		prefer := h.OldStart - 1
		if h.OldLines == 0 {
			prefer = h.OldStart // "insert after line OldStart"
		}
		at := findHunk(src, want, prefer, pos)
		if at < 0 {
			return nil, fmt.Errorf("%s: patch does not apply (hunk #%d at line %d)", f.Path(), n+1, h.OldStart)
		}
		out = append(out, src[pos:at]...)
		out = append(out, repl...)
		pos = at + len(want)
	}
	out = append(out, src[pos:]...)
	return []byte(strings.Join(out, "")), nil
}

// findHunk returns the index in src where want matches exactly, searching
// outward from prefer and never before min; -1 when it matches nowhere.
func findHunk(src, want []string, prefer, min int) int {
	if prefer < min {
		prefer = min
	}
	match := func(at int) bool {
		if at < min || at+len(want) > len(src) {
			return false
		}
		for i, w := range want {
			if src[at+i] != w {
				return false
			}
		}
		return true
	}
	if len(want) == 0 {
		if prefer > len(src) {
			return len(src)
		}
		return prefer
	}
	for d := 0; d <= len(src); d++ {
		if match(prefer + d) {
			return prefer + d
		}
		if d > 0 && match(prefer-d) {
			return prefer - d
		}
	}
	return -1
}

// Mail is one message of `git format-patch` output: the commit's author, date
// and message plus its diff.
type Mail struct {
	AuthorName, AuthorEmail string
	Date                    time.Time
	Subject                 string
	Body                    string // message body below the subject ("" when none)
	Patch                   []byte
}

// Message is the commit message: subject, then the body as a paragraph.
func (m Mail) Message() string {
	if m.Body == "" {
		return m.Subject
	}
	return m.Subject + "\n\n" + m.Body
}

var (
	mboxFrom    = regexp.MustCompile(`(?m)^From [0-9a-f]{40} .*\r?\n`)
	patchPrefix = regexp.MustCompile(`^(\[[^\]]*\]\s*)+`)
)

// ParseMbox splits format-patch output (one or many messages) into Mails.
func ParseMbox(data []byte) ([]Mail, error) {
	var chunks [][]byte
	locs := mboxFrom.FindAllIndex(data, -1)
	if len(locs) == 0 {
		chunks = [][]byte{data}
	}
	for i, loc := range locs {
		end := len(data)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		chunks = append(chunks, data[loc[1]:end])
	}
	var out []Mail
	for n, c := range chunks {
		msg, err := mail.ReadMessage(bytes.NewReader(c))
		if err != nil {
			return nil, fmt.Errorf("patch %d: not a mail message: %w", n+1, err)
		}
		var m Mail
		dec := new(mime.WordDecoder)
		if from := msg.Header.Get("From"); from != "" {
			if a, aerr := mail.ParseAddress(from); aerr == nil {
				m.AuthorName, m.AuthorEmail = a.Name, a.Address
			} else {
				m.AuthorName = from
			}
		}
		if d := msg.Header.Get("Date"); d != "" {
			if t, derr := mail.ParseDate(d); derr == nil {
				m.Date = t
			}
		}
		subj := msg.Header.Get("Subject")
		if s, derr := dec.DecodeHeader(subj); derr == nil {
			subj = s
		}
		m.Subject = strings.TrimSpace(patchPrefix.ReplaceAllString(strings.TrimSpace(subj), ""))
		var body bytes.Buffer
		if _, err := body.ReadFrom(msg.Body); err != nil {
			return nil, fmt.Errorf("patch %d: %w", n+1, err)
		}
		text := strings.ReplaceAll(body.String(), "\r\n", "\n")
		// The message ends at the "---" separator format-patch writes before
		// the diffstat; a mail with no separator ends where the diff starts.
		msgPart, patchPart := text, ""
		if i := strings.Index("\n"+text, "\n---\n"); i >= 0 {
			msgPart, patchPart = text[:i], text[i+len("---\n"):]
		} else if i := strings.Index("\n"+text, "\ndiff --git "); i >= 0 {
			msgPart, patchPart = text[:i], text[i:]
		}
		msgPart = strings.TrimSpace(msgPart)
		// An in-body "From:" names the real author when the sender differs.
		if strings.HasPrefix(msgPart, "From: ") {
			first, rest, _ := strings.Cut(msgPart, "\n")
			if a, aerr := mail.ParseAddress(strings.TrimPrefix(first, "From: ")); aerr == nil {
				m.AuthorName, m.AuthorEmail = a.Name, a.Address
				msgPart = strings.TrimSpace(rest)
			}
		}
		m.Body = msgPart
		m.Patch = []byte(patchPart)
		if m.Subject == "" {
			return nil, fmt.Errorf("patch %d: no Subject header", n+1)
		}
		out = append(out, m)
	}
	return out, nil
}
