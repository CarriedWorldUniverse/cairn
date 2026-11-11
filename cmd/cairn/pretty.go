package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// Commit pretty-printing in git's formats: the named ones (oneline, short,
// medium, full, fuller, raw), format:/tformat: placeholder strings, git's date
// styles, and ref decorations. Shared by log, show and rev-list.

// logFormat is a parsed --format/--pretty/--oneline choice.
type logFormat struct {
	kind         string // oneline | short | medium | full | fuller | raw | format
	tmpl         string // kind == format
	terminator   bool   // each entry ENDS with a newline (oneline, tformat) rather than entries being separated
	abbrevCommit bool
}

var namedFormats = map[string]bool{"oneline": true, "short": true, "medium": true, "full": true, "fuller": true, "raw": true}

// parseLogFormat reads --oneline, --format/--pretty and --abbrev-commit.
func parseLogFormat(g *gitOpts) (logFormat, error) {
	f := logFormat{kind: "medium"}
	if g.has("format") {
		v := g.str("format")
		switch {
		case v == "":
		case namedFormats[v]:
			f.kind = v
		case strings.HasPrefix(v, "format:"):
			f.kind, f.tmpl = "format", strings.TrimPrefix(v, "format:")
		case strings.HasPrefix(v, "tformat:"):
			f.kind, f.tmpl, f.terminator = "format", strings.TrimPrefix(v, "tformat:"), true
		case strings.Contains(v, "%"):
			f.kind, f.tmpl, f.terminator = "format", v, true
		default:
			return f, fmt.Errorf("%s: unknown pretty format %q (oneline, short, medium, full, fuller, raw, or format:<string>)", g.cmd, v)
		}
	}
	if g.defs["oneline"] != nil && g.on("oneline") {
		f.kind, f.abbrevCommit = "oneline", true
	}
	if f.kind == "oneline" {
		f.terminator = true
	}
	if g.defs["abbrev-commit"] != nil && g.has("abbrev-commit") {
		f.abbrevCommit = g.on("abbrev-commit")
	}
	return f, nil
}

// prettyPrinter renders commits.
type prettyPrinter struct {
	f        logFormat
	color    bool
	decorate bool   // show decorations in the named formats
	dateMode string // --date
	abbrev   int
	now      time.Time
	decor    map[string][]decoration
}

type decoration struct {
	text  string
	color string
}

// loadDecorations names every commit a line, tag or remote branch points at,
// in git's order: HEAD -> line, tags, remote branches, lines.
func loadDecorations(eng *change.Engine) (map[string][]decoration, error) {
	refs, err := eng.RefNames()
	if err != nil {
		return nil, err
	}
	out := map[string][]decoration{}
	var headSHA, headName string
	for _, r := range refs {
		if r.Head {
			headSHA, headName = r.SHA, r.Name
		}
	}
	if headSHA != "" {
		out[headSHA] = append(out[headSHA], decoration{"HEAD -> " + headName, cBold + cCyan})
	}
	for i := len(refs) - 1; i >= 0; i-- {
		r := refs[i]
		switch {
		case r.Head:
		case r.Kind == "tag":
			out[r.SHA] = append(out[r.SHA], decoration{"tag: " + r.Name, cBold + cYellow})
		case r.Kind == "remote":
			out[r.SHA] = append(out[r.SHA], decoration{r.Name, cBold + cRed})
		default:
			out[r.SHA] = append(out[r.SHA], decoration{r.Name, cBold + cGreen})
		}
	}
	return out, nil
}

// decorText renders a commit's decorations joined by ", " ("" when none).
func (p *prettyPrinter) decorText(sha string, color bool) string {
	ds := p.decor[sha]
	if len(ds) == 0 {
		return ""
	}
	parts := make([]string, len(ds))
	for i, d := range ds {
		parts[i] = d.text
		if color {
			if name, ok := strings.CutPrefix(d.text, "HEAD -> "); ok {
				parts[i] = paint(true, cBold+cCyan, "HEAD -> ") + paint(true, cBold+cGreen, name)
			} else {
				parts[i] = paint(true, d.color, d.text)
			}
		}
	}
	sep := ", "
	if color {
		sep = paint(true, cYellow, ", ")
	}
	return strings.Join(parts, sep)
}

// decorSuffix is " (a, b)" for the named formats, "" when off or none.
func (p *prettyPrinter) decorSuffix(sha string) string {
	if !p.decorate {
		return ""
	}
	d := p.decorText(sha, p.color)
	if d == "" {
		return ""
	}
	return " " + paint(p.color, cYellow, "(") + d + paint(p.color, cYellow, ")")
}

func (p *prettyPrinter) short(sha string) string {
	n := p.abbrev
	if n <= 0 {
		n = 7
	}
	if n > len(sha) {
		n = len(sha)
	}
	return sha[:n]
}

func (p *prettyPrinter) shortAll(shas []string) string {
	out := make([]string, len(shas))
	for i, s := range shas {
		out[i] = p.short(s)
	}
	return strings.Join(out, " ")
}

// entry renders one commit WITHOUT its final newline.
func (p *prettyPrinter) entry(c *change.CommitMeta) string {
	name := c.SHA
	if p.f.abbrevCommit {
		name = p.short(c.SHA)
	}
	switch p.f.kind {
	case "format":
		return p.expand(p.f.tmpl, c)
	case "oneline":
		return paint(p.color, cYellow, name) + p.decorSuffix(c.SHA) + " " + c.Subject()
	}
	var sb strings.Builder
	sb.WriteString(paint(p.color, cYellow, "commit "+name) + p.decorSuffix(c.SHA) + "\n")
	if p.f.kind == "raw" {
		fmt.Fprintf(&sb, "tree %s\n", c.Tree)
		for _, par := range c.Parents {
			fmt.Fprintf(&sb, "parent %s\n", par)
		}
		fmt.Fprintf(&sb, "author %s <%s> %s\n", c.Author.Name, c.Author.Email, formatDate(c.Author.When, "raw", p.now))
		fmt.Fprintf(&sb, "committer %s <%s> %s\n", c.Committer.Name, c.Committer.Email, formatDate(c.Committer.When, "raw", p.now))
		sb.WriteString("\n" + indentLines(c.Message))
		return strings.TrimRight(sb.String(), "\n")
	}
	if len(c.Parents) > 1 {
		fmt.Fprintf(&sb, "Merge: %s\n", p.shortAll(c.Parents))
	}
	switch p.f.kind {
	case "short":
		fmt.Fprintf(&sb, "Author: %s <%s>\n", c.Author.Name, c.Author.Email)
	case "medium":
		fmt.Fprintf(&sb, "Author: %s <%s>\n", c.Author.Name, c.Author.Email)
		fmt.Fprintf(&sb, "Date:   %s\n", formatDate(c.Author.When, p.dateMode, p.now))
	case "full":
		fmt.Fprintf(&sb, "Author: %s <%s>\n", c.Author.Name, c.Author.Email)
		fmt.Fprintf(&sb, "Commit: %s <%s>\n", c.Committer.Name, c.Committer.Email)
	case "fuller":
		fmt.Fprintf(&sb, "Author:     %s <%s>\n", c.Author.Name, c.Author.Email)
		fmt.Fprintf(&sb, "AuthorDate: %s\n", formatDate(c.Author.When, p.dateMode, p.now))
		fmt.Fprintf(&sb, "Commit:     %s <%s>\n", c.Committer.Name, c.Committer.Email)
		fmt.Fprintf(&sb, "CommitDate: %s\n", formatDate(c.Committer.When, p.dateMode, p.now))
	}
	sb.WriteString("\n")
	sb.WriteString("    " + c.Subject() + "\n")
	if body := c.Body(); body != "" && p.f.kind != "short" {
		sb.WriteString("    \n" + indentLines(body))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// indentLines indents every line of s by four spaces, as git does a message.
func indentLines(s string) string {
	var sb strings.Builder
	for _, ln := range strings.Split(s, "\n") {
		sb.WriteString("    " + ln + "\n")
	}
	return sb.String()
}

// expand substitutes git's pretty-format placeholders.
func (p *prettyPrinter) expand(tmpl string, c *change.CommitMeta) string {
	var sb strings.Builder
	date := func(s change.Signature, mode string) string { return formatDate(s.When, mode, p.now) }
	for i := 0; i < len(tmpl); i++ {
		if tmpl[i] != '%' || i+1 >= len(tmpl) {
			sb.WriteByte(tmpl[i])
			continue
		}
		rest := tmpl[i+1:]
		take := func(n int, s string) {
			sb.WriteString(s)
			i += n
		}
		two := ""
		if len(rest) >= 2 {
			two = rest[:2]
		}
		switch {
		case rest[0] == '%':
			take(1, "%")
		case rest[0] == 'n':
			take(1, "\n")
		case rest[0] == 'x' && len(rest) >= 3:
			if b, err := strconv.ParseUint(rest[1:3], 16, 8); err == nil {
				take(3, string([]byte{byte(b)}))
			} else {
				sb.WriteByte('%')
			}
		case rest[0] == 'H':
			take(1, c.SHA)
		case rest[0] == 'h':
			take(1, p.short(c.SHA))
		case rest[0] == 'T':
			take(1, c.Tree)
		case rest[0] == 't':
			take(1, p.short(c.Tree))
		case rest[0] == 'P':
			take(1, strings.Join(c.Parents, " "))
		case rest[0] == 'p':
			take(1, p.shortAll(c.Parents))
		case two == "an", two == "aN":
			take(2, c.Author.Name)
		case two == "ae", two == "aE":
			take(2, c.Author.Email)
		case two == "al", two == "aL":
			take(2, strings.SplitN(c.Author.Email, "@", 2)[0])
		case two == "ad":
			take(2, date(c.Author, p.dateMode))
		case two == "aD":
			take(2, date(c.Author, "rfc"))
		case two == "ar":
			take(2, date(c.Author, "relative"))
		case two == "at":
			take(2, date(c.Author, "unix"))
		case two == "ai":
			take(2, date(c.Author, "iso"))
		case two == "aI":
			take(2, date(c.Author, "iso-strict"))
		case two == "as":
			take(2, date(c.Author, "short"))
		case two == "cn", two == "cN":
			take(2, c.Committer.Name)
		case two == "ce", two == "cE":
			take(2, c.Committer.Email)
		case two == "cl", two == "cL":
			take(2, strings.SplitN(c.Committer.Email, "@", 2)[0])
		case two == "cd":
			take(2, date(c.Committer, p.dateMode))
		case two == "cD":
			take(2, date(c.Committer, "rfc"))
		case two == "cr":
			take(2, date(c.Committer, "relative"))
		case two == "ct":
			take(2, date(c.Committer, "unix"))
		case two == "ci":
			take(2, date(c.Committer, "iso"))
		case two == "cI":
			take(2, date(c.Committer, "iso-strict"))
		case two == "cs":
			take(2, date(c.Committer, "short"))
		case rest[0] == 's':
			take(1, c.Subject())
		case rest[0] == 'f':
			take(1, sanitizeSubject(c.Subject()))
		case rest[0] == 'b':
			b := c.Body()
			if b != "" {
				b += "\n"
			}
			take(1, b)
		case rest[0] == 'B':
			take(1, c.Message+"\n")
		case rest[0] == 'd':
			d := p.decorText(c.SHA, false)
			if d != "" {
				d = " (" + d + ")"
			}
			take(1, d)
		case rest[0] == 'D':
			take(1, p.decorText(c.SHA, false))
		case rest[0] == 'e', rest[0] == 'N':
			take(1, "")
		case rest[0] == 'm':
			take(1, ">")
		case rest[0] == 'C':
			n, code := parseColorPlaceholder(rest)
			if n == 0 {
				sb.WriteByte('%')
				continue
			}
			if p.color {
				sb.WriteString(code)
			}
			i += n
		default:
			sb.WriteByte('%')
		}
	}
	return sb.String()
}

var colorNames = map[string]string{
	"reset": cReset, "normal": "", "bold": cBold, "red": cRed, "green": cGreen, "yellow": cYellow,
	"blue": cBlue, "magenta": cMagenta, "cyan": cCyan, "white": "\x1b[37m", "black": "\x1b[30m",
	"dim": "\x1b[2m", "ul": "\x1b[4m", "reverse": "\x1b[7m", "auto": "",
}

// parseColorPlaceholder reads a %C… placeholder starting at the "C": %Cred,
// %Cgreen, %Cblue, %Creset, or %C(spec). It returns the bytes consumed (0 if
// it is not one) and the escape sequence.
func parseColorPlaceholder(rest string) (int, string) {
	if strings.HasPrefix(rest, "C(") {
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			return 0, ""
		}
		var code string
		for _, w := range strings.Fields(strings.ReplaceAll(rest[2:end], ",", " ")) {
			code += colorNames[strings.TrimPrefix(w, "auto:")]
		}
		return end + 1, code
	}
	for _, name := range []string{"reset", "red", "green", "blue"} {
		if strings.HasPrefix(rest[1:], name) {
			return 1 + len(name), colorNames[name]
		}
	}
	return 0, ""
}

// sanitizeSubject is git's %f / format-patch file-name form of a subject:
// runs of anything but letters, digits, '.' and '_' become one '-'.
func sanitizeSubject(s string) string {
	var sb strings.Builder
	dash := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_'
		if !ok {
			dash = sb.Len() > 0
			continue
		}
		if c == '.' && strings.HasSuffix(sb.String(), ".") && !dash {
			continue
		}
		if dash {
			sb.WriteByte('-')
			dash = false
		}
		sb.WriteByte(c)
	}
	return strings.TrimRight(sb.String(), ".-")
}

// ── dates ───────────────────────────────────────────────────────────────────

// formatDate renders t in one of git's --date styles. The commit's own
// timezone is kept unless the style ends in "-local".
func formatDate(t time.Time, mode string, now time.Time) string {
	if strings.HasSuffix(mode, "-local") {
		t = t.Local()
		mode = strings.TrimSuffix(mode, "-local")
	}
	switch mode {
	case "iso", "iso8601":
		return t.Format("2006-01-02 15:04:05 -0700")
	case "iso-strict", "iso8601-strict":
		return t.Format("2006-01-02T15:04:05-07:00")
	case "short":
		return t.Format("2006-01-02")
	case "unix":
		return strconv.FormatInt(t.Unix(), 10)
	case "raw":
		return fmt.Sprintf("%d %s", t.Unix(), t.Format("-0700"))
	case "rfc", "rfc2822":
		return t.Format("Mon, 2 Jan 2006 15:04:05 -0700")
	case "relative":
		return relativeDate(t, now)
	case "local":
		return t.Local().Format("Mon Jan 2 15:04:05 2006")
	}
	if f, ok := strings.CutPrefix(mode, "format:"); ok {
		return strftime(t, f)
	}
	return t.Format("Mon Jan 2 15:04:05 2006 -0700")
}

func validDateMode(mode string) bool {
	switch strings.TrimSuffix(mode, "-local") {
	case "", "default", "iso", "iso8601", "iso-strict", "iso8601-strict", "short", "unix", "raw", "rfc", "rfc2822", "relative", "local", "human":
		return true
	}
	return strings.HasPrefix(mode, "format:")
}

// strftime covers the conversions people actually put in --date=format:.
func strftime(t time.Time, f string) string {
	var sb strings.Builder
	for i := 0; i < len(f); i++ {
		if f[i] != '%' || i+1 >= len(f) {
			sb.WriteByte(f[i])
			continue
		}
		i++
		switch f[i] {
		case 'Y':
			sb.WriteString(t.Format("2006"))
		case 'y':
			sb.WriteString(t.Format("06"))
		case 'm':
			sb.WriteString(t.Format("01"))
		case 'd':
			sb.WriteString(t.Format("02"))
		case 'e':
			sb.WriteString(t.Format("_2"))
		case 'H':
			sb.WriteString(t.Format("15"))
		case 'I':
			sb.WriteString(t.Format("03"))
		case 'M':
			sb.WriteString(t.Format("04"))
		case 'S':
			sb.WriteString(t.Format("05"))
		case 'p':
			sb.WriteString(t.Format("PM"))
		case 'z':
			sb.WriteString(t.Format("-0700"))
		case 'Z':
			sb.WriteString(t.Format("MST"))
		case 'a':
			sb.WriteString(t.Format("Mon"))
		case 'A':
			sb.WriteString(t.Format("Monday"))
		case 'b', 'h':
			sb.WriteString(t.Format("Jan"))
		case 'B':
			sb.WriteString(t.Format("January"))
		case 'j':
			fmt.Fprintf(&sb, "%03d", t.YearDay())
		case 's':
			sb.WriteString(strconv.FormatInt(t.Unix(), 10))
		case 'F':
			sb.WriteString(t.Format("2006-01-02"))
		case 'T':
			sb.WriteString(t.Format("15:04:05"))
		case 'D':
			sb.WriteString(t.Format("01/02/06"))
		case 'n':
			sb.WriteByte('\n')
		case 't':
			sb.WriteByte('\t')
		case '%':
			sb.WriteByte('%')
		default:
			sb.WriteByte('%')
			sb.WriteByte(f[i])
		}
	}
	return sb.String()
}

// relativeDate is git's "3 days ago" rendering, thresholds and all.
func relativeDate(t, now time.Time) string {
	if now.Before(t) {
		return "in the future"
	}
	unit := func(n int64, one string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s ago", n, one)
		}
		return fmt.Sprintf("%d %ss ago", n, one)
	}
	diff := now.Unix() - t.Unix()
	if diff < 90 {
		return unit(diff, "second")
	}
	diff = (diff + 30) / 60
	if diff < 90 {
		return unit(diff, "minute")
	}
	diff = (diff + 30) / 60
	if diff < 36 {
		return unit(diff, "hour")
	}
	diff = (diff + 12) / 24
	if diff < 14 {
		return unit(diff, "day")
	}
	if diff < 70 {
		return unit((diff+3)/7, "week")
	}
	if diff < 365 {
		return unit((diff+15)/30, "month")
	}
	if diff < 1825 {
		total := (diff*12*2 + 365) / (365 * 2)
		years, months := total/12, total%12
		if months == 0 {
			return unit(years, "year")
		}
		ys := fmt.Sprintf("%d years", years)
		if years == 1 {
			ys = "1 year"
		}
		return ys + ", " + unit(months, "month")
	}
	return unit((diff+183)/365, "year")
}

var (
	agoRe  = regexp.MustCompile(`^(\d+)[ .]*(second|minute|hour|day|week|fortnight|month|year)s?(?:[ .]*ago)?$`)
	lastRe = regexp.MustCompile(`^last[ .]+(second|minute|hour|day|week|month|year)$`)
)

// parseDateArg reads a --since/--until value: a unix timestamp, an ISO or
// RFC date (local time when it names no zone), or a relative phrase such as
// "2 weeks ago", "3.days.ago", "yesterday", "last month".
func parseDateArg(s string, now time.Time) (time.Time, error) {
	raw := s
	s = strings.ToLower(strings.TrimSpace(s))
	if n, err := strconv.ParseInt(strings.TrimPrefix(s, "@"), 10, 64); err == nil && (strings.HasPrefix(s, "@") || len(s) >= 9) {
		return time.Unix(n, 0), nil
	}
	back := func(n int, unit string) time.Time {
		switch unit {
		case "second":
			return now.Add(-time.Duration(n) * time.Second)
		case "minute":
			return now.Add(-time.Duration(n) * time.Minute)
		case "hour":
			return now.Add(-time.Duration(n) * time.Hour)
		case "day":
			return now.AddDate(0, 0, -n)
		case "week":
			return now.AddDate(0, 0, -7*n)
		case "fortnight":
			return now.AddDate(0, 0, -14*n)
		case "month":
			return now.AddDate(0, -n, 0)
		}
		return now.AddDate(-n, 0, 0)
	}
	switch s {
	case "now":
		return now, nil
	case "today", "midnight":
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location()), nil
	case "yesterday":
		return now.AddDate(0, 0, -1), nil
	}
	if m := agoRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[1])
		return back(n, m[2]), nil
	}
	if m := lastRe.FindStringSubmatch(s); m != nil {
		return back(1, m[1]), nil
	}
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05",
		"2006-01-02 15:04", "2006-01-02", "2006/01/02", "01/02/2006", time.RFC1123Z, time.RFC1123,
		"Mon Jan 2 15:04:05 2006 -0700", "Mon Jan 2 15:04:05 2006", "Jan 2 2006", "2 Jan 2006",
	} {
		if t, err := time.ParseInLocation(layout, raw, now.Location()); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot read %q as a date (try 2026-01-31, \"2 weeks ago\", or a unix timestamp)", raw)
}

// ── patterns ────────────────────────────────────────────────────────────────

// goPattern converts a git pattern to Go regexp syntax. git's default is a
// POSIX BASIC expression, where ( ) { } | + ? are literal unless
// backslash-escaped — the reverse of Go's.
func goPattern(p string, fixed, extended bool) string {
	if fixed {
		return regexp.QuoteMeta(p)
	}
	var sb strings.Builder
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		if inClass {
			sb.WriteByte(c)
			if c == ']' {
				inClass = false
			}
			continue
		}
		switch {
		case c == '[':
			inClass = true
			sb.WriteByte(c)
			// A leading ] or ^] is a literal member of the class.
			if i+1 < len(p) && p[i+1] == '^' {
				sb.WriteByte('^')
				i++
			}
			if i+1 < len(p) && p[i+1] == ']' {
				sb.WriteString(`\]`)
				i++
			}
		case c == '\\' && i+1 < len(p):
			n := p[i+1]
			i++
			switch {
			case n == '<' || n == '>':
				sb.WriteString(`\b`)
			case !extended && strings.IndexByte("(){}|+?", n) >= 0:
				sb.WriteByte(n)
			default:
				sb.WriteByte('\\')
				sb.WriteByte(n)
			}
		case !extended && strings.IndexByte("(){}|+?", c) >= 0:
			sb.WriteByte('\\')
			sb.WriteByte(c)
		case !extended && c == '*' && i == 0:
			sb.WriteString(`\*`)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

func goPatterns(ps []string, fixed, extended bool) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = goPattern(p, fixed, extended)
	}
	return out
}
