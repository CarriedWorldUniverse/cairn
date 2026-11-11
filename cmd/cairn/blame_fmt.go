package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// git's blame output formats (default, --porcelain, --line-porcelain).

type blameFmt struct {
	porcelain, repeat bool
	noAuthor, email   bool // -s, -e
	long              bool // -l
	number            bool // -n
	rawTime           bool // -t
	showName          bool // -f
	abbrev            int
	path              string // the file asked for
}

const zeroSHA = "0000000000000000000000000000000000000000"

func decimalWidth(n int) int { return len(strconv.Itoa(n)) }

// renderBlame prints the lines selected by keep (nil = all). isWorking marks
// commits that are the open working change: git calls those "Not Committed
// Yet".
func renderBlame(out io.Writer, lines []change.BlameLine, keep map[int]bool, isWorking func(string) bool, f blameFmt) error {
	type row struct {
		change.BlameLine
		final int // 0-based
	}
	var rows []row
	for i, ln := range lines {
		if keep != nil && !keep[i] {
			continue
		}
		if isWorking(ln.Commit) {
			ln.Commit, ln.Author, ln.AuthorEmail = zeroSHA, "Not Committed Yet", "not.committed.yet"
			ln.Committer, ln.CommitterEmail, ln.Summary = ln.Author, ln.AuthorEmail, ""
			ln.When = time.Now()
			ln.CommitterWhen = ln.When
			ln.Boundary = false
		}
		rows = append(rows, row{ln, i})
	}
	if len(rows) == 0 {
		return nil
	}
	// Alignment, as git's find_alignment.
	showName := f.showName
	longestFile, longestAuthor, maxFinal, maxSrc := 0, 0, 0, 0
	paths := map[string]map[string]bool{}
	for _, r := range rows {
		if r.Path != f.path {
			showName = true
		}
		longestFile = max(longestFile, len(r.Path))
		name := r.Author
		if f.email {
			name = "<" + r.AuthorEmail + ">"
		}
		longestAuthor = max(longestAuthor, utf8.RuneCountInString(name))
		maxFinal = max(maxFinal, r.final+1)
		maxSrc = max(maxSrc, r.OrigLine)
		if paths[r.Commit] == nil {
			paths[r.Commit] = map[string]bool{}
		}
		paths[r.Commit][r.Path] = true
	}
	maxDigits, maxOrig := decimalWidth(maxFinal), decimalWidth(maxSrc)

	hexLen := f.abbrev + 1
	if f.long {
		hexLen = 40
	}
	seen := map[string]bool{}
	for i := 0; i < len(rows); {
		// a group: consecutive final lines of one commit, contiguous in its file
		j := i + 1
		for j < len(rows) && rows[j].Commit == rows[i].Commit && rows[j].Path == rows[i].Path &&
			rows[j].final == rows[j-1].final+1 && rows[j].OrigLine == rows[j-1].OrigLine+1 {
			j++
		}
		g := rows[i]
		if f.porcelain {
			fmt.Fprintf(out, "%s %d %d %d\n", g.Commit, g.OrigLine, g.final+1, j-i)
			detail := func(force bool) {
				shown := seen[g.Commit] && !force
				if !shown {
					seen[g.Commit] = true
					fmt.Fprintf(out, "author %s\nauthor-mail <%s>\nauthor-time %d\nauthor-tz %s\n", g.Author, g.AuthorEmail, g.When.Unix(), g.When.Format("-0700"))
					fmt.Fprintf(out, "committer %s\ncommitter-mail <%s>\ncommitter-time %d\ncommitter-tz %s\n", g.Committer, g.CommitterEmail, g.CommitterWhen.Unix(), g.CommitterWhen.Format("-0700"))
					fmt.Fprintf(out, "summary %s\n", g.Summary)
					if g.Boundary {
						fmt.Fprintln(out, "boundary")
					}
				}
				if !shown || len(paths[g.Commit]) > 1 {
					if g.PrevCommit != "" {
						fmt.Fprintf(out, "previous %s %s\n", g.PrevCommit, g.PrevPath)
					}
					fmt.Fprintf(out, "filename %s\n", g.Path)
				}
			}
			detail(f.repeat)
			for k := i; k < j; k++ {
				if k > i {
					fmt.Fprintf(out, "%s %d %d\n", g.Commit, rows[k].OrigLine, rows[k].final+1)
					if f.repeat {
						detail(true)
					}
				}
				fmt.Fprintf(out, "\t%s\n", rows[k].Text)
			}
			i = j
			continue
		}
		for k := i; k < j; k++ {
			r := rows[k]
			var hex string
			if r.Boundary {
				hex = "^" + r.Commit[:hexLen-1]
			} else {
				hex = r.Commit[:hexLen]
			}
			var b strings.Builder
			b.WriteString(hex)
			if showName {
				fmt.Fprintf(&b, " %-*s", longestFile, r.Path)
			}
			if f.number {
				fmt.Fprintf(&b, " %*d", maxOrig, r.OrigLine)
			}
			if !f.noAuthor {
				name := r.Author
				if f.email {
					name = "<" + r.AuthorEmail + ">"
				}
				pad := longestAuthor - utf8.RuneCountInString(name)
				when := r.When.Format("2006-01-02 15:04:05 -0700")
				if f.rawTime {
					when = fmt.Sprintf("%d %s", r.When.Unix(), r.When.Format("-0700"))
				}
				fmt.Fprintf(&b, " (%s%s %10s", name, strings.Repeat(" ", pad), when)
			}
			fmt.Fprintf(&b, " %*d) %s\n", maxDigits, r.final+1, r.Text)
			if _, err := io.WriteString(out, b.String()); err != nil {
				return err
			}
		}
		i = j
	}
	return nil
}
