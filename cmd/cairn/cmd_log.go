package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

// log, show and shortlog in git's shape.
//
// The output is git's: the walk starts at HEAD (the SEALED tip), follows
// every parent, is unbounded, and prints git's formats; show prints git's
// medium header and a git-format patch. cairn's original compact views stay
// reachable with --classic (`log --classic [line] [-n N]`: first-parent, the
// open working change included, 20 entries; `show --classic <commit>`).
//
// The open working change is not a commit git knows about, so log leaves it
// out; `log --working` lists it first when it holds anything.

// diffSpec are the diff-output options log, show and diff share.
const diffSpec = "p|patch|u s|no-patch stat? shortstat numstat name-only name-status U|unified= " +
	"w|ignore-all-space color? full-index binary R summary stat-width= stat-name-width= stat-graph-width= stat-count= no-renames M|find-renames? no-ext-diff no-textconv"

// diffOut is a parsed choice of diff outputs.
type diffOut struct {
	patch, stat, shortstat, numstat, nameOnly, nameStatus bool
	summary                                               bool
	po                                                    change.PatchOptions
	reverse                                               bool
	color                                                 bool
	width                                                 int
	statOpts                                              change.StatOptions
}

// parseDiffOut reads the shared diff options. defaultPatch is what the
// command shows when no output option is given.
func parseDiffOut(g *gitOpts, defaultPatch bool) (diffOut, error) {
	d := diffOut{
		stat: g.on("stat") || g.on("stat-width") || g.on("stat-name-width") || g.on("stat-graph-width") || g.on("stat-count"), shortstat: g.on("shortstat"), numstat: g.on("numstat"),
		nameOnly: g.on("name-only"), nameStatus: g.on("name-status"),
		reverse: g.on("R"), width: 80, summary: g.on("summary"),
	}
	ctx, err := g.integer("U", 3)
	if err != nil {
		return d, err
	}
	d.po = change.PatchOptions{Context: ctx, IgnoreAllSpace: g.on("w"), FullIndex: g.on("full-index"), Binary: g.on("binary")}
	if err := d.parseStat(g); err != nil {
		return d, err
	}
	d.patch = g.on("p") || g.has("U") || g.on("binary")
	if d.nameOnly && d.nameStatus {
		return d, errors.New("options '--name-only', '--name-status', '--check', and '-s' cannot be used together")
	}
	if d.nameOnly || d.nameStatus {
		// As git: a name listing replaces the patch and every stat form.
		d.patch, d.stat, d.shortstat, d.numstat, d.summary = false, false, false, false, false
	}
	if !d.any() && defaultPatch {
		d.patch = true
	}
	return d, nil
}

// parseStat reads --stat=<width>[,<name-width>[,<count>]] and the separate
// --stat-width/--stat-name-width/--stat-graph-width/--stat-count options.
func (d *diffOut) parseStat(g *gitOpts) error {
	num := func(name string, dst *int) error {
		if !g.on(name) {
			return nil
		}
		n, err := strconv.Atoi(g.str(name))
		if err != nil || n < 0 {
			return fmt.Errorf("%s: invalid --%s value: %s", g.cmd, name, g.str(name))
		}
		*dst = n
		return nil
	}
	for _, v := range g.strs("stat") {
		if v == "" {
			continue
		}
		parts := strings.Split(v, ",")
		bad := len(parts) > 3
		var ns [3]int
		for i := 0; i < len(parts) && !bad; i++ {
			if parts[i] == "" && i > 0 {
				continue // "60,,1": the middle one keeps its default
			}
			n, err := strconv.Atoi(parts[i])
			if err != nil || n < 0 {
				bad = true
			}
			ns[i] = n
		}
		if bad {
			return fmt.Errorf("%s: invalid --stat value: %s", g.cmd, v)
		}
		d.statOpts.Width = ns[0]
		if len(parts) > 1 && parts[1] != "" {
			d.statOpts.NameWidth = ns[1]
		}
		if len(parts) > 2 && parts[2] != "" {
			d.statOpts.Count = ns[2]
		}
	}
	for _, e := range []struct {
		name string
		dst  *int
	}{{"stat-width", &d.statOpts.Width}, {"stat-name-width", &d.statOpts.NameWidth},
		{"stat-graph-width", &d.statOpts.GraphWidth}, {"stat-count", &d.statOpts.Count}} {
		if err := num(e.name, e.dst); err != nil {
			return err
		}
	}
	return nil
}

func (d diffOut) any() bool {
	return d.patch || d.stat || d.shortstat || d.numstat || d.nameOnly || d.nameStatus || d.summary
}

// none turns every output off (-s / --no-patch).
func (d diffOut) none() diffOut {
	d.patch, d.stat, d.shortstat, d.numstat, d.nameOnly, d.nameStatus, d.summary = false, false, false, false, false, false, false
	return d
}

// compute returns the per-file changes between two commits (a == "" is the
// empty tree), doing only as much work as the chosen outputs need: name
// listings never read a blob, stat-only output never renders a hunk.
func (d diffOut) compute(eng *change.Engine, a, b string, ps *change.Pathspec) ([]change.FilePatch, error) {
	if !d.any() {
		return nil, nil
	}
	if !d.patch && !d.stat && !d.shortstat && !d.numstat && !d.summary && !d.po.IgnoreAllSpace {
		changes, err := eng.TreeChanges(a, b, ps)
		if err != nil {
			return nil, err
		}
		out := make([]change.FilePatch, len(changes))
		for i, ch := range changes {
			if d.reverse {
				ch = ch.Reversed()
			}
			out[i] = change.FilePatch{Path: ch.Path, Status: ch.Status()}
		}
		return out, nil
	}
	po := d.po
	po.StatOnly = !d.patch
	return eng.CommitPatches(a, b, ps, po, d.reverse)
}

// render writes the chosen outputs in git's order: name listings, then
// stats, then a blank line and the patch.
func (d diffOut) render(patches []change.FilePatch) string {
	if len(patches) == 0 {
		return ""
	}
	var sb strings.Builder
	switch {
	case d.nameOnly:
		for _, p := range patches {
			sb.WriteString(change.QuotePath(p.Path) + "\n")
		}
	case d.nameStatus:
		for _, p := range patches {
			fmt.Fprintf(&sb, "%c\t%s\n", p.Status, change.QuotePath(p.Path))
		}
	}
	if d.numstat {
		for _, p := range patches {
			if p.Binary {
				fmt.Fprintf(&sb, "-\t-\t%s\n", change.QuotePath(p.Path))
			} else {
				fmt.Fprintf(&sb, "%d\t%d\t%s\n", p.Added, p.Deleted, change.QuotePath(p.Path))
			}
		}
	}
	if d.stat {
		sb.WriteString(d.colorStat(change.FormatStatOpts(patches, d.statOpts)))
	} else if d.shortstat {
		sb.WriteString(change.FormatShortStat(patches))
	}
	if d.summary {
		for _, p := range patches {
			sb.WriteString(p.Summary)
		}
	}
	if d.patch {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		for _, p := range patches {
			sb.WriteString(d.colorPatch(p.Text()))
		}
	}
	return sb.String()
}

func (d diffOut) colorStat(s string) string {
	if !d.color {
		return s
	}
	lines := strings.SplitAfter(s, "\n")
	for i, ln := range lines {
		bar := strings.LastIndex(ln, " | ")
		if bar < 0 {
			continue
		}
		head, tail := ln[:bar+3], ln[bar+3:]
		plus := strings.IndexByte(tail, '+')
		minus := strings.IndexByte(tail, '-')
		if strings.Contains(tail, "Bin") || (plus < 0 && minus < 0) {
			continue
		}
		cut := len(strings.TrimRight(tail, "+-\n"))
		graph := strings.TrimRight(tail[cut:], "\n")
		p := strings.Count(graph, "+")
		lines[i] = head + tail[:cut] + paint(true, cGreen, graph[:p]) + paint(true, cRed, graph[p:]) + "\n"
	}
	return strings.Join(lines, "")
}

// colorPatch colours a git-format patch as git does.
func (d diffOut) colorPatch(s string) string {
	if !d.color {
		return s
	}
	var sb strings.Builder
	inHunk := false
	for _, ln := range strings.SplitAfter(s, "\n") {
		if ln == "" {
			continue
		}
		body := strings.TrimSuffix(ln, "\n")
		switch {
		case strings.HasPrefix(body, "diff --git "):
			inHunk = false
			sb.WriteString(paint(true, cBold, body))
		case strings.HasPrefix(body, "@@"):
			inHunk = true
			if end := strings.Index(body[2:], "@@"); end >= 0 {
				sb.WriteString(paint(true, cCyan, body[:end+4]) + body[end+4:])
			} else {
				sb.WriteString(paint(true, cCyan, body))
			}
		case !inHunk:
			sb.WriteString(paint(true, cBold, body))
		case strings.HasPrefix(body, "+"):
			sb.WriteString(paint(true, cGreen, body))
		case strings.HasPrefix(body, "-"):
			sb.WriteString(paint(true, cRed, body))
		default:
			sb.WriteString(body)
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

const logSpec = "n|max-count= # skip= oneline format|pretty? abbrev-commit abbrev= date= decorate? graph " +
	"all first-parent merges no-merges reverse follow author= committer= grep= i|regexp-ignore-case " +
	"E|extended-regexp F|fixed-strings all-match invert-grep since|after= until|before= S= G= " +
	"topo-order date-order m no-walk classic working " + diffSpec

// walkFilters fills the filter half of a WalkOptions from the options that
// log, rev-list and shortlog share.
func walkFilters(g *gitOpts, o *change.WalkOptions) error {
	fixed := g.defs["F"] != nil && g.on("F")
	ext := g.defs["E"] != nil && g.on("E")
	o.Authors = goPatterns(g.strs("author"), fixed, ext)
	if g.defs["committer"] != nil && g.defs["committer"].kind == optValue {
		o.Committers = goPatterns(g.strs("committer"), fixed, ext)
	}
	o.Greps = goPatterns(g.strs("grep"), fixed, ext)
	o.IgnoreCase = g.defs["i"] != nil && g.on("i")
	o.AllMatch = g.defs["all-match"] != nil && g.on("all-match")
	o.InvertGrep = g.defs["invert-grep"] != nil && g.on("invert-grep")
	o.Merges, o.NoMerges = g.on("merges"), g.on("no-merges")
	o.FirstParent = g.on("first-parent")
	now := time.Now()
	var err error
	if g.has("since") {
		if o.Since, err = parseDateArg(g.str("since"), now); err != nil {
			return fmt.Errorf("%s: --since: %w", g.cmd, err)
		}
	}
	if g.has("until") {
		if o.Until, err = parseDateArg(g.str("until"), now); err != nil {
			return fmt.Errorf("%s: --until: %w", g.cmd, err)
		}
	}
	return nil
}

// walkRange resolves a command's revisions (default HEAD, or everything with
// --all) into the walk's include/exclude sets.
func walkRange(eng *change.Engine, g *gitOpts, revs []string, o *change.WalkOptions) error {
	rr, err := eng.ParseRevisions(revs)
	if err != nil {
		return mapErr(err)
	}
	if g.defs["all"] != nil && g.on("all") {
		tips, err := eng.AllTips()
		if err != nil {
			return mapErr(err)
		}
		if g.allNot {
			rr.Exclude = append(rr.Exclude, tips...)
		} else {
			rr.Include = append(rr.Include, tips...)
		}
	} else if len(rr.Include) == 0 && len(rr.Exclude) == 0 {
		head, err := eng.ResolveCommit("HEAD")
		if err != nil {
			return mapErr(err)
		}
		rr.Include = []string{head}
	}
	o.Include, o.Exclude = rr.Include, rr.Exclude
	return nil
}

// cmdLog prints commit history.
// Usage: cairn log [<revision range>] [options] [-- <path>...]
func cmdLog(args []string) error {
	g := newGitOpts("log", logSpec)
	if err := g.parse(args); err != nil {
		return err
	}
	r, err := openRepo(g.repoDir(), defaultAuthor())
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	eng := r.Engine()

	// cairn's original compact view: `cairn log --classic [line] [-n N]`.
	if g.on("classic") {
		if !g.onlyGiven("repo", "n", "classic") || len(g.pos) > 1 {
			return errors.New("usage: cairn log --classic [line] [-n N]")
		}
		return logClassic(r, g)
	}

	lc, err := currentLine(r, g.repoDir())
	if err != nil {
		return err
	}
	// --working on a line with nothing sealed yet still has the open change
	// to show; without it that line has no history, as in a fresh git repo.
	unborn := false
	revs, paths, err := splitRevsPaths(eng, lc, g)
	if errors.Is(err, change.ErrNoCommits) && g.on("working") && len(g.pos) <= 1 {
		unborn, revs, paths, err = true, g.pos, g.paths, nil
	}
	if err != nil {
		return mapErr(err)
	}
	if paths, err = lc.treePaths(paths); err != nil {
		return err
	}
	o := change.WalkOptions{Paths: paths, Reverse: g.on("reverse"), Topo: g.on("graph") || g.on("topo-order"),
		PickaxeS: g.str("S"), PickaxeG: g.str("G")}
	if err := walkFilters(g, &o); err != nil {
		return err
	}
	if g.on("follow") {
		if len(paths) != 1 {
			return errors.New("log: --follow requires exactly one path")
		}
		o.Follow = true
	}
	if err := walkRange(eng, g, revs, &o); err != nil {
		if !(errors.Is(err, change.ErrNoCommits) && g.on("working")) {
			return err
		}
		unborn = true
	}
	limit := -1
	if g.numSet {
		limit = g.num
	}
	if g.has("n") {
		if limit, err = g.integer("n", -1); err != nil {
			return err
		}
	}
	skip, err := g.integer("skip", 0)
	if err != nil {
		return err
	}
	if limit == 0 {
		return nil
	}
	graph := g.on("graph")
	if graph && o.Reverse {
		return errors.New("log: --graph and --reverse cannot be combined")
	}
	if !graph {
		// The graph needs the whole selection to place its columns, so it
		// applies skip/limit itself; otherwise the walk stops early.
		o.Skip = skip
		if limit > 0 {
			o.Limit = limit
		}
	}

	f, err := parseLogFormat(g)
	if err != nil {
		return err
	}
	d, err := parseDiffOut(g, false)
	if err != nil {
		return err
	}
	if g.has("s") {
		d = d.none()
	}
	color := useColor(g, r)
	d.color = color
	if !validDateMode(g.str("date")) {
		return fmt.Errorf("log: unknown date format %q", g.str("date"))
	}
	pp := &prettyPrinter{f: f, color: color, dateMode: g.str("date"), now: time.Now()}
	if pp.abbrev, err = g.integer("abbrev", 7); err != nil {
		return err
	}
	pp.decorate = g.on("decorate") || (!g.has("decorate") && stdoutIsTTY())
	if pp.decorate || strings.Contains(f.tmpl, "%d") || strings.Contains(f.tmpl, "%D") {
		if pp.decor, err = loadDecorations(eng); err != nil {
			return mapErr(err)
		}
	}
	basePS := change.NewPathspec(paths)
	mergeDiffs := g.on("m") || o.FirstParent

	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()
	lw := &logWriter{out: out, f: f, d: d}

	render := func(c *change.CommitMeta) (text, diff string, err error) {
		text = pp.entry(c)
		if !d.any() || (len(c.Parents) > 1 && !mergeDiffs) {
			return text, "", nil
		}
		ps := basePS
		if o.Follow && c.Path != "" {
			ps = change.NewPathspec([]string{c.Path})
		}
		parent := ""
		if len(c.Parents) > 0 {
			parent = c.Parents[0]
		}
		patches, err := d.compute(eng, parent, c.SHA, ps)
		if err != nil {
			return "", "", err
		}
		return text, d.render(patches), nil
	}

	// --working: the open working change first, when it holds anything. It
	// has no git counterpart (it is the working tree), so it is opt-in and
	// HEAD stays the sealed tip either way.
	if g.on("working") {
		wb := lc.branch
		if len(revs) == 1 && r.IsExpressed(revs[0]) {
			wb = revs[0]
		} else if len(revs) > 0 {
			return errors.New("log: --working shows a line's open change; give no revision, or one expressed line")
		}
		if graph || o.Reverse {
			return errors.New("log: --working cannot be combined with --graph or --reverse")
		}
		if err := r.SyncWorking(); err != nil {
			return fmt.Errorf("snapshotting working copy: %w", err)
		}
		base, head, err := workingPair(r, wb)
		if err != nil {
			return err
		}
		if base != head {
			changes, err := eng.TreeChanges(base, head, basePS)
			if err != nil {
				return mapErr(err)
			}
			if len(changes) > 0 {
				c, err := eng.CommitMeta(head)
				if err != nil {
					return mapErr(err)
				}
				if pp.decor == nil {
					pp.decor = map[string][]decoration{}
				}
				pp.decor[head] = []decoration{{"working change", cBold + cMagenta}}
				was := pp.decorate
				pp.decorate = true
				text := pp.entry(c)
				pp.decorate = was
				patches, err := d.compute(eng, base, head, basePS)
				if err != nil {
					return mapErr(err)
				}
				lw.write(text, d.render(patches), nil)
			}
		}
	}

	if unborn {
		return nil
	}
	if !graph {
		err = eng.WalkCommits(o, func(c *change.CommitMeta) error {
			text, diff, err := render(c)
			if err != nil {
				return err
			}
			lw.write(text, diff, nil)
			return nil
		})
		return mapErr(err)
	}

	var list []*change.CommitMeta
	if err := eng.WalkCommits(o, func(c *change.CommitMeta) error { list = append(list, c); return nil }); err != nil {
		return mapErr(err)
	}
	// Draw edges only to commits that will be listed (filters may hide some).
	listed := make(map[string]bool, len(list))
	for _, c := range list {
		listed[c.SHA] = true
	}
	gs := &graphState{}
	for i, c := range list {
		if limit > 0 && i >= skip+limit {
			break
		}
		var parents []string
		for _, p := range c.GraphParents {
			if listed[p] {
				parents = append(parents, p)
			}
		}
		prefixes, cont, width := gs.step(c.SHA, parents)
		if i < skip {
			continue
		}
		text, diff, err := render(c)
		if err != nil {
			return mapErr(err)
		}
		lw.write(text, diff, &graphPrefix{first: prefixes, cont: cont, width: width, color: color})
	}
	return nil
}

// logWriter lays entries out the way git does: terminator formats (oneline,
// tformat) end every entry with a newline; the others separate entries with a
// blank line; a diff follows the message after a blank line (none for
// oneline).
type logWriter struct {
	out   *bufio.Writer
	f     logFormat
	d     diffOut
	shown bool
	// prevBars is the graph's columns after the previous entry, drawn on the
	// blank line that separates entries.
	prevBars string
}

type graphPrefix struct {
	first []string // the commit's own line, then the edge lines after it
	cont  string   // the columns after the commit, for every further line
	width int      // every prefix is padded to this
	color bool
}

func (lw *logWriter) write(text, diff string, gp *graphPrefix) {
	var sb strings.Builder
	separated := !lw.f.terminator
	bare := lw.f.kind == "format" && separated // format: — no newline after the last entry
	sb.WriteString(text)
	if diff != "" && lw.f.kind != "oneline" {
		// As git (log_tree_diff_flush): the message's own newline (a format:
		// message has none), then "---" when stat and patch are both shown,
		// then ONE newline — so format: gets no blank line before the diff
		// and "X---" on the stat/patch separator.
		if !bare {
			sb.WriteString("\n")
		}
		if lw.d.stat && lw.d.patch {
			sb.WriteString("---")
		}
		sb.WriteString("\n")
	} else if !bare || diff != "" {
		sb.WriteString("\n")
	}
	sb.WriteString(diff)
	entry := sb.String()
	if gp == nil {
		if lw.shown && separated {
			_, _ = lw.out.WriteString("\n")
		}
		_, _ = lw.out.WriteString(entry)
		lw.shown = true
		return
	}
	// Under --graph every line carries the columns, padded to the graph's
	// width: the commit's own line, then the edge lines to its parents
	// (which double as the prefix of the entry's next lines, as in git), then
	// plain bars. The blank line between entries shows the columns as the
	// previous entry left them, at this entry's width.
	pad := func(s string) string {
		if len(s) < gp.width {
			s += strings.Repeat(" ", gp.width-len(s))
		}
		return s
	}
	if lw.shown && separated {
		_, _ = lw.out.WriteString(pad(lw.prevBars) + "\n")
	}
	// Edge lines ride along the commit's own text only; a diff starts below
	// them.
	head := strings.TrimSuffix(entry, diff)
	lines := strings.Split(strings.TrimSuffix(head, "\n"), "\n")
	for i := 0; i < max(len(lines), len(gp.first)); i++ {
		prefix := gp.cont
		if i < len(gp.first) {
			prefix = gp.first[i]
		}
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		_, _ = lw.out.WriteString(pad(prefix) + line + "\n")
	}
	if diff != "" {
		for _, line := range strings.Split(strings.TrimSuffix(diff, "\n"), "\n") {
			_, _ = lw.out.WriteString(pad(gp.cont) + line + "\n")
		}
	}
	lw.prevBars = gp.cont
	lw.shown = true
}

// graphState draws the --graph columns: one per line of development awaiting
// its next commit.
type graphState struct {
	cols []string
}

func bars(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("| ", n), " ")
}

// step places commit sha and returns the prefix of its own line followed by
// the edge lines that lead to its parents, and the prefix for further lines.
func (gs *graphState) step(sha string, parents []string) (lines []string, cont string, width int) {
	idx := indexOf(gs.cols, sha)
	if idx < 0 {
		gs.cols = append(gs.cols, sha)
		idx = len(gs.cols) - 1
	}
	n := len(gs.cols)
	var first strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			first.WriteByte(' ')
		}
		if i == idx {
			first.WriteByte('*')
		} else {
			first.WriteByte('|')
		}
	}
	var edges []string
	after := append([]string(nil), gs.cols...)
	at := idx // where this commit's line continues
	if len(parents) == 0 {
		after = append(after[:idx], after[idx+1:]...)
		if idx < n-1 {
			edges = append(edges, strings.Repeat("| ", idx)+strings.TrimLeft(strings.Repeat(" /", n-1-idx), ""))
		}
		at = -1
	} else if j := indexOf(after, parents[0]); j >= 0 && j != idx {
		// The first parent already has a column: this one joins it.
		keep, drop := min(idx, j), max(idx, j)
		after[keep] = parents[0]
		after = append(after[:drop], after[drop+1:]...)
		edges = append(edges, bars(drop)+"/"+strings.Repeat(" /", n-1-drop))
		at = keep
	} else {
		after[idx] = parents[0]
	}
	for k := 1; k < len(parents) && at >= 0; k++ {
		p := parents[k]
		if j := indexOf(after, p); j >= 0 {
			// Merging a line that is already drawn.
			b := []byte(bars(len(after)))
			switch {
			case j > at && 2*at+1 < len(b):
				b[2*at+1] = '\\'
			case j < at && 2*at-1 >= 0:
				b[2*at-1] = '/'
			}
			edges = append(edges, string(b))
			continue
		}
		pos := at + k
		if pos > len(after) {
			pos = len(after)
		}
		right := len(after) - pos
		after = append(after[:pos], append([]string{p}, after[pos:]...)...)
		edges = append(edges, bars(pos)+`\`+strings.Repeat(` \`, right))
	}
	gs.cols = after
	// git pads every line of an entry to the graph's width: two cells per
	// column, at the wider of the layouts before and after the commit.
	width = 2 * max(n, len(after))
	return append([]string{first.String()}, edges...), bars(len(after)), width
}

func indexOf(xs []string, x string) int {
	for i, y := range xs {
		if y == x {
			return i
		}
	}
	return -1
}

// logClassic is cairn's original log: the line's first-parent history,
// newest first, with the open working change marked.
func logClassic(r *worktree.Repo, g *gitOpts) error {
	n, err := g.integer("n", 20)
	if err != nil {
		return err
	}
	branch := ""
	if len(g.pos) > 0 {
		branch = g.pos[0]
	} else if branch, err = r.DefaultBranch(); err != nil {
		return mapErr(err)
	}
	commits, err := r.Log(branch, n)
	if err != nil {
		return mapErr(err)
	}
	for _, c := range commits {
		short := c.SHA
		if len(short) > 8 {
			short = short[:8]
		}
		subject := c.Subject
		if c.Working {
			// The head of an open (unsealed) change is the live working commit. Its
			// description is the "(working)" placeholder until the change is named;
			// surface the marker once (avoid a doubled "(working) (working)").
			if subject == "" || subject == "(working)" {
				subject = "(working)"
			} else {
				subject = "(working) " + subject
			}
		}
		fmt.Printf("%s  %s  %s  %s\n", short, c.When.Format("2006-01-02"), c.AuthorName, subject)
	}
	return nil
}

const showSpec = "oneline format|pretty? abbrev-commit abbrev= date= decorate? q|quiet m first-parent classic " + diffSpec

// cmdShow prints objects: a commit's message and diff, a blob's content
// (<rev>:<path>), a tree's listing, or a tag.
// Usage: cairn show [options] [<object>...] [-- <path>...]
func cmdShow(args []string) error {
	g := newGitOpts("show", showSpec)
	if err := g.parse(args); err != nil {
		return err
	}
	r, err := openRepo(g.repoDir(), defaultAuthor())
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	eng := r.Engine()

	// cairn's original view: `cairn show --classic <commit>`.
	if g.on("classic") {
		if len(g.pos) != 1 {
			return errors.New("usage: cairn show --classic <commit>")
		}
		return showClassic(r, g.pos[0])
	}

	lc, err := currentLine(r, g.repoDir())
	if err != nil {
		return err
	}
	paths, err := lc.treePaths(g.paths)
	if err != nil {
		return err
	}
	objs := g.pos
	if len(objs) == 0 {
		objs = []string{"HEAD"}
	}
	f, err := parseLogFormat(g)
	if err != nil {
		return err
	}
	d, err := parseDiffOut(g, true)
	if err != nil {
		return err
	}
	if g.has("s") {
		d = d.none()
	}
	color := useColor(g, r)
	d.color = color
	if !validDateMode(g.str("date")) {
		return fmt.Errorf("show: unknown date format %q", g.str("date"))
	}
	pp := &prettyPrinter{f: f, color: color, dateMode: g.str("date"), now: time.Now()}
	if pp.abbrev, err = g.integer("abbrev", 7); err != nil {
		return err
	}
	pp.decorate = g.on("decorate") || (!g.has("decorate") && stdoutIsTTY())
	if pp.decorate || strings.Contains(f.tmpl, "%d") || strings.Contains(f.tmpl, "%D") {
		if pp.decor, err = loadDecorations(eng); err != nil {
			return mapErr(err)
		}
	}
	ps := change.NewPathspec(paths)
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()
	lw := &logWriter{out: out, f: f, d: d}
	for i, name := range objs {
		// <rev>:<path> is relative to where the operator stands, as in git.
		if rev, p, ok := strings.Cut(name, ":"); ok && !strings.HasPrefix(name, ":/") {
			if strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") {
				if p, err = lc.treePath(p); err != nil {
					return err
				}
			}
			name = rev + ":" + p
		}
		sha, typ, err := eng.ResolveObject(name)
		if err != nil {
			return mapErr(err)
		}
		if typ != "commit" && i > 0 {
			_, _ = out.WriteString("\n")
		}
		switch typ {
		case "blob":
			data, err := eng.ReadBlob(sha)
			if err != nil {
				return mapErr(err)
			}
			_, _ = out.Write(data)
		case "tree":
			if err := showTree(eng, out, name, sha); err != nil {
				return err
			}
		case "tag":
			raw, err := eng.ObjectBytes(sha)
			if err != nil {
				return mapErr(err)
			}
			showTag(out, raw)
			target, err := eng.ResolveCommit(name)
			if err != nil {
				return mapErr(err)
			}
			_, _ = out.WriteString("\n")
			if err := showCommit(eng, lw, pp, d, ps, target); err != nil {
				return err
			}
		default:
			if err := showCommit(eng, lw, pp, d, ps, sha); err != nil {
				return err
			}
		}
	}
	return nil
}

func showCommit(eng *change.Engine, lw *logWriter, pp *prettyPrinter, d diffOut, ps *change.Pathspec, sha string) error {
	c, err := eng.CommitMeta(sha)
	if err != nil {
		return mapErr(err)
	}
	// A merge is shown against its first parent (git shows a combined diff).
	parent := ""
	if len(c.Parents) > 0 {
		parent = c.Parents[0]
	}
	if !ps.All() {
		// As git: a commit that does not touch the pathspec is not shown.
		changes, err := eng.TreeChanges(parent, sha, ps)
		if err != nil {
			return mapErr(err)
		}
		if len(changes) == 0 {
			return nil
		}
	}
	patches, err := d.compute(eng, parent, sha, ps)
	if err != nil {
		return mapErr(err)
	}
	lw.write(pp.entry(c), d.render(patches), nil)
	return nil
}

func showTree(eng *change.Engine, out io.Writer, name, sha string) error {
	ents, err := eng.ListTree(sha, nil, false, false)
	if err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(out, "tree %s\n\n", name)
	for _, e := range ents {
		if e.Type == "tree" {
			fmt.Fprintln(out, e.Path+"/")
		} else {
			fmt.Fprintln(out, e.Path)
		}
	}
	return nil
}

// showTag prints an annotated tag object's header and message.
func showTag(out io.Writer, raw []byte) {
	head, msg, _ := strings.Cut(string(raw), "\n\n")
	for _, ln := range strings.Split(head, "\n") {
		k, v, _ := strings.Cut(ln, " ")
		switch k {
		case "tag":
			fmt.Fprintf(out, "tag %s\n", v)
		case "tagger":
			// "Name <email> 1700000000 +0000"
			if i := strings.LastIndex(v, "> "); i >= 0 {
				fmt.Fprintf(out, "Tagger: %s\n", v[:i+1])
				var ts int64
				var tz string
				if _, err := fmt.Sscanf(v[i+2:], "%d %s", &ts, &tz); err == nil {
					when := time.Unix(ts, 0).UTC()
					if z, err := time.Parse("-0700", tz); err == nil {
						when = when.In(z.Location())
					}
					fmt.Fprintf(out, "Date:   %s\n", formatDate(when, "", time.Now()))
				}
			}
		}
	}
	fmt.Fprintf(out, "\n%s", msg)
	if !strings.HasSuffix(msg, "\n") {
		fmt.Fprintln(out)
	}
}

// showClassic is cairn's original show: a plain header and the commit's diff
// against its first parent.
func showClassic(r *worktree.Repo, rev string) error {
	ci, diffs, err := r.Show(rev)
	if err != nil {
		return mapErr(err)
	}
	fmt.Printf("commit %s\nAuthor: %s <%s>\nDate:   %s\n\n", ci.SHA, ci.AuthorName, ci.AuthorEmail, ci.When.Format(time.RFC3339))
	for _, line := range strings.Split(ci.Message, "\n") {
		fmt.Printf("    %s\n", line)
	}
	fmt.Println()
	for _, d := range diffs {
		if d.Binary {
			fmt.Printf("Binary files differ: %s\n", d.Path)
			continue
		}
		if d.Unified != "" {
			fmt.Print(d.Unified)
		} else {
			fmt.Printf("%s: %s\n", d.Status, d.Path)
		}
	}
	return nil
}

const shortlogSpec = "s|summary n|numbered e|email c|committer all first-parent merges no-merges " +
	"author= grep= i|regexp-ignore-case E|extended-regexp F|fixed-strings since|after= until|before="

// cmdShortlog summarises history by author.
// Usage: cairn shortlog [-s] [-n] [-e] [<revision range>] [-- <path>...]
func cmdShortlog(args []string) error {
	g := newGitOpts("shortlog", shortlogSpec)
	if err := g.parse(args); err != nil {
		return err
	}
	r, err := openRepo(g.repoDir(), defaultAuthor())
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	eng := r.Engine()
	lc, err := currentLine(r, g.repoDir())
	if err != nil {
		return err
	}
	revs, paths, err := splitRevsPaths(eng, lc, g)
	if err != nil {
		return mapErr(err)
	}
	if paths, err = lc.treePaths(paths); err != nil {
		return err
	}
	o := change.WalkOptions{Paths: paths}
	if err := walkFilters(g, &o); err != nil {
		return err
	}
	// As git: with no revision given and stdin not a terminal, shortlog
	// summarises a `git log` / `cairn log` read from stdin, not HEAD.
	fromStdin := len(revs) == 0 && !g.on("all") && !isInteractive()
	if !fromStdin {
		if err := walkRange(eng, g, revs, &o); err != nil {
			return err
		}
	}
	type group struct {
		name     string
		subjects []string
	}
	groups := map[string]*group{}
	add := func(name, email, subject string) {
		key := name
		if g.on("e") {
			key = fmt.Sprintf("%s <%s>", name, email)
		}
		gr := groups[key]
		if gr == nil {
			gr = &group{name: key}
			groups[key] = gr
		}
		gr.subjects = append(gr.subjects, subject)
	}
	if fromStdin {
		err = shortlogStdin(os.Stdin, g.on("c"), add)
	} else {
		err = eng.WalkCommits(o, func(c *change.CommitMeta) error {
			who := c.Author
			if g.on("c") {
				who = c.Committer
			}
			add(who.Name, who.Email, c.Subject())
			return nil
		})
	}
	if err != nil {
		return mapErr(err)
	}
	list := make([]*group, 0, len(groups))
	for _, gr := range groups {
		list = append(list, gr)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
	if g.on("n") {
		sort.SliceStable(list, func(i, j int) bool { return len(list[i].subjects) > len(list[j].subjects) })
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for _, gr := range list {
		if g.on("s") {
			fmt.Fprintf(out, "%6d\t%s\n", len(gr.subjects), gr.name)
			continue
		}
		fmt.Fprintf(out, "%s (%d):\n", gr.name, len(gr.subjects))
		// Oldest first, as git lists them.
		for i := len(gr.subjects) - 1; i >= 0; i-- {
			fmt.Fprintf(out, "      %s\n", gr.subjects[i])
		}
		fmt.Fprintln(out)
	}
	return nil
}

// shortlogStdin reads log output (medium or fuller): each "Author: " line
// (or "Commit: " with -c) names a commit, whose subject is the first
// non-blank line after the header block — as git shortlog does. A leading
// "[PATCH ...] " tag is dropped.
func shortlogStdin(in io.Reader, committer bool, add func(name, email, subject string)) error {
	prefix := "Author: "
	if committer {
		prefix = "Commit: "
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<24)
	for sc.Scan() {
		ident, ok := strings.CutPrefix(sc.Text(), prefix)
		if !ok {
			continue
		}
		// git keeps the padding of `--format=fuller`'s "Author:     Name".
		name, email := strings.TrimRight(ident, " "), ""
		if i := strings.Index(ident, " <"); i >= 0 {
			name = ident[:i]
			email = strings.TrimSuffix(strings.TrimSpace(ident[i+2:]), ">")
		}
		for sc.Scan() && sc.Text() != "" { // the rest of the header block
		}
		subject := ""
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t != "" {
				subject = t
				break
			}
		}
		if strings.HasPrefix(subject, "[PATCH") {
			if _, rest, ok := strings.Cut(subject, "] "); ok {
				subject = rest
			}
		}
		add(name, email, subject)
	}
	return sc.Err()
}
