package change

import (
	"fmt"
	"github.com/go-git/go-git/v5/plumbing"
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
	c, err := e.git.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return nil, false, fmt.Errorf("change.FileLines: %w", err)
	}
	te, found, err := e.treePath(c.TreeHash, path)
	if err != nil || !found {
		return nil, false, err
	}
	data, err := e.readBlob(te.Hash.String())
	if err != nil {
		return nil, false, err
	}
	return splitKeepNL(data), true, nil
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
	newLines, ok, err := e.FileLines(commit, path)
	if err != nil || !ok {
		return LineLogStep{}, err
	}
	var oldLines []string
	hadFile := false
	if parent != "" {
		if oldLines, hadFile, err = e.FileLines(parent, path); err != nil {
			return LineLogStep{}, err
		}
	}
	s0, e0 := r.Start-1, min(r.End, len(newLines))
	if !hadFile {
		return LineLogStep{Touched: true, Patch: lineLogPatch(path, nil, newLines[s0:e0], 0, s0, false)}, nil
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
		step.Patch = lineLogPatch(path, oldLines[ps:pe], newLines[s0:e0], ps, s0, true)
	}
	return step, nil
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@.*$`)

// lineLogPatch renders the difference of two slices of a file as git's
// line-log does: a diff --git header with no index line, hunks that never
// reach outside the slices (3 lines of context at most), and counts always
// written.
func lineLogPatch(path string, old, new []string, oldOff, newOff int, hadFile bool) string {
	ops := diffOps(old, new, nil, nil)
	body := renderHunks(old, new, ops, 3)
	var sb strings.Builder
	from := "a/" + path
	if !hadFile {
		from = "/dev/null"
	}
	fmt.Fprintf(&sb, "diff --git %s %s\n--- %s\n+++ b/%s\n", QuotePath("a/"+path), QuotePath("b/"+path), from, path)
	for _, ln := range strings.SplitAfter(body, "\n") {
		if m := hunkHeader.FindStringSubmatch(strings.TrimSuffix(ln, "\n")); m != nil {
			num := func(s string, def int) int {
				if s == "" {
					return def
				}
				n, _ := strconv.Atoi(s)
				return n
			}
			fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", num(m[1], 0)+oldOff, num(m[2], 1), num(m[3], 0)+newOff, num(m[4], 1))
			continue
		}
		sb.WriteString(ln)
	}
	return sb.String()
}
