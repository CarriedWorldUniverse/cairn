package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// diff (git-shaped), grep, blame and format-patch.

const diffCmdSpec = diffSpec + " author= exit-code q|quiet cached|staged abbrev? no-index classic"

// cmdDiff shows changes.
//
//	cairn diff [options] [-- <path>...]            the working change vs its sealed parent
//	cairn diff [options] <line>                    that line's working change
//	cairn diff [options] <rev> [-- <path>...]      a commit vs the working tree
//	cairn diff [options] <a> <b> | <a>..<b>        commit vs commit
//	cairn diff [options] <a>...<b>                 what b did since it forked from a
//
// The output is git's: a real git-format patch (`git apply`-able), git's stat
// block, git's exit codes. --classic prints cairn's original format instead
// (cmdDiffClassic).
func cmdDiff(args []string) error {
	g := newGitOpts("diff", diffCmdSpec)
	if err := g.parse(args); err != nil {
		return err
	}
	if g.on("classic") {
		return cmdDiffClassic(withoutToken(withoutToken(args, "--classic"), "-classic"))
	}
	r, err := openRepo(g.repoDir(), firstNonEmptyStr(g.str("author"), defaultAuthor()))
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	eng := r.Engine()
	lc, err := currentLine(r, g.repoDir())
	if err != nil {
		return err
	}

	// Operands: a lone expressed line keeps its cairn meaning (that line's
	// working change); otherwise revisions, then paths.
	var revs, paths []string
	branch := lc.branch
	if len(g.pos) >= 1 && r.IsExpressed(g.pos[0]) && (len(g.pos) == 1 || g.hasDD) && !strings.Contains(g.pos[0], "..") {
		branch = g.pos[0]
		paths = g.paths
		if len(g.pos) > 1 {
			return errors.New("usage: cairn diff [options] [<line> | <rev> | <a> <b> | <a>..<b> | <a>...<b>] [-- <path>...]")
		}
		if ent, ok := r.Ls()[branch]; ok && branch != lc.branch {
			lc = lineContext{branch: branch, dir: filepath.Join(r.Root(), ent.Path)}
		}
	} else if revs, paths, err = splitRevsPaths(eng, lc, g); err != nil {
		return mapErr(err)
	}
	if paths, err = lc.treePaths(paths); err != nil {
		return err
	}
	var a, b string
	working := func() error {
		if err := r.SyncWorking(); err != nil {
			return fmt.Errorf("snapshotting working copy: %w", err)
		}
		return nil
	}
	switch len(revs) {
	case 0:
		if err := working(); err != nil {
			return err
		}
		if a, b, err = workingPair(r, branch); err != nil {
			return err
		}
	case 1:
		switch l, rgt, ok := strings.Cut(revs[0], "..."); {
		case ok:
			if b, err = eng.ResolveCommit(firstNonEmptyStr(rgt, "HEAD")); err != nil {
				return mapErr(err)
			}
			if a, err = eng.MergeBase(firstNonEmptyStr(l, "HEAD"), b); err != nil {
				return mapErr(err)
			}
		case strings.Contains(revs[0], ".."):
			l, rgt, _ = strings.Cut(revs[0], "..")
			if a, err = eng.ResolveCommit(firstNonEmptyStr(l, "HEAD")); err != nil {
				return mapErr(err)
			}
			if b, err = eng.ResolveCommit(firstNonEmptyStr(rgt, "HEAD")); err != nil {
				return mapErr(err)
			}
		default:
			if a, err = eng.ResolveCommit(revs[0]); err != nil {
				return mapErr(err)
			}
			if err := working(); err != nil {
				return err
			}
			if _, b, err = workingPair(r, branch); err != nil {
				return err
			}
		}
	case 2:
		if a, err = eng.ResolveCommit(revs[0]); err != nil {
			return mapErr(err)
		}
		if b, err = eng.ResolveCommit(revs[1]); err != nil {
			return mapErr(err)
		}
	default:
		return errors.New("usage: cairn diff [options] [<line> | <rev> | <a> <b> | <a>..<b> | <a>...<b>] [-- <path>...]")
	}

	d, err := parseDiffOut(g, true)
	if err != nil {
		return err
	}
	if g.has("s") {
		d = d.none()
	}
	d.color = useColor(g, r)
	quiet := g.on("q")
	var patches []change.FilePatch
	if a != b {
		probe := d
		if !probe.any() {
			probe.nameOnly = true // -s/--quiet still need to know IF anything differs
		}
		if patches, err = probe.compute(eng, a, b, change.NewPathspec(paths)); err != nil {
			return mapErr(err)
		}
	}
	// A pathspec that matched nothing and names nothing on disk or in HEAD is
	// a typo, not "no changes": say so rather than print an empty diff.
	if len(patches) == 0 && len(revs) == 0 {
		for i, p := range paths {
			if !lc.treePathExists(eng, p) {
				typed := p
				if i < len(g.paths) {
					typed = g.paths[i]
				}
				return fmt.Errorf("diff: pathspec %q did not match any files in the working copy", typed)
			}
		}
	}
	if !quiet && d.any() {
		out := bufio.NewWriterSize(os.Stdout, 1<<16)
		_, _ = out.WriteString(d.render(patches))
		if err := out.Flush(); err != nil {
			return err
		}
	}
	if (quiet || g.on("exit-code")) && len(patches) > 0 {
		return errExit1
	}
	return nil
}

const grepSpec = "n|line-number i|ignore-case l|files-with-matches|name-only L|files-without-match c|count " +
	"w|word-regexp v|invert-match E|extended-regexp F|fixed-strings G|basic-regexp P|perl-regexp e= " +
	"h H q|quiet I a|text color? o|only-matching A|after-context= B|before-context= C|context= # " +
	"cached untracked no-index s|no-messages z|null full-name m|max-count= r|recursive exclude-standard"

// cmdGrep searches file contents.
// Usage: cairn grep [options] [-e] <pattern> [<rev>...] [-- <pathspec>...]
//
// With no <rev> it searches the files of the line you stand in, as they are
// on disk; with one, that revision's tree. Exit status 1 when nothing matches.
func cmdGrep(args []string) error {
	g := newGitOpts("grep", grepSpec)
	if err := g.parse(args); err != nil {
		return err
	}
	patterns := g.strs("e")
	if len(patterns) == 0 {
		if len(g.pos) == 0 {
			return errors.New("usage: cairn grep [options] [-e] <pattern> [<rev>...] [-- <pathspec>...]")
		}
		patterns, g.pos = []string{g.pos[0]}, g.pos[1:]
	}
	parts := make([]string, len(patterns))
	for i, p := range patterns {
		p = goPattern(p, g.on("F"), g.on("E") || g.on("P"))
		if g.on("w") {
			p = `\b(?:` + p + `)\b`
		}
		parts[i] = p
	}
	expr := strings.Join(parts, "|")
	if g.on("i") {
		expr = "(?i)" + expr
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return fmt.Errorf("grep: bad pattern: %w", err)
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
	if len(paths) == 0 {
		paths = lc.prefixSpec()
	}
	ps := change.NewPathspec(paths)

	type source struct {
		label  string // "<rev>:" prefix, "" for the working tree
		commit string
		disk   string // read files from this folder instead of the object store
	}
	var sources []source
	if len(revs) == 0 {
		if err := r.SyncWorking(); err != nil {
			return fmt.Errorf("snapshotting working copy: %w", err)
		}
		_, head, err := workingPair(r, lc.branch)
		if err != nil {
			return err
		}
		sources = append(sources, source{commit: head, disk: lc.dir})
	}
	for _, rev := range revs {
		sha, err := eng.ResolveCommit(rev)
		if err != nil {
			return mapErr(err)
		}
		sources = append(sources, source{label: rev + ":", commit: sha})
	}

	ctxN := 0
	if g.numSet {
		ctxN = g.num
	}
	if ctxN, err = g.integer("C", ctxN); err != nil {
		return err
	}
	gp := &grepPrinter{
		re: re, invert: g.on("v"), lineNo: g.on("n"), count: g.on("c"), names: g.on("l"), without: g.on("L"),
		only: g.on("o"), text: g.on("a"), skipBinary: g.on("I"), color: useColor(g, r), null: g.on("z"),
	}
	if gp.after, err = g.integer("A", ctxN); err != nil {
		return err
	}
	if gp.before, err = g.integer("B", ctxN); err != nil {
		return err
	}
	if gp.max, err = g.integer("m", 0); err != nil {
		return err
	}
	quiet := g.on("q")

	type job struct {
		name string
		sha  string
		disk string
	}
	var jobs []job
	for _, src := range sources {
		if src.commit == "" {
			continue
		}
		files, err := eng.CommitFiles(src.commit, ps)
		if err != nil {
			return mapErr(err)
		}
		for _, f := range files {
			if f.Type != "blob" || (src.disk != "" && f.Mode == "120000") {
				continue
			}
			name := f.Path
			if !g.on("full-name") {
				name = lc.display(name)
			}
			j := job{name: src.label + name, sha: f.SHA}
			if src.disk != "" {
				j.disk = filepath.Join(src.disk, filepath.FromSlash(f.Path))
			}
			jobs = append(jobs, j)
		}
	}
	showName := !g.on("h")

	// Files are searched in parallel and printed in order.
	results := make([]string, len(jobs))
	hits := make([]bool, len(jobs))
	var (
		wg      sync.WaitGroup
		next    int
		mu      sync.Mutex
		readErr error
	)
	for w := 0; w < max(1, runtime.GOMAXPROCS(0)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				i := next
				next++
				mu.Unlock()
				if i >= len(jobs) {
					return
				}
				j := jobs[i]
				var data []byte
				var err error
				if j.disk != "" {
					data, err = os.ReadFile(j.disk)
				}
				if j.disk == "" || err != nil {
					data, err = eng.ReadBlob(j.sha)
				}
				if err != nil {
					mu.Lock()
					readErr = err
					mu.Unlock()
					return
				}
				results[i], hits[i] = gp.file(j.name, data, showName)
			}
		}()
	}
	wg.Wait()
	if readErr != nil {
		return mapErr(readErr)
	}
	any := false
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	for i := range jobs {
		if hits[i] {
			any = true
		}
		if !quiet {
			_, _ = out.WriteString(results[i])
		}
	}
	if err := out.Flush(); err != nil {
		return err
	}
	if !any {
		return errExit1
	}
	return nil
}

// grepPrinter renders one file's matches.
type grepPrinter struct {
	re                            *regexp.Regexp
	invert, lineNo, count         bool
	names, without, only          bool
	text, skipBinary, color, null bool
	before, after, max            int
}

// file searches data and returns what to print for it, and whether the file
// counts as a hit (for the exit status).
func (gp *grepPrinter) file(name string, data []byte, showName bool) (string, bool) {
	nameSep, nameEnd := ":", "\n"
	if gp.null {
		nameSep, nameEnd = "\x00", "\x00" // -z ends -l/-L names with NUL too
	}
	label := func(sep string) string {
		if !showName {
			return ""
		}
		if sep == ":" {
			sep = nameSep
		}
		return paint(gp.color, cMagenta, name) + paint(gp.color, cCyan, sep)
	}
	binary := !gp.text && bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0
	if binary {
		if gp.skipBinary {
			return "", false
		}
		hit := gp.re.Match(data) != gp.invert
		switch {
		case gp.without:
			if !hit {
				return name + nameEnd, true
			}
			return "", false
		case !hit:
			return "", false
		case gp.names:
			return name + nameEnd, true
		case gp.count:
			return fmt.Sprintf("%s%d\n", label(":"), 1), true
		}
		return fmt.Sprintf("Binary file %s matches\n", name), true
	}
	var sb strings.Builder
	matched := 0
	lastPrinted := -1 // last line index written (for context bookkeeping)
	afterLeft := 0
	var lines [][]byte
	if gp.before > 0 || gp.after > 0 {
		lines = bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	}
	writeLine := func(idx int, line []byte, sep string) {
		sb.WriteString(label(sep))
		if gp.lineNo {
			lsep := sep
			if gp.null && showName {
				lsep = "\x00" // git: file NUL number NUL line
			}
			sb.WriteString(paint(gp.color, cGreen, strconv.Itoa(idx+1)) + paint(gp.color, cCyan, lsep))
		}
		if gp.color && sep == ":" && !gp.invert {
			last := 0
			for _, m := range gp.re.FindAllIndex(line, -1) {
				if m[0] == m[1] {
					continue
				}
				sb.Write(line[last:m[0]])
				sb.WriteString(paint(true, cBoldRed, string(line[m[0]:m[1]])))
				last = m[1]
			}
			sb.Write(line[last:])
		} else {
			sb.Write(line)
		}
		sb.WriteByte('\n')
	}
	idx := 0
	for rest := data; len(rest) > 0; idx++ {
		var line []byte
		if nl := bytes.IndexByte(rest, '\n'); nl >= 0 {
			line, rest = rest[:nl], rest[nl+1:]
		} else {
			line, rest = rest, nil
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		if gp.re.Match(line) == gp.invert {
			if afterLeft > 0 && !gp.count && !gp.names && !gp.without {
				writeLine(idx, line, "-")
				lastPrinted = idx
				afterLeft--
			}
			continue
		}
		matched++
		if gp.names || gp.without {
			break
		}
		if gp.count {
			if gp.max > 0 && matched >= gp.max {
				break
			}
			continue
		}
		if gp.before > 0 || gp.after > 0 {
			start := max(idx-gp.before, lastPrinted+1)
			if lastPrinted >= 0 && start > lastPrinted+1 {
				sb.WriteString(paint(gp.color, cCyan, "--") + "\n")
			}
			for k := start; k < idx; k++ {
				writeLine(k, bytes.TrimSuffix(lines[k], []byte("\r")), "-")
			}
		}
		if gp.only && !gp.invert {
			for _, m := range gp.re.FindAll(line, -1) {
				if len(m) == 0 {
					continue
				}
				sb.WriteString(label(":"))
				if gp.lineNo {
					sb.WriteString(paint(gp.color, cGreen, strconv.Itoa(idx+1)) + paint(gp.color, cCyan, ":"))
				}
				sb.WriteString(paint(gp.color, cBoldRed, string(m)) + "\n")
			}
		} else {
			writeLine(idx, line, ":")
		}
		lastPrinted = idx
		afterLeft = gp.after
		if gp.max > 0 && matched >= gp.max {
			break
		}
	}
	switch {
	case gp.without:
		if matched == 0 {
			return name + nameEnd, true
		}
		return "", false
	case gp.names:
		if matched > 0 {
			return paint(gp.color, cMagenta, name) + nameEnd, true
		}
		return "", false
	case gp.count:
		if matched > 0 {
			return fmt.Sprintf("%s%d\n", label(":"), matched), true
		}
		return "", false
	}
	return sb.String(), matched > 0
}

// cmdBlame prints per-line provenance for a file, mapping each line back to
// the commit that last changed it.
//
//	cairn blame [-L <a>,<b>]... <path> [line]      the line's tip, working change included
//	cairn blame [-L <a>,<b>]... [<rev>] -- <path>  as of a revision
//	cairn blame [-L <a>,<b>]... <rev> <path>
func cmdBlame(args []string) error {
	g := newGitOpts("blame", "author= L= s l e|show-email w C? M? classic porcelain line-porcelain abbrev= n|show-number t f|show-name root incremental")
	if err := g.parse(args); err != nil {
		return err
	}
	const usageMsg = "usage: cairn blame [-L <a>,<b>] <path> [line]  |  cairn blame [-L <a>,<b>] [<rev>] -- <path>"
	r, err := openRepoSynced(g.repoDir(), firstNonEmptyStr(g.str("author"), defaultAuthor()))
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	eng := r.Engine()
	lc, err := currentLine(r, g.repoDir())
	if err != nil {
		return err
	}
	var path, branch, rev string
	switch {
	case g.hasDD && len(g.paths) == 1 && len(g.pos) <= 1:
		path = g.paths[0]
		if len(g.pos) == 1 {
			rev = g.pos[0]
		}
	case !g.hasDD && len(g.pos) == 1:
		path = g.pos[0]
	case !g.hasDD && len(g.pos) == 2:
		// `<path> <line>` (cairn) or `<rev> <path>` (git).
		if _, lerr := eng.LineByName(g.pos[1]); lerr == nil && !lc.pathExists(eng, g.pos[1]) {
			path, branch = g.pos[0], g.pos[1]
		} else {
			rev, path = g.pos[0], g.pos[1]
		}
	default:
		return errors.New(usageMsg)
	}
	var lines []change.BlameLine
	var blamed string
	bopt := change.BlameOptions{IgnoreSpace: g.on("w")}
	if rev != "" {
		sha, err := eng.ResolveCommit(rev)
		if err != nil {
			return mapErr(err)
		}
		tp, err := lc.treePath(path)
		if err != nil {
			return err
		}
		blamed = tp
		if lines, err = eng.BlameWith(sha, tp, bopt); err != nil {
			return mapErr(err)
		}
	} else {
		if branch == "" {
			branch = lc.branch
			if path, err = lc.treePath(path); err != nil {
				return err
			}
		}
		blamed = path
		if lines, err = r.BlameWith(branch, path, bopt); err != nil {
			return mapErr(err)
		}
	}
	keep, err := blameRanges(g.strs("L"), len(lines))
	if err != nil {
		return err
	}
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()
	working := map[string]bool{}
	isWorking := func(sha string) bool {
		w, seen := working[sha]
		if !seen {
			w, _ = r.IsWorkingCommit(sha)
			working[sha] = w
		}
		return w
	}
	if g.on("classic") {
		for i, ln := range lines {
			if keep != nil && !keep[i] {
				continue
			}
			id := ln.Commit
			if len(id) > 8 {
				id = id[:8]
			}
			if isWorking(ln.Commit) {
				id = "(working)"
			}
			fmt.Fprintf(out, "%-10s %-14s %s  %s\n", id, ln.Author, ln.When.Format("2006-01-02"), strings.TrimRight(ln.Text, "\n"))
		}
		return nil
	}
	abbrev := 7
	if g.has("abbrev") {
		if abbrev, err = g.integer("abbrev", 7); err != nil || abbrev < 0 {
			return errors.New("blame: bad --abbrev")
		}
	}
	return renderBlame(out, lines, keep, isWorking, blameFmt{
		porcelain: g.on("porcelain") || g.on("line-porcelain"), repeat: g.on("line-porcelain"),
		noAuthor: g.on("s"), email: g.on("e"), long: g.on("l"), number: g.on("n"),
		rawTime: g.on("t"), showName: g.on("f"), abbrev: abbrev, path: blamed,
	})
}

// blameRanges turns -L <a>,<b> arguments (1-based, inclusive; b may be +N or
// omitted) into the set of line indexes to print. nil means every line.
func blameRanges(specs []string, total int) (map[int]bool, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	keep := map[int]bool{}
	for _, spec := range specs {
		as, bs, hasB := strings.Cut(spec, ",")
		a, b := 1, total
		var err error
		if as != "" {
			if a, err = strconv.Atoi(as); err != nil || a < 1 {
				return nil, fmt.Errorf("blame: -L %s: bad start line", spec)
			}
		}
		switch {
		case !hasB || bs == "":
		case strings.HasPrefix(bs, "+"):
			n, err := strconv.Atoi(bs[1:])
			if err != nil || n < 1 {
				return nil, fmt.Errorf("blame: -L %s: bad line count", spec)
			}
			b = a + n - 1
		default:
			if b, err = strconv.Atoi(bs); err != nil || b < a {
				return nil, fmt.Errorf("blame: -L %s: bad end line", spec)
			}
		}
		if a > total {
			return nil, fmt.Errorf("blame: -L %s: file has only %d lines", spec, total)
		}
		for i := a; i <= b && i <= total; i++ {
			keep[i-1] = true
		}
	}
	return keep, nil
}

// cmdFormatPatch writes commits as mbox patches `git am` can apply.
//
//	cairn format-patch [-<N>] [<since> | <range>] [-o <dir>] [--stdout] [--root]
//
// A single <since> means <since>..HEAD; -<N> takes the newest N commits.
// Merges are skipped. Without --stdout each patch is written to
// <dir>/NNNN-subject.patch (default: the current directory) and its name
// printed.
func cmdFormatPatch(args []string) error {
	g := newGitOpts("format-patch", "o|output-directory= stdout N|no-numbered n|numbered k|keep-subject "+
		"start-number= # root no-stat subject-prefix= q|quiet first-parent full-index no-binary no-renames")
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
	paths, err := lc.treePaths(g.paths)
	if err != nil {
		return err
	}
	o := change.WalkOptions{NoMerges: true, Reverse: true, Paths: paths, FirstParent: g.on("first-parent")}
	revs := g.pos
	switch {
	case len(revs) == 0 && !g.numSet && !g.on("root"):
		return errors.New("usage: cairn format-patch [-<N>] [<since> | <range>] [-o <dir>] [--stdout]")
	case len(revs) == 0:
		revs = []string{"HEAD"}
	case len(revs) == 1 && !strings.Contains(revs[0], "..") && !strings.HasPrefix(revs[0], "^") && !g.numSet && !g.on("root"):
		revs = []string{revs[0] + "..HEAD"}
	}
	rr, err := eng.ParseRevisions(revs)
	if err != nil {
		return mapErr(err)
	}
	o.Include, o.Exclude = rr.Include, rr.Exclude
	if g.numSet {
		if g.num == 0 {
			return nil
		}
		o.Limit = g.num
	}
	var commits []*change.CommitMeta
	if err := eng.WalkCommits(o, func(c *change.CommitMeta) error { commits = append(commits, c); return nil }); err != nil {
		return mapErr(err)
	}
	start, err := g.integer("start-number", 1)
	if err != nil {
		return err
	}
	prefix := "PATCH"
	if g.has("subject-prefix") {
		prefix = g.str("subject-prefix")
	}
	numbered := (len(commits) > 1 || g.on("n")) && !g.on("N")
	dir := "."
	if g.has("o") {
		dir = g.str("o")
		if !g.on("stdout") {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
	}
	ps := change.NewPathspec(paths)
	po := change.PatchOptions{Context: 3, Binary: !g.on("no-binary"), FullIndex: g.on("full-index")}
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()
	for i, c := range commits {
		parent := ""
		if len(c.Parents) > 0 {
			parent = c.Parents[0]
		}
		patches, err := eng.CommitPatches(parent, c.SHA, ps, po, false)
		if err != nil {
			return mapErr(err)
		}
		tag := ""
		switch {
		case g.on("k"):
		case numbered:
			width := len(strconv.Itoa(start + len(commits) - 1))
			tag = fmt.Sprintf("[%s %0*d/%d] ", prefix, width, start+i, start+len(commits)-1)
			if prefix == "" {
				tag = fmt.Sprintf("[%0*d/%d] ", width, start+i, start+len(commits)-1)
			}
		case prefix != "":
			tag = "[" + prefix + "] "
		}
		mail := mboxPatch(c, tag, patches, !g.on("no-stat"))
		if g.on("stdout") {
			if i > 0 {
				_, _ = out.WriteString("\n") // git separates messages by a blank line
			}
			_, _ = out.WriteString(mail)
			continue
		}
		subj := sanitizeSubject(c.Subject())
		if len(subj) > 52 {
			subj = strings.TrimRight(subj[:52], ".-")
		}
		name := filepath.Join(dir, fmt.Sprintf("%04d-%s.patch", start+i, subj))
		if err := os.WriteFile(name, []byte(mail), 0o644); err != nil {
			return err
		}
		if !g.on("q") {
			fmt.Fprintln(out, name)
		}
	}
	return nil
}

// mboxPatch renders one commit as an mbox message in git format-patch's
// layout.
func mboxPatch(c *change.CommitMeta, subjectTag string, patches []change.FilePatch, stat bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "From %s Mon Sep 17 00:00:00 2001\n", c.SHA)
	fmt.Fprintf(&sb, "From: %s <%s>\n", mailName(c.Author.Name), c.Author.Email)
	fmt.Fprintf(&sb, "Date: %s\n", formatDate(c.Author.When, "rfc", c.Author.When))
	fmt.Fprintf(&sb, "Subject: %s%s\n", subjectTag, mailSubject("Subject: "+subjectTag, c.Subject()))
	body := c.Body()
	if !isASCII(body) || !isASCII(c.Subject()) {
		sb.WriteString("MIME-Version: 1.0\nContent-Type: text/plain; charset=UTF-8\nContent-Transfer-Encoding: 8bit\n")
	}
	sb.WriteString("\n")
	if body != "" {
		sb.WriteString(body + "\n")
	}
	// As git: --no-stat drops the "---" line, the stat block and the
	// summary; the patch follows the message after one blank line.
	if stat {
		sb.WriteString("---\n")
		sb.WriteString(change.FormatStat(patches, 72))
		for _, p := range patches {
			sb.WriteString(p.Summary)
		}
	}
	sb.WriteString("\n")
	for _, p := range patches {
		sb.WriteString(p.Text())
	}
	fmt.Fprintf(&sb, "-- \ncairn %s\n\n", buildVersion)
	return sb.String()
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// mailSubject renders a Subject: header value that follows prefix ("Subject:
// [PATCH] "), as git does: non-ASCII text becomes RFC 2047 encoded words
// folded so no header line passes 76 columns; plain ASCII is folded at spaces
// so no line passes 78.
func mailSubject(prefix, s string) string {
	if !isASCII(s) {
		return rfc2047(len(prefix), s, false)
	}
	var sb strings.Builder
	col := len(prefix)
	for i, w := range strings.Split(s, " ") {
		switch {
		case i == 0:
		case col+1+len(w) > 78:
			sb.WriteString("\n ")
			col = 1
		default:
			sb.WriteByte(' ')
			col++
		}
		sb.WriteString(w)
		col += len(w)
	}
	return sb.String()
}

// rfc2047 is git's add_rfc2047: a run of "=?UTF-8?q?…?=" encoded words, a new
// one (after a folded line) whenever the next character would pass column 76.
// startCol is where the value starts on its header line. phrase selects the
// stricter character set of an address's display name.
func rfc2047(startCol int, s string, phrase bool) string {
	const enc = "UTF-8"
	var sb strings.Builder
	sb.WriteString("=?" + enc + "?q?")
	col := startCol + len(enc) + 5
	for _, r := range s {
		chr := string(r)
		special := r >= 0x80 || r <= ' ' || r == '=' || r == '?' || r == '_'
		if phrase && !special {
			special = !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || strings.ContainsRune("!*+-/", r))
		}
		n := len(chr)
		if special {
			n *= 3
		}
		if col+n+2 > 76 {
			sb.WriteString("?=\n =?" + enc + "?q?")
			col = len(enc) + 5 + 1
		}
		if special {
			for i := 0; i < len(chr); i++ {
				fmt.Fprintf(&sb, "=%02X", chr[i])
			}
		} else {
			sb.WriteString(chr)
		}
		col += n
	}
	sb.WriteString("?=")
	return sb.String()
}

// mailName renders a display name for a From: header: encoded when not
// ASCII, quoted when it holds characters RFC 822 reserves.
func mailName(s string) string {
	if !isASCII(s) {
		return rfc2047(len("From: "), s, true)
	}
	if strings.ContainsAny(s, `()<>[]:;@\,."`) {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return s
}
