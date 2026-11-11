package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/gitattributes"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Maintenance and inspection verbs: reflog, gc, fsck, count-objects, archive,
// check-ignore, help.

func humanBytes(n int64) string {
	neg := ""
	if n < 0 {
		neg, n = "-", -n
	}
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%s%.1f MiB", neg, float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%s%.1f KiB", neg, float64(n)/(1<<10))
	}
	return fmt.Sprintf("%s%d bytes", neg, n)
}

// gitHumanBytes is git's strbuf_humanise_bytes: two decimals, 1024-based.
func gitHumanBytes(n int64) string {
	switch {
	case n > 1<<30:
		return fmt.Sprintf("%d.%02d GiB", n>>30, (n&(1<<30-1))/10737419)
	case n > 1<<20:
		return fmt.Sprintf("%d.%02d MiB", n>>20, (n&(1<<20-1))/10485)
	case n > 1<<10:
		return fmt.Sprintf("%d.%02d KiB", n>>10, (n&(1<<10-1))*100/1024)
	}
	return fmt.Sprintf("%d bytes", n)
}

// cmdReflog shows the operation log through one line, git-style:
// `<sha> <line>@{N}: <op>: <detail>`, newest first. Only operations that moved
// the line's tip appear; `cairn oplog` is the unfiltered log.
func cmdReflog(args []string) error {
	if len(args) > 0 && args[0] == "show" {
		args = args[1:]
	}
	if len(args) > 0 && (args[0] == "expire" || args[0] == "delete" || args[0] == "exists") {
		return errors.New("the operation log is append-only — `cairn undo` reverts an operation, nothing prunes or deletes entries")
	}
	fs := flag.NewFlagSet("reflog", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	n := fs.Int("n", 0, "show at most N entries")
	fs.IntVar(n, "max-count", 0, "show at most N entries")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	line := ""
	if fs.NArg() > 0 {
		line = fs.Arg(0)
	}
	label := line
	if line == "" || line == "HEAD" {
		// git's reflog of HEAD: the line you stand in, spelled HEAD@{N}.
		label = "HEAD"
		if b, ok := r.CWDBranch(); ok {
			line = b
		} else if line, err = r.DefaultBranch(); err != nil {
			return mapErr(err)
		}
	}
	entries, err := r.Reflog(line)
	if err != nil {
		return mapErr(err)
	}
	for i, e := range entries {
		if *n > 0 && i >= *n {
			break
		}
		msg := e.Op
		if e.Detail != "" {
			msg += ": " + e.Detail
		}
		fmt.Printf("%s %s@{%d}: %s\n", short7(e.Sha), label, i, msg)
	}
	return nil
}

// cmdGC packs the object store, drops loose copies and superseded packs,
// vacuums the catalogue and prunes stale caches. Everything the catalogue
// refers to (undo targets, working snapshots, stashes) counts as reachable;
// unreferenced loose objects and old packs/caches are only removed past a
// 14-day grace (--prune=now drops it).
func cmdGC(args []string) error {
	fs := flag.NewFlagSet("gc", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	fs.Bool("aggressive", false, "accepted: cairn always repacks in full")
	fs.Bool("auto", false, "accepted")
	prune := fs.String("prune", "", "prune loose objects older than <date>; 'now' prunes everything unreferenced")
	quiet := fs.Bool("quiet", false, "no output")
	fs.BoolVar(quiet, "q", false, "no output")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if *prune != "" && *prune != "now" {
		return errors.New("--prune only supports 'now' (the default grace is 14 days)")
	}
	r, err := openRepoSynced(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	rep, err := r.Gc(*prune == "now")
	if err != nil {
		return mapErr(err)
	}
	if !*quiet {
		fmt.Fprintf(os.Stderr, "cairn: gc: packed %d objects, pruned %d loose, dropped %d cache file(s); store %s -> %s (reclaimed %s)\n",
			rep.Packed, rep.PrunedLoose, rep.CacheFiles, humanBytes(rep.BeforeBytes), humanBytes(rep.AfterBytes),
			humanBytes(rep.BeforeBytes-rep.AfterBytes+rep.CacheBytes))
	}
	return nil
}

// cmdFsck verifies that every commit, tree and blob reachable from the line
// tips, open changes and tags exists, and that the catalogue is consistent.
// Exit status is 1 when anything is wrong.
func cmdFsck(args []string) error {
	fs := flag.NewFlagSet("fsck", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	verbose := fs.Bool("verbose", false, "report what was checked")
	fs.BoolVar(verbose, "v", false, "report what was checked")
	for _, n := range []string{"full", "strict", "no-dangling", "no-progress", "connectivity-only"} {
		fs.Bool(n, false, "accepted")
	}
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	rep, err := r.Fsck()
	if err != nil {
		return mapErr(err)
	}
	for _, p := range rep.Problems {
		fmt.Fprintln(os.Stderr, "error:", p)
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "checked %d commits, %d trees\n", rep.Commits, rep.Trees)
	}
	if len(rep.Problems) > 0 {
		return fmt.Errorf("fsck found %d problem(s)", len(rep.Problems))
	}
	return nil
}

// cmdCountObjects prints loose/packed object counts, like git's.
func cmdCountObjects(args []string) error {
	fs := flag.NewFlagSet("count-objects", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	verbose := fs.Bool("v", false, "verbose")
	fs.BoolVar(verbose, "verbose", false, "verbose")
	human := fs.Bool("H", false, "human-readable sizes")
	fs.BoolVar(human, "human-readable", false, "human-readable sizes")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	c, err := r.CountObjects()
	if err != nil {
		return mapErr(err)
	}
	size := func(k int64) string {
		if *human {
			return gitHumanBytes(k * 1024)
		}
		return fmt.Sprint(k)
	}
	if !*verbose {
		unit := " kilobytes"
		if *human {
			unit = ""
		}
		fmt.Printf("%d objects, %s%s\n", c.Loose, size(c.LooseKiB), unit)
		return nil
	}
	fmt.Printf("count: %d\nsize: %s\nin-pack: %d\npacks: %d\nsize-pack: %s\nprune-packable: 0\ngarbage: %d\nsize-garbage: %s\n",
		c.Loose, size(c.LooseKiB), c.InPack, c.Packs, size(c.PackKiB), c.Garbage, size(c.GarbageKiB))
	return nil
}

// cmdArchive writes a commit's tree as tar, tar.gz or zip — like git archive:
// `archive [--format=tar|tar.gz|tgz|zip] [-o file] [--prefix=p/] <rev> [path...]`.
// <rev> is a line name (its sealed tip), a tag, a sha, or HEAD (the line you
// stand in); entries carry the commit's time, git's default modes (0664 / 0775
// for executables) and symlinks as symlinks.
func cmdArchive(args []string) error {
	fs := flag.NewFlagSet("archive", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	format := fs.String("format", "", "tar, tar.gz, tgz or zip (default: from -o's extension, else tar)")
	out := fs.String("output", "", "write to a file instead of stdout")
	fs.StringVar(out, "o", "", "write to a file instead of stdout")
	prefix := fs.String("prefix", "", "prepend this path to every entry (e.g. proj-1.0/)")
	listFmt := fs.Bool("list", false, "list the supported formats")
	fs.BoolVar(listFmt, "l", false, "list the supported formats")
	fs.String("remote", "", "refused: archiving a remote is not supported")
	fs.Bool("worktree-attributes", false, "accepted (only the attributes committed in <rev> are read)")
	for d := '0'; d <= '9'; d++ {
		fs.Bool(string(d), false, "compression level (accepted)")
	}
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if *listFmt {
		fmt.Println("tar\ntar.gz\ntgz\nzip")
		return nil
	}
	if fs.Lookup("remote").Value.String() != "" {
		return errors.New("--remote is not supported: clone the repository, then archive it")
	}
	if fs.NArg() < 1 {
		return errors.New("usage: cairn archive [--format=tar|tar.gz|zip] [-o <file>] [--prefix=<p>/] <rev> [<path>...]")
	}
	f := *format
	if f == "" {
		switch {
		case strings.HasSuffix(*out, ".tar.gz"), strings.HasSuffix(*out, ".tgz"):
			f = "tar.gz"
		case strings.HasSuffix(*out, ".zip"):
			f = "zip"
		default:
			f = "tar"
		}
	}
	if f != "tar" && f != "tar.gz" && f != "tgz" && f != "zip" {
		return fmt.Errorf("unknown archive format %q (tar, tar.gz, tgz, zip)", f)
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	sha, err := r.ResolveRefsRev(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("not a valid object name: %s (%w)", fs.Arg(0), err)
	}
	var filters []string
	for _, p := range fs.Args()[1:] {
		filters = append(filters, strings.Trim(filepath.ToSlash(p), "/"))
	}
	included := func(path string) bool {
		if len(filters) == 0 {
			return true
		}
		for _, f := range filters {
			if f == "" || f == "." || path == f || strings.HasPrefix(path, f+"/") {
				return true
			}
		}
		return false
	}

	// .gitattributes in the archived tree: export-ignore drops a file or a
	// whole directory, like git archive. (export-subst is not applied.)
	attrIgnored, substNote, err := exportAttributes(r, sha)
	if err != nil {
		return mapErr(err)
	}
	pathFilter := included
	included = func(path string) bool { return pathFilter(path) && !attrIgnored(path) }
	if substNote {
		fmt.Fprintln(os.Stderr, "cairn: note: export-subst attributes are not applied; files are archived verbatim")
	}
	if len(filters) > 0 {
		// Like git: a pathspec that matches nothing is an error before any
		// output (or output file) exists.
		matched := false
		_, _ = r.WalkCommitFiles(sha, func(path string, _ filemode.FileMode, _ *object.File) error {
			if included(path) {
				matched = true
			}
			return nil
		})
		if !matched {
			return fmt.Errorf("pathspec %v did not match any files", filters)
		}
	}
	var w io.Writer = os.Stdout
	var outFile *os.File
	if *out != "" {
		outFile, err = os.Create(*out)
		if err != nil {
			return err
		}
		w = outFile
	}
	bw := bufio.NewWriter(w)
	var emit archiveWriter
	var closers []func() error
	switch f {
	case "zip":
		zw := zip.NewWriter(bw)
		emit = &zipEmitter{zw: zw}
		closers = append(closers, zw.Close)
	default:
		var tw *tar.Writer
		if f == "tar" {
			tw = tar.NewWriter(bw)
		} else {
			gz := gzip.NewWriter(bw)
			tw = tar.NewWriter(gz)
			closers = append(closers, gz.Close)
		}
		emit = &tarEmitter{tw: tw}
		closers = append([]func() error{tw.Close}, closers...)
	}

	mtime, err := r.CommitTime(sha)
	if err != nil {
		return mapErr(err)
	}
	dirs := map[string]bool{}
	emitDirs := func(path string) error {
		parts := strings.Split(path, "/")
		for i := 1; i < len(parts); i++ {
			d := strings.Join(parts[:i], "/")
			if !dirs[d] {
				dirs[d] = true
				if err := emit.dir(*prefix+d+"/", mtime); err != nil {
					return err
				}
			}
		}
		return nil
	}
	count := 0 // entries written; the first one triggers the prefix directory
	_, werr := r.WalkCommitFiles(sha, func(path string, mode filemode.FileMode, file *object.File) error {
		if !included(path) {
			return nil
		}
		if count == 0 && *prefix != "" && strings.HasSuffix(*prefix, "/") {
			// git emits the bare prefix directory first.
			if err := emit.dir(*prefix, mtime); err != nil {
				return err
			}
		}
		count++
		if err := emitDirs(path); err != nil {
			return err
		}
		rd, err := file.Reader()
		if err != nil {
			return err
		}
		defer rd.Close()
		return emit.file(*prefix+path, mode, file.Size, mtime, rd)
	})
	for _, c := range closers {
		if err := c(); werr == nil {
			werr = err
		}
	}
	if err := bw.Flush(); werr == nil {
		werr = err
	}
	if outFile != nil {
		if err := outFile.Close(); werr == nil {
			werr = err
		}
	}
	if werr != nil {
		return werr
	}
	return nil
}

type archiveWriter interface {
	dir(name string, mt time.Time) error
	file(name string, mode filemode.FileMode, size int64, mt time.Time, rd io.Reader) error
}

type tarEmitter struct{ tw *tar.Writer }

func (t *tarEmitter) dir(name string, mt time.Time) error {
	return t.tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name, Mode: 0o775, ModTime: mt, Uname: "root", Gname: "root", Format: tar.FormatPAX})
}

func (t *tarEmitter) file(name string, mode filemode.FileMode, size int64, mt time.Time, rd io.Reader) error {
	h := &tar.Header{Name: name, ModTime: mt, Uname: "root", Gname: "root", Format: tar.FormatPAX}
	switch mode {
	case filemode.Symlink:
		b, err := io.ReadAll(rd)
		if err != nil {
			return err
		}
		h.Typeflag, h.Linkname, h.Mode = tar.TypeSymlink, string(b), 0o777
		return t.tw.WriteHeader(h)
	case filemode.Executable:
		h.Mode = 0o775
	default:
		h.Mode = 0o664
	}
	h.Typeflag, h.Size = tar.TypeReg, size
	if err := t.tw.WriteHeader(h); err != nil {
		return err
	}
	_, err := io.Copy(t.tw, rd)
	return err
}

type zipEmitter struct{ zw *zip.Writer }

func (z *zipEmitter) dir(name string, mt time.Time) error {
	h := &zip.FileHeader{Name: name, Method: zip.Store, Modified: mt}
	h.SetMode(os.ModeDir | 0o775)
	_, err := z.zw.CreateHeader(h)
	return err
}

func (z *zipEmitter) file(name string, mode filemode.FileMode, size int64, mt time.Time, rd io.Reader) error {
	h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: mt}
	switch mode {
	case filemode.Symlink:
		h.SetMode(os.ModeSymlink | 0o777)
	case filemode.Executable:
		h.SetMode(0o775)
	default:
		h.SetMode(0o664)
	}
	w, err := z.zw.CreateHeader(h)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, rd)
	return err
}

// cmdCheckIgnore reports which paths the line folder's .gitignore/.cairnignore
// rules ignore — the same rules, library and precedence the working-copy scan
// applies. Paths are relative to where you stand, inside a line folder;
// -v prints `<file>:<line>:<pattern><TAB><path>`; exit status 1 when no path
// is ignored (like git).
func cmdCheckIgnore(args []string) error {
	fs := flag.NewFlagSet("check-ignore", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	verbose := fs.Bool("v", false, "show the matching file, line and pattern")
	fs.BoolVar(verbose, "verbose", false, "show the matching file, line and pattern")
	nonMatching := fs.Bool("n", false, "with -v, also show paths that match nothing")
	fs.BoolVar(nonMatching, "non-matching", false, "with -v, also show paths that match nothing")
	stdin := fs.Bool("stdin", false, "read paths from stdin, one per line")
	fs.Bool("no-index", false, "accepted")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	paths := fs.Args()
	if *stdin {
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t != "" {
				paths = append(paths, t)
			}
		}
	}
	if len(paths) == 0 {
		return errors.New("usage: cairn check-ignore [-v] [-n] <path>...")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	any := false
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(r.Root(), abs)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("%s: outside a line folder of this repo", p)
		}
		rel = filepath.ToSlash(rel)
		first, within, _ := strings.Cut(rel, "/")
		if _, ok := r.BranchForFolder(first); !ok || within == "" {
			return fmt.Errorf("%s: not inside an expressed line folder (run from a line's folder, or pass a path under one)", p)
		}
		m, err := r.CheckIgnore(first, within)
		if err != nil {
			return mapErr(err)
		}
		switch {
		case m.Ignored:
			any = true
			if *verbose {
				fmt.Printf("%s:%d:%s\t%s\n", m.Source, m.Line, m.Pattern, p)
			} else {
				fmt.Println(p)
			}
		case *verbose && *nonMatching:
			fmt.Printf("::\t%s\n", p)
		}
	}
	if !any {
		return errSilent
	}
	return nil
}

// ---- help ----

// verbUsage returns the usage-table lines that describe verb (lines whose
// first word is the verb), plus the git-spelling line for it.
func verbUsage(verb string) []string {
	var out []string
	for _, ln := range strings.Split(usage, "\n") {
		if !strings.HasPrefix(ln, "  ") || strings.HasPrefix(ln, "   ") {
			continue
		}
		f := strings.Fields(ln)
		if len(f) > 0 && f[0] == verb {
			out = append(out, ln)
		}
	}
	return out
}

// helpFlagVerbs are the commands whose flag set can be listed by running them
// with -h (they parse flags before touching anything).
var helpFlagVerbs = map[string]bool{
	"branch": true, "tag": true, "remote": true, "push": true, "fetch": true, "pull": true,
	"clone": true, "init": true, "config": true, "gc": true, "fsck": true, "count-objects": true,
	"archive": true, "check-ignore": true, "reflog": true,
}

var inHelp bool

// printVerbHelp prints a verb's usage-table lines and, for the verbs that
// support it, its flag list. It reports whether the verb is known.
func printVerbHelp(verb string) bool {
	if verb == "pr" {
		fmt.Println(prUsage)
		return true
	}
	lines := verbUsage(verb)
	if hint, ok := gitOnlyVerbs[verb]; ok && len(lines) == 0 {
		fmt.Printf("git %s: %s\n", verb, hint)
		return true
	}
	if len(lines) == 0 {
		return false
	}
	fmt.Printf("usage: cairn %s ...\n\n", verb)
	for _, l := range lines {
		fmt.Println(l)
	}
	if helpFlagVerbs[verb] {
		old := os.Stderr
		rd, wr, err := os.Pipe()
		if err == nil {
			os.Stderr = wr
			inHelp = true
			done := make(chan struct{})
			var buf strings.Builder
			go func() { _, _ = io.Copy(&buf, rd); close(done) }()
			_ = run([]string{verb, "-h"})
			inHelp = false
			_ = wr.Close()
			<-done
			os.Stderr = old
			var keep []string
			for _, l := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
				if strings.HasPrefix(l, "Usage of") {
					continue
				}
				keep = append(keep, l)
			}
			if len(keep) > 0 && strings.TrimSpace(strings.Join(keep, "")) != "" {
				fmt.Println("\nflags:")
				fmt.Println(strings.Join(keep, "\n"))
			}
		}
	}
	return true
}

// cmdHelp is `help [verb]`: the whole usage table, or one verb's section.
func cmdHelp(args []string) error {
	if len(args) == 0 {
		fmt.Println(usage)
		return nil
	}
	if len(args) == 1 && isAgentHelpTopic(args[0]) {
		printAgentHelp()
		return nil
	}
	verbs := args
	sort.Strings(verbs)
	for _, v := range verbs {
		if !printVerbHelp(v) {
			return fmt.Errorf("no help for %q — 'cairn help' lists every verb", v)
		}
	}
	return nil
}

// wantsHelp reports whether args ask for the verb's help (-h / --help first).
func wantsHelp(args []string) bool {
	return len(args) > 0 && (args[0] == "-h" || args[0] == "--help") && !inHelp
}

// exportAttributes reads every .gitattributes committed in the commit's tree
// and returns a predicate for "this path (or a parent directory) is
// export-ignore"d, plus whether any export-subst attribute is present.
func exportAttributes(r *worktree.Repo, sha string) (func(string) bool, bool, error) {
	type af struct {
		domain []string
		body   string
	}
	var files []af
	_, err := r.WalkCommitFiles(sha, func(path string, _ filemode.FileMode, f *object.File) error {
		if path != ".gitattributes" && !strings.HasSuffix(path, "/.gitattributes") {
			return nil
		}
		body, err := f.Contents()
		if err != nil {
			return err
		}
		var dom []string
		if i := strings.LastIndex(path, "/"); i >= 0 {
			dom = strings.Split(path[:i], "/")
		}
		files = append(files, af{dom, body})
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if len(files) == 0 {
		return func(string) bool { return false }, false, nil
	}
	// Shallow before deep, so a nested file overrides its parent's rules.
	sort.SliceStable(files, func(i, j int) bool { return len(files[i].domain) < len(files[j].domain) })
	var all []gitattributes.MatchAttribute
	subst := false
	for _, f := range files {
		attrs, err := gitattributes.ReadAttributes(strings.NewReader(f.body), f.domain, true)
		if err != nil {
			return nil, false, err
		}
		for _, a := range attrs {
			for _, at := range a.Attributes {
				if at.Name() == "export-subst" && at.IsSet() {
					subst = true
				}
			}
		}
		all = append(all, attrs...)
	}
	m := gitattributes.NewMatcher(all)
	return func(path string) bool {
		parts := strings.Split(path, "/")
		for i := 1; i <= len(parts); i++ {
			if res, _ := m.Match(parts[:i], []string{"export-ignore"}); res != nil {
				if a, ok := res["export-ignore"]; ok && a.IsSet() {
					return true
				}
			}
		}
		return false
	}, subst, nil
}
