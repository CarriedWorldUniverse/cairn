package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// multiFlag collects a repeatable string flag (-m a -m b).
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, "\n\n") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// cmdTag is git's `tag`. `tag <name> [rev]` tags any commit (a line name means
// that line's sealed tip); -a/-m/-F make an annotated tag object; bare or -l
// lists; -d deletes; -f moves. Create/delete/move are oplog entries, so
// `cairn undo` reverts them.
func cmdTag(args []string) error {
	fs := flag.NewFlagSet("tag", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	var list, annotated, del, force, quiet bool
	var msgs multiFlag
	fs.BoolVar(&list, "l", false, "list tags")
	fs.BoolVar(&list, "list", false, "list tags")
	fs.BoolVar(&annotated, "a", false, "annotated tag (needs -m or -F)")
	fs.BoolVar(&annotated, "annotate", false, "annotated tag (needs -m or -F)")
	fs.Var(&msgs, "m", "tag message (implies -a)")
	fs.Var(&msgs, "message", "tag message (implies -a)")
	file := fs.String("F", "", "read the tag message from a file (- = stdin)")
	fs.StringVar(file, "file", "", "read the tag message from a file (- = stdin)")
	fs.BoolVar(&del, "d", false, "delete tags")
	fs.BoolVar(&del, "delete", false, "delete tags")
	fs.BoolVar(&force, "f", false, "replace an existing tag")
	fs.BoolVar(&force, "force", false, "replace an existing tag")
	fs.BoolVar(&quiet, "q", false, "quiet")
	var sorts multiFlag
	fs.Var(&sorts, "sort", "sort key: refname, -refname, creatordate, -creatordate, version:refname")
	format := fs.String("format", "", "output format")
	fs.Bool("no-color", false, "accepted")
	fs.Bool("s", false, "")
	fs.String("u", "", "")

	// -n / -n<N>: how many message lines to show when listing.
	nLines := 0
	var stripped []string
	nRe := regexp.MustCompile(`^-n(\d*)$`)
	for _, a := range args {
		if m := nRe.FindStringSubmatch(a); m != nil {
			nLines = 1
			if m[1] != "" {
				nLines, _ = strconv.Atoi(m[1])
			}
			continue
		}
		stripped = append(stripped, a)
	}
	stripped, contains, hasContains := takeOptionalRev(stripped, "contains")
	stripped, noContains, hasNoContains := takeOptionalRev(stripped, "no-contains")
	stripped, points, hasPoints := takeOptionalRev(stripped, "points-at")
	stripped = expandShortCluster(stripped, "ladfq")
	if err := parseArgs(fs, stripped); err != nil {
		return err
	}
	if fs.Lookup("s").Value.String() == "true" || fs.Lookup("u").Value.String() != "" {
		return errors.New("signed tags (-s/-u) are not supported: cairn has no GPG integration")
	}
	pos := fs.Args()

	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()

	switch {
	case del:
		if len(pos) == 0 {
			return errors.New("tag name required")
		}
		var errs []string
		for _, n := range pos {
			sha, err := r.DeleteTag(n)
			if err != nil {
				errs = append(errs, fmt.Sprintf("tag '%s' not found.", n))
				continue
			}
			if !quiet {
				fmt.Printf("Deleted tag '%s' (was %s)\n", n, short7(sha))
			}
		}
		if len(errs) > 0 {
			return errors.New(strings.Join(errs, "\n"))
		}
		return nil
	case list || len(pos) == 0 || nLines > 0 || hasContains || hasNoContains || hasPoints || len(sorts) > 0 || *format != "":
		return tagList(r, tagListOpts{patterns: pos, n: nLines, sorts: sorts, format: *format,
			contains: contains, noContains: noContains, points: points})
	}

	// Create.
	if len(pos) > 2 {
		return errors.New("usage: cairn tag [-a] [-m <msg>] [-f] <name> [<rev>]")
	}
	name := pos[0]
	rev := "HEAD"
	if len(pos) == 2 {
		rev = pos[1]
	}
	message := strings.Join(msgs, "\n\n")
	if *file != "" {
		var b []byte
		if *file == "-" {
			b, err = io.ReadAll(os.Stdin)
		} else {
			b, err = os.ReadFile(*file)
		}
		if err != nil {
			return fmt.Errorf("reading message: %w", err)
		}
		message = string(b)
	}
	isAnnotated := annotated || message != "" || *file != ""
	if isAnnotated && strings.TrimSpace(message) == "" {
		return errors.New("an annotated tag needs a message: pass -m <msg> or -F <file>")
	}
	if err := r.CreateTag(name, rev, message, isAnnotated, force); err != nil {
		return mapErr(err)
	}
	if !quiet {
		fmt.Fprintf(os.Stderr, "cairn: tagged %s -> %s\n", rev, name)
	}
	return nil
}

type tagListOpts struct {
	patterns, sorts, contains, noContains, points []string
	n                                             int
	format                                        string
}

// versionLess is git's version:refname order: digit runs compare numerically.
var versionChunk = regexp.MustCompile(`\d+|\D+`)

func versionLess(a, b string) bool {
	chunk := versionChunk
	ca, cb := chunk.FindAllString(a, -1), chunk.FindAllString(b, -1)
	for i := 0; i < len(ca) && i < len(cb); i++ {
		x, y := ca[i], cb[i]
		xn, xerr := strconv.ParseUint(x, 10, 64)
		yn, yerr := strconv.ParseUint(y, 10, 64)
		if xerr == nil && yerr == nil {
			if xn != yn {
				return xn < yn
			}
			continue
		}
		if x != y {
			return x < y
		}
	}
	return len(ca) < len(cb)
}

func tagList(r interface {
	TagInfos() ([]change.TagInfo, error)
	ResolveRefsRev(string) (string, error)
	IsAncestor(a, b string) (bool, error)
	CommitSubject(string) string
}, o tagListOpts) error {
	tags, err := r.TagInfos()
	if err != nil {
		return mapErr(err)
	}
	resolve := func(revs []string) ([]string, error) {
		var out []string
		for _, rev := range revs {
			sha, err := r.ResolveRefsRev(rev)
			if err != nil {
				return nil, fmt.Errorf("malformed object name %s: %w", rev, err)
			}
			out = append(out, sha)
		}
		return out, nil
	}
	cs, err := resolve(o.contains)
	if err != nil {
		return err
	}
	ncs, err := resolve(o.noContains)
	if err != nil {
		return err
	}
	ps, err := resolve(o.points)
	if err != nil {
		return err
	}
	var keep []change.TagInfo
	for _, t := range tags {
		ok, err := matchAnyGlob(o.patterns, t.Name)
		if err != nil {
			return err
		}
		for _, c := range cs {
			if v, _ := r.IsAncestor(c, t.Commit); !v {
				ok = false
			}
		}
		for _, c := range ncs {
			if v, _ := r.IsAncestor(c, t.Commit); v {
				ok = false
			}
		}
		if len(ps) > 0 {
			hit := false
			for _, p := range ps {
				if t.Commit == p || t.Obj == p {
					hit = true
				}
			}
			ok = ok && hit
		}
		if ok {
			keep = append(keep, t)
		}
	}
	tags = keep
	// Default order is by refname (the catalogue returns it). Like git, the
	// LAST --sort key is the primary one: apply them first to last, stably.
	for i := 0; i < len(o.sorts); i++ {
		key := o.sorts[i]
		desc := strings.HasPrefix(key, "-")
		key = strings.TrimPrefix(key, "-")
		var less func(a, b change.TagInfo) bool
		switch key {
		case "refname":
			less = func(a, b change.TagInfo) bool { return a.Name < b.Name }
		case "version:refname", "v:refname":
			less = func(a, b change.TagInfo) bool { return versionLess(a.Name, b.Name) }
		case "creatordate", "taggerdate", "committerdate":
			less = func(a, b change.TagInfo) bool { return a.Date.Before(b.Date) }
		default:
			return fmt.Errorf("unknown sort key %q (supported: refname, version:refname, creatordate; prefix '-' to reverse)", o.sorts[i])
		}
		sort.SliceStable(tags, func(x, y int) bool {
			if desc {
				return less(tags[y], tags[x])
			}
			return less(tags[x], tags[y])
		})
	}
	for _, t := range tags {
		t := t
		msg := strings.TrimRight(t.Message, "\n")
		if !t.Annotated {
			msg = r.CommitSubject(t.Commit)
		}
		switch {
		case o.format != "":
			line, err := expandFormat(o.format, func(f, mod string) (string, bool) {
				switch f {
				case "refname":
					if mod == "short" {
						return t.Name, true
					}
					return "refs/tags/" + t.Name, true
				case "objectname":
					v := t.Commit
					if t.Obj != "" {
						v = t.Obj
					}
					if mod == "short" {
						v = short7(v)
					}
					return v, true
				case "*objectname":
					if !t.Annotated {
						return "", true
					}
					if mod == "short" {
						return short7(t.Commit), true
					}
					return t.Commit, true
				case "objecttype":
					if t.Annotated {
						return "tag", true
					}
					return "commit", true
				case "subject", "contents:subject":
					return strings.SplitN(msg, "\n", 2)[0], true
				case "contents":
					return msg, true
				case "taggername":
					return t.Tagger, true
				case "creatordate", "taggerdate":
					return t.Date.Format("Mon Jan 2 15:04:05 2006 -0700"), true
				}
				return "", false
			})
			if err != nil {
				return err
			}
			fmt.Println(line)
		case o.n > 0:
			lines := strings.Split(msg, "\n")
			if len(lines) > o.n {
				lines = lines[:o.n]
			}
			fmt.Printf("%-15s %s\n", t.Name, lines[0])
			for _, l := range lines[1:] {
				fmt.Printf("    %s\n", l)
			}
		default:
			fmt.Println(t.Name)
		}
	}
	return nil
}
