package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

// The read-only plumbing scripts lean on: rev-parse, rev-list, cat-file,
// ls-tree, ls-files, describe. Each answers in git's output format, because
// the point of these commands is that an existing script keeps working.

// cmdRevParse resolves revisions and answers questions about the repo.
// Usage: cairn rev-parse [--verify] [-q] [--short[=n]] [--abbrev-ref] <rev>...
//
//	cairn rev-parse --show-toplevel | --git-dir | --is-inside-work-tree | --show-prefix
//
// Arguments are processed in order, as git does.
func cmdRevParse(args []string) error {
	repoDir := "."
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--repo" || a == "-repo":
			if i+1 >= len(args) {
				return errors.New("rev-parse: --repo needs a value")
			}
			i++
			repoDir = args[i]
		case strings.HasPrefix(a, "--repo="):
			repoDir = strings.TrimPrefix(a, "--repo=")
		case a == "-h" || a == "--help":
			return flag.ErrHelp
		default:
			rest = append(rest, a)
		}
	}
	r, err := openRepo(repoDir, defaultAuthor())
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	eng := r.Engine()
	lc, err := currentLine(r, repoDir)
	if err != nil {
		return err
	}
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	var (
		verify, quiet, abbrevRef, symbolic bool
		short                              = -1
		shown                              int
	)
	name := func(sha string) string {
		if short >= 0 && len(sha) > short {
			return sha[:short]
		}
		return sha
	}
	fail := func(rev string, err error) error {
		if verify {
			if quiet {
				return errExit1
			}
			return errors.New("rev-parse: needed a single revision")
		}
		if quiet {
			return errExit1
		}
		return fmt.Errorf("rev-parse: %s: unknown revision or path not in the working tree: %w", rev, err)
	}
	for _, a := range rest {
		switch {
		case a == "--verify":
			verify = true
		case a == "-q" || a == "--quiet":
			quiet = true
		case a == "--short":
			short = 7
		case strings.HasPrefix(a, "--short="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--short="))
			if err != nil || n < 4 {
				return fmt.Errorf("rev-parse: bad --short value %q", a)
			}
			short = n
		case a == "--abbrev-ref", strings.HasPrefix(a, "--abbrev-ref="):
			abbrevRef = true
		case a == "--symbolic-full-name":
			symbolic = true
		case a == "--symbolic", a == "--default", a == "--revs-only", a == "--no-revs", a == "--flags", a == "--no-flags", a == "--end-of-options", a == "--":
			// accepted; no effect on what is supported here
		case a == "--show-toplevel":
			if lc.dir == "" {
				return fmt.Errorf("rev-parse: line %q has no folder (not expressed)", lc.branch)
			}
			fmt.Fprintln(out, lc.dir)
		case a == "--git-dir", a == "--absolute-git-dir", a == "--git-common-dir":
			fmt.Fprintln(out, filepath.Join(r.Root(), ".cairn"))
		case a == "--is-inside-work-tree":
			_, inside := r.CWDBranch()
			fmt.Fprintln(out, inside)
		case a == "--is-inside-git-dir":
			abs, _ := filepath.Abs(repoDir)
			rel, rerr := filepath.Rel(filepath.Join(r.Root(), ".cairn"), abs)
			fmt.Fprintln(out, rerr == nil && !strings.HasPrefix(rel, ".."))
		case a == "--is-bare-repository", a == "--is-shallow-repository":
			fmt.Fprintln(out, false)
		case a == "--show-prefix":
			if lc.prefix == "" {
				fmt.Fprintln(out)
			} else {
				fmt.Fprintln(out, lc.prefix+"/")
			}
		case a == "--show-cdup":
			if lc.prefix == "" {
				fmt.Fprintln(out)
			} else {
				fmt.Fprintln(out, strings.Repeat("../", strings.Count(lc.prefix, "/")+1))
			}
		case strings.HasPrefix(a, "--") && len(a) > 2:
			return fmt.Errorf("rev-parse: unknown option %s", a)
		case abbrevRef || symbolic:
			ref, err := symbolicName(eng, a, symbolic)
			if err != nil {
				return fail(a, err)
			}
			fmt.Fprintln(out, ref)
			shown++
		case strings.Contains(a, "...") && !strings.Contains(a, ":"):
			l, rgt, _ := strings.Cut(a, "...")
			rr, err := eng.ParseRevisions([]string{a})
			if err != nil || verify {
				return fail(a, err)
			}
			_, _ = l, rgt
			// git prints the right side first: `B A ^base`.
			for i := len(rr.Include) - 1; i >= 0; i-- {
				fmt.Fprintln(out, name(rr.Include[i]))
			}
			for _, s := range rr.Exclude {
				fmt.Fprintln(out, "^"+name(s))
			}
			shown++
		case strings.Contains(a, "..") && !strings.Contains(a, ":"):
			rr, err := eng.ParseRevisions([]string{a})
			if err != nil || verify {
				return fail(a, err)
			}
			for _, s := range rr.Include {
				fmt.Fprintln(out, name(s))
			}
			for _, s := range rr.Exclude {
				fmt.Fprintln(out, "^"+name(s))
			}
			shown++
		default:
			rev, neg := a, ""
			if strings.HasPrefix(a, "^") && len(a) > 1 {
				rev, neg = a[1:], "^"
			}
			if rv, p, ok := strings.Cut(rev, ":"); ok && (strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")) {
				tp, perr := lc.treePath(p)
				if perr != nil {
					return fail(a, perr)
				}
				rev = rv + ":" + tp
			}
			sha, _, err := eng.ResolveObject(rev)
			if err != nil {
				return fail(a, err)
			}
			if verify && shown > 0 {
				return fail(a, errors.New("more than one revision"))
			}
			fmt.Fprintln(out, neg+name(sha))
			shown++
		}
	}
	if verify && shown == 0 {
		return fail("", errors.New("no revision given"))
	}
	return nil
}

// symbolicName is rev-parse --abbrev-ref / --symbolic-full-name: the name a
// revision goes by, rather than its hash.
func symbolicName(eng *change.Engine, rev string, full bool) (string, error) {
	if _, err := eng.ResolveCommit(rev); err != nil {
		return "", err
	}
	lineName := ""
	switch {
	case rev == "HEAD" || rev == "@":
		l, err := eng.HeadLine()
		if err != nil {
			return "", err
		}
		lineName = l.Name
	case strings.HasSuffix(rev, "@{u}") || strings.HasSuffix(rev, "@{upstream}"):
		base := strings.TrimSuffix(strings.TrimSuffix(rev, "@{u}"), "@{upstream}")
		if base == "" || base == "HEAD" || base == "@" {
			l, err := eng.HeadLine()
			if err != nil {
				return "", err
			}
			base = l.Name
		}
		refs, err := eng.RefNames()
		if err != nil {
			return "", err
		}
		for _, r := range refs {
			if r.Kind == "remote" && strings.HasSuffix(r.Name, "/"+base) {
				if full {
					return "refs/remotes/" + r.Name, nil
				}
				return r.Name, nil
			}
		}
		return "", fmt.Errorf("no upstream for %q", base)
	default:
		refs, err := eng.RefNames()
		if err != nil {
			return "", err
		}
		for _, r := range refs {
			if r.Name != rev {
				continue
			}
			if !full {
				return r.Name, nil
			}
			switch r.Kind {
			case "head":
				return "refs/heads/" + r.Name, nil
			case "tag":
				return "refs/tags/" + r.Name, nil
			}
			return "refs/remotes/" + r.Name, nil
		}
		// Not a name: git prints the revision's hash.
		return eng.ResolveCommit(rev)
	}
	if full {
		return "refs/heads/" + lineName, nil
	}
	return lineName, nil
}

const revListSpec = "n|max-count= # skip= count reverse all first-parent merges no-merges author= committer= " +
	"grep= i|regexp-ignore-case E|extended-regexp F|fixed-strings all-match invert-grep " +
	"since|after= until|before= topo-order date-order parents abbrev-commit abbrev= left-right S= G= oneline format|pretty? date= no-abbrev"

// cmdRevList lists commit hashes in a range.
// Usage: cairn rev-list [--count] [-n N] [--reverse] [--all] [--first-parent] <range>... [-- <path>...]
func cmdRevList(args []string) error {
	g := newGitOpts("rev-list", revListSpec)
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
	if len(revs) == 0 && !g.on("all") {
		return errors.New("usage: cairn rev-list [options] <revision range>... [-- <path>...]")
	}
	if paths, err = lc.treePaths(paths); err != nil {
		return err
	}
	o := change.WalkOptions{Paths: paths, Reverse: g.on("reverse"), Topo: g.on("topo-order"),
		PickaxeS: g.str("S"), PickaxeG: g.str("G")}
	if err := walkFilters(g, &o); err != nil {
		return err
	}
	if err := walkRange(eng, g, revs, &o); err != nil {
		return err
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
	if o.Skip, err = g.integer("skip", 0); err != nil {
		return err
	}
	if limit > 0 {
		o.Limit = limit
	}
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()

	if g.on("left-right") {
		// The common scripted use: ahead/behind counts for a...b.
		if !g.on("count") || len(revs) != 1 || !strings.Contains(revs[0], "...") {
			return errors.New("rev-list: --left-right is supported as `--left-right --count <a>...<b>`")
		}
		l, rgt, _ := strings.Cut(revs[0], "...")
		side := func(inc, exc string) (int, error) {
			rr, err := eng.ParseRevisions([]string{firstNonEmptyStr(inc, "HEAD"), "^" + firstNonEmptyStr(exc, "HEAD")})
			if err != nil {
				return 0, err
			}
			so := o
			so.Include, so.Exclude = rr.Include, rr.Exclude
			return eng.CountCommits(so)
		}
		left, err := side(l, rgt)
		if err != nil {
			return mapErr(err)
		}
		right, err := side(rgt, l)
		if err != nil {
			return mapErr(err)
		}
		fmt.Fprintf(out, "%d\t%d\n", left, right)
		return nil
	}
	if limit == 0 {
		if g.on("count") {
			fmt.Fprintln(out, 0)
		}
		return nil
	}
	if g.on("count") {
		n, err := eng.CountCommits(o)
		if err != nil {
			return mapErr(err)
		}
		fmt.Fprintln(out, n)
		return nil
	}
	abbrev := 0
	if g.on("abbrev-commit") {
		if abbrev, err = g.integer("abbrev", 7); err != nil {
			return err
		}
	}
	cut := func(s string) string {
		if abbrev > 0 && len(s) > abbrev {
			return s[:abbrev]
		}
		return s
	}
	if g.on("oneline") || g.has("format") {
		// --oneline / --pretty[=<fmt>]: the log's formats, but a multi-line
		// or format: entry is introduced by a bare "commit <sha>" line.
		f, err := parseLogFormat(g)
		if err != nil {
			return err
		}
		if !validDateMode(g.str("date")) {
			return fmt.Errorf("rev-list: unknown date format %q", g.str("date"))
		}
		pp := &prettyPrinter{f: f, dateMode: g.str("date"), now: time.Now()}
		if pp.abbrev, err = abbrevLen(g); err != nil {
			return err
		}
		return mapErr(eng.WalkCommits(o, func(c *change.CommitMeta) error {
			text := pp.entry(c)
			switch f.kind {
			case "oneline":
				_, _ = out.WriteString(text + "\n")
			case "format":
				name := c.SHA
				if f.abbrevCommit {
					name = pp.short(c.SHA)
				}
				_, _ = out.WriteString("commit " + name + "\n" + text + "\n")
			default:
				_, _ = out.WriteString(text + "\n\n")
			}
			return nil
		}))
	}
	return mapErr(eng.WalkCommits(o, func(c *change.CommitMeta) error {
		_, _ = out.WriteString(cut(c.SHA))
		if g.on("parents") {
			for _, p := range c.Parents {
				_, _ = out.WriteString(" " + cut(p))
			}
		}
		_ = out.WriteByte('\n')
		return nil
	}))
}

// cmdCatFile inspects one object.
// Usage: cairn cat-file (-t | -s | -p | -e) <object>
//
//	cairn cat-file <type> <object>
//	cairn cat-file --batch-check | --batch   (object names on stdin)
func cmdCatFile(args []string) error {
	g := newGitOpts("cat-file", "t s p e batch? batch-check? follow-symlinks buffer unordered")
	if err := g.parse(args); err != nil {
		return err
	}
	r, err := openRepo(g.repoDir(), defaultAuthor())
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	eng := r.Engine()
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()

	if g.has("batch") || g.has("batch-check") {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			name := strings.TrimSpace(sc.Text())
			if name == "" {
				continue
			}
			sha, _, err := eng.ResolveObject(name)
			if err != nil {
				fmt.Fprintf(out, "%s missing\n", name)
				continue
			}
			typ, size, err := eng.ObjectInfo(sha)
			if err != nil {
				fmt.Fprintf(out, "%s missing\n", name)
				continue
			}
			fmt.Fprintf(out, "%s %s %d\n", sha, typ, size)
			if g.has("batch") {
				data, err := eng.ObjectBytes(sha)
				if err != nil {
					return mapErr(err)
				}
				_, _ = out.Write(data)
				_ = out.WriteByte('\n')
			}
		}
		return sc.Err()
	}

	modes := 0
	for _, m := range []string{"t", "s", "p", "e"} {
		if g.on(m) {
			modes++
		}
	}
	const usageMsg = "usage: cairn cat-file (-t | -s | -p | -e) <object>  |  cairn cat-file <type> <object>  |  cairn cat-file --batch-check"
	wantType := ""
	var name string
	switch {
	case modes == 1 && len(g.pos) == 1:
		name = g.pos[0]
	case modes == 0 && len(g.pos) == 2:
		wantType, name = g.pos[0], g.pos[1]
	default:
		return errors.New(usageMsg)
	}
	sha, _, err := eng.ResolveObject(name)
	if err != nil {
		if g.on("e") {
			return errExit1
		}
		return mapErr(err)
	}
	typ, size, err := eng.ObjectInfo(sha)
	if err != nil {
		if g.on("e") {
			return errExit1
		}
		return mapErr(err)
	}
	switch {
	case g.on("e"):
		return nil
	case g.on("t"):
		fmt.Fprintln(out, typ)
		return nil
	case g.on("s"):
		fmt.Fprintln(out, size)
		return nil
	}
	if wantType != "" && wantType != typ {
		// `cat-file commit <tag>` and `cat-file tree <commit>` peel, as git does.
		peeled, ptyp, perr := eng.ResolveObject(name + "^{" + wantType + "}")
		if perr != nil || ptyp != wantType {
			return fmt.Errorf("cat-file: %s is a %s, not a %s", name, typ, wantType)
		}
		sha, typ = peeled, ptyp
	}
	if typ == "tree" && g.on("p") {
		ents, err := eng.ListTree(sha, nil, false, false)
		if err != nil {
			return mapErr(err)
		}
		for _, e := range ents {
			fmt.Fprintf(out, "%s %s %s\t%s\n", e.Mode, e.Type, e.SHA, e.Path)
		}
		return nil
	}
	data, err := eng.ObjectBytes(sha)
	if err != nil {
		return mapErr(err)
	}
	_, _ = out.Write(data)
	return nil
}

// cmdLsTree lists a tree.
// Usage: cairn ls-tree [-r] [-d] [-t] [-l] [--name-only] [-z] <tree-ish> [<path>...]
func cmdLsTree(args []string) error {
	g := newGitOpts("ls-tree", "r d t l|long name-only name-status z full-name full-tree abbrev?")
	if err := g.parse(args); err != nil {
		return err
	}
	if len(g.pos) < 1 {
		return errors.New("usage: cairn ls-tree [-r] [-d] [-t] [-l] [--name-only] <tree-ish> [<path>...]")
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
	if g.on("full-tree") {
		lc.prefix = ""
	}
	sha, typ, err := eng.ResolveObject(g.pos[0])
	if err != nil {
		return mapErr(err)
	}
	if typ == "commit" || typ == "tag" {
		if sha, typ, err = eng.ResolveObject(g.pos[0] + "^{tree}"); err != nil {
			return mapErr(err)
		}
	}
	if typ != "tree" {
		return fmt.Errorf("ls-tree: %s is a %s, not a tree", g.pos[0], typ)
	}
	var specs []string
	for _, p := range append(g.pos[1:], g.paths...) {
		tp, err := lc.treePath(p)
		if err != nil {
			return err
		}
		// "dir/" (and ".") lists the directory's contents; "dir" lists the
		// entry. Pathspecs here are literal: no glob expansion, as in git.
		if (strings.HasSuffix(p, "/") || p == "." || strings.HasSuffix(p, "/.")) && tp != "" {
			tp += "/"
		}
		specs = append(specs, tp)
	}
	if len(specs) == 0 && lc.prefix != "" {
		specs = []string{lc.prefix + "/"}
	}
	ents, err := eng.ListTree(sha, change.NewLiteralPathspec(specs), g.on("r"), g.on("t") || g.on("d"))
	if err != nil {
		return mapErr(err)
	}
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()
	term := "\n"
	if g.on("z") {
		term = "\x00"
	}
	for _, e := range ents {
		if g.on("d") && e.Type != "tree" {
			continue
		}
		name := e.Path
		if !g.on("full-name") && !g.on("full-tree") {
			name = lc.display(name)
		}
		if !g.on("z") {
			name = change.QuotePath(name)
		}
		switch {
		case g.on("name-only") || g.on("name-status"):
			_, _ = out.WriteString(name + term)
		case g.on("l"):
			size := "-"
			if e.Type == "blob" {
				if _, n, err := eng.ObjectInfo(e.SHA); err == nil {
					size = strconv.FormatInt(n, 10)
				}
			}
			fmt.Fprintf(out, "%s %s %s %7s\t%s%s", e.Mode, e.Type, e.SHA, size, name, term)
		default:
			fmt.Fprintf(out, "%s %s %s\t%s%s", e.Mode, e.Type, e.SHA, name, term)
		}
	}
	return nil
}

// workingPair returns the two commits a line's uncommitted work lies between:
// the commit it is shown against (base) and the working commit (head). They
// are equal when nothing is uncommitted; both "" for a line with no commits.
// The caller must have synced the working copy.
func workingPair(r *worktree.Repo, branch string) (base, head string, err error) {
	eng := r.Engine()
	line, err := eng.LineByName(branch)
	if err != nil {
		return "", "", mapErr(err)
	}
	head = line.TipCommit
	if head == "" {
		return "", "", nil
	}
	working, err := r.IsWorkingCommit(head)
	if err != nil {
		return "", "", mapErr(err)
	}
	if !working {
		return head, head, nil
	}
	// The working commit's edits are shown against its LAST parent: for the
	// merge a conflicted pull leaves, that is the upstream side being adopted
	// (the same rule `cairn status` and `cairn diff` use).
	parents, err := eng.Parents(head)
	if err != nil {
		return "", "", mapErr(err)
	}
	if len(parents) == 0 {
		return "", head, nil
	}
	return parents[len(parents)-1], head, nil
}

// cmdLsFiles lists the files of the line you stand in.
// Usage: cairn ls-files [-c] [-m] [-d] [-o [--exclude-standard]] [-s] [-z] [--] [<path>...]
//
// cairn has no index: everything on disk that is not ignored already belongs
// to the working change. So the "cached" list is the working change's tree;
// -m/-d are its modifications/deletions against the sealed tip; and -o
// (untracked) can only ever be ignored files — nothing at all once
// --exclude-standard hides those.
func cmdLsFiles(args []string) error {
	g := newGitOpts("ls-files", "c|cached m|modified o|others d|deleted s|stage z exclude-standard "+
		"full-name error-unmatch t i|ignored k|killed u|unmerged x|exclude= directory no-empty-directory deduplicate")
	if err := g.parse(args); err != nil {
		return err
	}
	r, err := openRepoSynced(g.repoDir(), defaultAuthor())
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	eng := r.Engine()
	lc, err := currentLine(r, g.repoDir())
	if err != nil {
		return err
	}
	paths, err := lc.treePaths(append(g.pos, g.paths...))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		paths = lc.prefixSpec()
	}
	ps := change.NewPathspec(paths)
	base, head, err := workingPair(r, lc.branch)
	if err != nil {
		return err
	}
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()
	term := "\n"
	if g.on("z") {
		term = "\x00"
	}
	shown := 0
	emit := func(p, lead string) {
		name := p
		if !g.on("full-name") {
			name = lc.display(p)
		}
		if !g.on("z") {
			name = change.QuotePath(name)
		}
		_, _ = out.WriteString(lead + name + term)
		shown++
	}
	listed := g.on("m") || g.on("d") || g.on("o") || g.on("u") || g.on("k") || g.on("i")
	if g.on("c") || !listed {
		files, err := eng.CommitFiles(head, ps)
		if err != nil {
			return mapErr(err)
		}
		for _, f := range files {
			lead := ""
			if g.on("s") {
				lead = fmt.Sprintf("%s %s 0\t", f.Mode, f.SHA)
			}
			emit(f.Path, lead)
		}
	}
	if (g.on("m") || g.on("d")) && base != head {
		changes, err := eng.TreeChanges(base, head, ps)
		if err != nil {
			return mapErr(err)
		}
		for _, ch := range changes {
			st := ch.Status()
			if st == 'A' || (st != 'D' && !g.on("m")) {
				continue
			}
			emit(ch.Path, "")
		}
	}
	if g.on("o") && !g.on("exclude-standard") && lc.dir != "" {
		tracked := map[string]bool{}
		files, err := eng.CommitFiles(head, nil)
		if err != nil {
			return mapErr(err)
		}
		for _, f := range files {
			tracked[f.Path] = true
		}
		var others []string
		_ = filepath.WalkDir(lc.dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if n := d.Name(); n == ".git" || n == ".cairn" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, rerr := filepath.Rel(lc.dir, p)
			if rerr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if !tracked[rel] && ps.Match(rel) {
				others = append(others, rel)
			}
			return nil
		})
		sort.Strings(others)
		for _, p := range others {
			emit(p, "")
		}
	}
	if g.on("error-unmatch") && shown == 0 && len(g.pos)+len(g.paths) > 0 {
		return fmt.Errorf("ls-files: pathspec %q did not match any file(s) known to cairn", strings.Join(append(g.pos, g.paths...), " "))
	}
	return nil
}

// cmdDescribe names a commit after the nearest tag.
// Usage: cairn describe [--tags] [--always] [--abbrev=<n>] [--dirty[=<mark>]] [--long] [--exact-match] [--match <glob>] [<rev>]
//
// cairn's tags are all lightweight, so --tags is implied.
func cmdDescribe(args []string) error {
	g := newGitOpts("describe", "tags always abbrev= dirty? long exact-match all match= first-parent candidates= contains")
	if err := g.parse(args); err != nil {
		return err
	}
	if len(g.pos) > 1 {
		return errors.New("usage: cairn describe [--tags] [--always] [--abbrev=<n>] [--dirty] [<rev>]")
	}
	if g.has("dirty") && len(g.pos) > 0 {
		return errors.New("describe: --dirty describes the working copy and cannot be combined with a revision")
	}
	open := openRepo
	if g.has("dirty") {
		open = openRepoSynced
	}
	r, err := open(g.repoDir(), defaultAuthor())
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	eng := r.Engine()
	rev := "HEAD"
	if len(g.pos) == 1 {
		rev = g.pos[0]
	}
	sha, err := eng.ResolveCommit(rev)
	if err != nil {
		return mapErr(err)
	}
	if g.on("contains") {
		// `describe --contains` is `name-rev --tags --name-only`:
		// the nearest tag at or AFTER the commit.
		opt := nameRevOptions{tagsOnly: !g.on("all"), nameOnly: true}
		if pat := g.str("match"); pat != "" {
			opt.refs = []string{"refs/tags/" + pat}
		}
		names, err := nameRevNames(eng, []string{sha}, opt)
		if err != nil {
			return mapErr(err)
		}
		name := names[sha]
		if name == "" {
			if g.on("always") {
				name = sha[:7]
			} else {
				return fmt.Errorf("describe: cannot describe '%s'", sha)
			}
		}
		fmt.Println(name)
		return nil
	}
	abbrev, err := g.integer("abbrev", 7)
	if err != nil {
		return err
	}
	var match func(string) bool
	if pat := g.str("match"); pat != "" {
		match = func(tag string) bool { ok, _ := path.Match(pat, tag); return ok }
	}
	tag, depth, err := eng.DescribeWith(sha, match, !g.on("tags") && !g.on("all"))
	if err != nil {
		return mapErr(err)
	}
	var name string
	switch {
	case tag == "" && g.on("always"):
		name = sha[:max(min(abbrev, len(sha)), 4)]
		if abbrev == 0 {
			name = sha[:7]
		}
	case tag == "" && !g.on("tags"):
		return errors.New("describe: no annotated tags can describe this commit (try --tags, or --always to fall back to the abbreviated hash)")
	case tag == "":
		return errors.New("describe: no tags can describe this commit (use --always to fall back to the abbreviated hash)")
	case depth > 0 && g.on("exact-match"):
		return fmt.Errorf("describe: no tag exactly matches %s", sha)
	case abbrev == 0 || (depth == 0 && !g.on("long")):
		name = tag
	default:
		name = fmt.Sprintf("%s-%d-g%s", tag, depth, sha[:min(abbrev, len(sha))])
	}
	if g.has("dirty") {
		lc, err := currentLine(r, g.repoDir())
		if err != nil {
			return err
		}
		base, head, err := workingPair(r, lc.branch)
		if err != nil {
			return err
		}
		if base != head {
			changes, err := eng.TreeChanges(base, head, nil)
			if err != nil {
				return mapErr(err)
			}
			if len(changes) > 0 {
				name += firstNonEmptyStr(g.str("dirty"), "-dirty")
			}
		}
	}
	fmt.Println(name)
	return nil
}
