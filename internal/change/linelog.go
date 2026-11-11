package change

import (
	"fmt"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"regexp"
	"strconv"
	"strings"
)

// git log -L: follow a range of lines of one file back through history.

// LineRange is a range of lines, 1-based and inclusive; Empty when Start > End.
type LineRange struct{ Start, End int }

// Empty reports a range with no lines.
func (r LineRange) Empty() bool { return r.End < r.Start }

// FileLines returns the lines of path at commit, each with its newline; ok is
// false when the commit has no such file.
func (e *Engine) FileLines(commit, path string) (lines []string, ok bool, err error) {
	f, ok, err := e.fileAt(commit, path)
	return f.lines, ok, err
}

// lineLogFile is a file as of one commit: its lines and the blob and mode
// that name it in the diff header.
type lineLogFile struct {
	lines []string
	sha   string
	mode  string
}

func (e *Engine) fileAt(commit, path string) (lineLogFile, bool, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return lineLogFile{}, false, fmt.Errorf("change.FileLines: %w", err)
	}
	te, found, err := e.treePath(c.TreeHash, path)
	if err != nil || !found {
		return lineLogFile{}, false, err
	}
	data, err := e.readBlob(te.Hash.String())
	if err != nil {
		return lineLogFile{}, false, err
	}
	mode := "100644"
	switch te.Mode {
	case filemode.Executable:
		mode = "100755"
	case filemode.Symlink:
		mode = "120000"
	}
	return lineLogFile{splitKeepNL(data), te.Hash.String(), mode}, true, nil
}

// ParseLineRange reads git's -L <start>,<end> (the part before ":<file>")
// against a file's lines. start/end are a number, /regex/ (end: searched from
// start), or for end +N / -N; an omitted start is the first line, an omitted
// end the last.
func ParseLineRange(spec string, lines []string) (LineRange, error) {
	if len(lines) == 0 {
		return LineRange{}, fmt.Errorf("file has no lines")
	}
	sp, ep, hasEnd := strings.Cut(spec, ",")
	find := func(pat string, from int) (int, error) {
		re, err := regexp.Compile(pat)
		if err != nil {
			return 0, fmt.Errorf("-L: bad regex %q: %v", pat, err)
		}
		for i := from; i < len(lines); i++ {
			if re.MatchString(strings.TrimSuffix(lines[i], "\n")) {
				return i + 1, nil
			}
		}
		return 0, fmt.Errorf("-L: no match for /%s/", pat)
	}
	isRe := func(s string) bool { return len(s) >= 2 && s[0] == '/' && s[len(s)-1] == '/' }
	start := 1
	switch {
	case sp == "":
	case isRe(sp):
		n, err := find(sp[1:len(sp)-1], 0)
		if err != nil {
			return LineRange{}, err
		}
		start = n
	default:
		n, err := strconv.Atoi(sp)
		if err != nil || n < 1 {
			return LineRange{}, fmt.Errorf("-L: invalid line number %q", sp)
		}
		start = n
	}
	end := len(lines)
	switch {
	case !hasEnd:
		end = start
	case ep == "":
	case isRe(ep):
		n, err := find(ep[1:len(ep)-1], start-1)
		if err != nil {
			return LineRange{}, err
		}
		end = n
	case ep[0] == '+' || ep[0] == '-':
		n, err := strconv.Atoi(ep[1:])
		if err != nil || n < 1 {
			return LineRange{}, fmt.Errorf("-L: invalid line count %q", ep)
		}
		if ep[0] == '+' {
			end = start + n - 1
		} else {
			start, end = max(start-n+1, 1), start
		}
	default:
		n, err := strconv.Atoi(ep)
		if err != nil || n < 1 {
			return LineRange{}, fmt.Errorf("-L: invalid line number %q", ep)
		}
		end = n
	}
	if start > end {
		start, end = end, start
	}
	if start > len(lines) {
		return LineRange{}, fmt.Errorf("file has only %d lines", len(lines))
	}
	return LineRange{start, min(end, len(lines))}, nil
}

// funcLine reports whether a line starts a function by git's default rule: a
// letter, '_' or '$' in the first column.
func funcLine(l string) bool {
	return l != "" && (l[0] >= 'a' && l[0] <= 'z' || l[0] >= 'A' && l[0] <= 'Z' || l[0] == '_' || l[0] == '$')
}

// FuncLineRange is -L :<regex>:<file>: from the first function line matching
// re to the line before the next function line.
func FuncLineRange(re string, lines []string) (LineRange, error) {
	rx, err := regexp.Compile(re)
	if err != nil {
		return LineRange{}, fmt.Errorf("-L: bad regex %q: %v", re, err)
	}
	for i, l := range lines {
		l = strings.TrimSuffix(l, "\n")
		if !funcLine(l) || !rx.MatchString(l) {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if funcLine(lines[j]) {
				end = j
				break
			}
		}
		return LineRange{i + 1, end}, nil
	}
	return LineRange{}, fmt.Errorf("-L: no function matches /%s/", re)
}

// LineLogStep is one step of the walk: the tracked range r of path at commit,
// seen against parent ("" for none). It reports whether the commit changed
// the range, the range in the parent, and the patch of just the range.
type LineLogStep struct {
	Touched bool
	Parent  LineRange
	Patch   string
}

// LineLog compares the range with the parent's file. A parent without the
// file means the range was born in this commit.
func (e *Engine) LineLog(parent, commit, path string, r LineRange) (LineLogStep, error) {
	nf, ok, err := e.fileAt(commit, path)
	if err != nil || !ok {
		return LineLogStep{}, err
	}
	newLines := nf.lines
	var of lineLogFile
	hadFile := false
	if parent != "" {
		if of, hadFile, err = e.fileAt(parent, path); err != nil {
			return LineLogStep{}, err
		}
	}
	oldLines := of.lines
	s0, e0 := r.Start-1, min(r.End, len(newLines))
	if !hadFile {
		return LineLogStep{Touched: true, Patch: lineLogPatch(path, of, nf, nil, newLines[s0:e0], 0, s0, false, e0-s0)}, nil
	}
	ops := diffOps(oldLines, newLines, nil, nil)
	touched := false
	for _, op := range ops {
		switch {
		case op.tag == 'e':
		case op.j1 == op.j2:
			if s0 < op.j1 && op.j1 < e0 {
				touched = true
			}
		case op.j1 < e0 && op.j2 > s0:
			touched = true
		}
	}
	mapPos := func(x int, isEnd bool) int {
		delta := 0
		for _, op := range ops {
			if op.tag == 'e' {
				continue
			}
			if op.j2 <= x {
				if op.j1 == op.j2 && isEnd && op.j1 == x {
					continue
				}
				delta += (op.i2 - op.i1) - (op.j2 - op.j1)
				continue
			}
			if op.j1 < x {
				if isEnd {
					return op.i2
				}
				return op.i1
			}
			break
		}
		return x + delta
	}
	ps, pe := mapPos(s0, false), mapPos(e0, true)
	if pe < ps {
		pe = ps
	}
	step := LineLogStep{Touched: touched, Parent: LineRange{ps + 1, pe}}
	if touched {
		step.Patch = lineLogPatch(path, of, nf, oldLines[ps:pe], newLines[s0:e0], ps, s0, true, e0-s0)
	}
	return step, nil
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@.*$`)

// lineLogPatch renders the difference of two slices of a file as git's
// line-log does: a diff --git header with the mode and index lines, hunks that
// never reach outside the slices, and counts always written. git diffs with
// as many lines of context as the tracked range is long (at least 3), so a
// touched range shows whole; each hunk's section heading is found in the
// whole file's diff (see lineLogHeading).
func lineLogPatch(path string, of, nf lineLogFile, old, new []string, oldOff, newOff int, hadFile bool, rangeLen int) string {
	ctx := max(3, rangeLen)
	ops := diffOps(old, new, nil, nil)
	body := renderHunks(old, new, ops, ctx)
	heading := ""
	if hadFile {
		heading = lineLogHeading(of.lines, nf.lines, ctx, newOff, newOff+rangeLen)
	}
	var sb strings.Builder
	from := "a/" + path
	fmt.Fprintf(&sb, "diff --git %s %s\n", QuotePath("a/"+path), QuotePath("b/"+path))
	switch {
	case !hadFile:
		from = "/dev/null"
		fmt.Fprintf(&sb, "new file mode %s\n", nf.mode)
	case of.mode != nf.mode:
		fmt.Fprintf(&sb, "old mode %s\nnew mode %s\n", of.mode, nf.mode)
	}
	if of.sha != nf.sha {
		oldID := strings.Repeat("0", 7)
		if hadFile {
			oldID = of.sha[:7]
		}
		fmt.Fprintf(&sb, "index %s..%s", oldID, nf.sha[:7])
		if hadFile && of.mode == nf.mode {
			sb.WriteString(" " + nf.mode)
		}
		sb.WriteByte('\n')
	}
	fmt.Fprintf(&sb, "--- %s\n+++ b/%s\n", from, path)
	for _, ln := range strings.SplitAfter(body, "\n") {
		if m := hunkHeader.FindStringSubmatch(strings.TrimSuffix(ln, "\n")); m != nil {
			num := func(s string, def int) int {
				if s == "" {
					return def
				}
				n, _ := strconv.Atoi(s)
				return n
			}
			fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@%s\n", num(m[1], 0)+oldOff, num(m[2], 1), num(m[3], 0)+newOff, num(m[4], 1), heading)
			continue
		}
		sb.WriteString(ln)
	}
	return sb.String()
}

// lineLogHeading is the section heading of the hunk git shows for the range
// [s0, e0) of the new file. git diffs the two whole files with ctx lines of
// context and cuts the hunks down to the range, so the heading belongs to
// the whole-file hunk holding the range's first change (which may begin
// well before the range, and merge in changes outside it): the nearest
// earlier line of the old file that starts like a declaration (a letter,
// '_' or '$'), as git's default funcname rule has it.
func lineLogHeading(oldLines, newLines []string, ctx, s0, e0 int) string {
	ops := diffOps(oldLines, newLines, nil, nil)
	first, ingroup, found := 0, false, false
	for _, op := range ops {
		if op.tag == 'e' {
			// a gap of more than 2*ctx unchanged lines ends the hunk
			if found {
				break
			}
			if op.i2-op.i1 > 2*ctx {
				ingroup = false
			}
			continue
		}
		if !ingroup {
			first, ingroup = op.i1, true
		}
		if op.j1 == op.j2 && s0 < op.j1 && op.j1 < e0 || op.j1 != op.j2 && op.j1 < e0 && op.j2 > s0 {
			found = true
		}
	}
	if !found {
		return ""
	}
	for i := min(max(first-ctx, 0), len(oldLines)) - 1; i >= 0; i-- {
		if c := oldLines[i][0]; c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '$' {
			h := strings.TrimRight(oldLines[i], " \t\r\n")
			if len(h) > 80 {
				h = h[:80]
			}
			if h == "" {
				return ""
			}
			return " " + h
		}
	}
	return ""
}
