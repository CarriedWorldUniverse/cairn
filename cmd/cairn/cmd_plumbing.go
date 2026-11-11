package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// Read-only plumbing that scripts call: merge-base, symbolic-ref,
// hash-object, for-each-ref, name-rev.

// cmdMergeBase: cairn merge-base [--all] [--is-ancestor] <a> <b>
func cmdMergeBase(args []string) error {
	g := newGitOpts("merge-base", "a|all is-ancestor fork-point octopus independent")
	if err := g.parse(args); err != nil {
		return err
	}
	if g.on("fork-point") {
		return errors.New("merge-base: --fork-point reads git's reflog; cairn keeps an operation log instead — use `cairn merge-base <a> <b>`")
	}
	if g.on("octopus") || g.on("independent") {
		return errors.New("merge-base: --octopus/--independent are not supported; give two revisions")
	}
	if len(g.pos) != 2 {
		return errors.New("usage: cairn merge-base [--all] <rev> <rev>  |  cairn merge-base --is-ancestor <rev> <rev>")
	}
	r, err := openRepo(g.repoDir(), defaultAuthor())
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	eng := r.Engine()
	if _, err := currentLine(r, g.repoDir()); err != nil {
		return err
	}
	if g.on("is-ancestor") {
		a, err := eng.ResolveCommit(g.pos[0])
		if err != nil {
			return mapErr(err)
		}
		b, err := eng.ResolveCommit(g.pos[1])
		if err != nil {
			return mapErr(err)
		}
		ok, err := eng.IsAncestor(a, b)
		if err != nil {
			return mapErr(err)
		}
		if !ok {
			return errExit1
		}
		return nil
	}
	bases, err := eng.MergeBases(g.pos[0], g.pos[1])
	if err != nil {
		return mapErr(err)
	}
	if len(bases) == 0 {
		return errExit1
	}
	if !g.on("all") {
		bases = bases[:1]
	}
	for _, b := range bases {
		fmt.Println(b)
	}
	return nil
}

// cmdSymbolicRef: cairn symbolic-ref [-q] [--short] HEAD
func cmdSymbolicRef(args []string) error {
	g := newGitOpts("symbolic-ref", "q|quiet short d|delete m= no-recurse recurse")
	if err := g.parse(args); err != nil {
		return err
	}
	if g.on("d") || len(g.pos) > 1 {
		return errors.New("symbolic-ref: HEAD is the line folder you stand in and cannot be re-pointed — `cd <line>/` to move it (`cairn express <line>` first if it has no folder)")
	}
	if len(g.pos) != 1 {
		return errors.New("usage: cairn symbolic-ref [-q] [--short] HEAD")
	}
	r, err := openRepo(g.repoDir(), defaultAuthor())
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	lc, err := currentLine(r, g.repoDir())
	if err != nil {
		return err
	}
	if g.pos[0] != "HEAD" {
		if g.on("q") {
			return errExit1
		}
		return fmt.Errorf("symbolic-ref: ref %s is not a symbolic ref", g.pos[0])
	}
	if g.on("short") {
		fmt.Println(lc.branch)
	} else {
		fmt.Println("refs/heads/" + lc.branch)
	}
	return nil
}

// cmdHashObject: cairn hash-object [-w] [-t <type>] [--stdin] [--stdin-paths] [--] <file>...
func cmdHashObject(args []string) error {
	g := newGitOpts("hash-object", "w t= stdin stdin-paths path= no-filters literally")
	if err := g.parse(args); err != nil {
		return err
	}
	typ := firstNonEmptyStr(g.str("t"), "blob")
	if _, err := change.HashObject(typ, nil); err != nil {
		return fmt.Errorf("hash-object: %w", err)
	}
	files := append(append([]string{}, g.pos...), g.paths...)
	var inputs [][]byte
	var named []string
	switch {
	case g.on("stdin-paths"):
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			files = append(files, sc.Text())
		}
	case g.on("stdin") || len(files) == 0:
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		inputs = append(inputs, data)
		named = append(named, "")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("hash-object: %w", err)
		}
		inputs = append(inputs, data)
		named = append(named, f)
	}
	var eng *change.Engine
	if g.on("w") {
		r, err := openRepo(g.repoDir(), defaultAuthor())
		if err != nil {
			return mapErr(err)
		}
		defer r.Close()
		eng = r.Engine()
	}
	for i, data := range inputs {
		var sha string
		var err error
		if !g.on("literally") {
			if verr := checkObjectFormat(typ, data); verr != nil {
				return fmt.Errorf("hash-object: %s: %v", firstNonEmptyStr(named[i], "<stdin>"), verr)
			}
		}
		if eng != nil {
			sha, err = eng.WriteObject(typ, data)
		} else {
			sha, err = change.HashObject(typ, data)
		}
		if err != nil {
			return mapErr(err)
		}
		fmt.Println(sha)
	}
	return nil
}

// checkObjectFormat is the sanity check git hash-object makes (skipped by
// --literally) so a malformed commit, tag or tree is not stored by accident.
func checkObjectFormat(typ string, data []byte) error {
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	switch typ {
	case "commit":
		head, _, _ := strings.Cut(string(data), "\n")
		if v, ok := strings.CutPrefix(head, "tree "); !ok || !hex40.MatchString(v) {
			return errors.New("corrupt commit: no valid tree line")
		}
		if !strings.Contains(string(data), "\nauthor ") || !strings.Contains(string(data), "\ncommitter ") {
			return errors.New("corrupt commit: missing author or committer")
		}
	case "tag":
		head, _, _ := strings.Cut(string(data), "\n")
		if v, ok := strings.CutPrefix(head, "object "); !ok || !hex40.MatchString(v) {
			return errors.New("corrupt tag: no valid object line")
		}
	case "tree":
		for rest := data; len(rest) > 0; {
			sp := strings.IndexByte(string(rest), ' ')
			nul := strings.IndexByte(string(rest), 0)
			if sp < 0 || nul < sp || len(rest) < nul+21 {
				return errors.New("corrupt tree entry")
			}
			rest = rest[nul+21:]
		}
	}
	return nil
}

// ── for-each-ref ────────────────────────────────────────────────────────────

type refInfo struct {
	name   string // full refname
	kind   string
	obj    string // the object the ref names (a tag object for an annotated tag)
	typ    string // its type
	commit string // peeled commit
	tag    *tagInfo
	meta   *change.CommitMeta
	head   bool
}

type tagInfo struct {
	tagger       change.Signature
	hasTagger    bool
	message      string // raw message
	deref        string
	derefTyp     string
	derefCommit  string
	derefSubject string
}

func loadRefInfos(eng *change.Engine, patterns []string) ([]*refInfo, error) {
	names, err := eng.RefNames()
	if err != nil {
		return nil, err
	}
	tagObj := map[string]string{}
	if tags, err := eng.ListTags(); err == nil {
		for _, t := range tags {
			tagObj[t.Name] = t.Commit
		}
	}
	var out []*refInfo
	for _, n := range names {
		ri := &refInfo{kind: n.Kind, commit: n.SHA, head: n.Head}
		switch n.Kind {
		case "head":
			ri.name = "refs/heads/" + n.Name
		case "remote":
			ri.name = "refs/remotes/" + n.Name
		default:
			ri.name = "refs/tags/" + n.Name
		}
		if !refMatches(ri.name, patterns) {
			continue
		}
		ri.obj, ri.typ = n.SHA, "commit"
		if n.Kind == "tag" {
			if o := tagObj[n.Name]; o != "" && o != n.SHA {
				if typ, _, err := eng.ObjectInfo(o); err == nil && typ == "tag" {
					ri.obj, ri.typ = o, "tag"
					ri.tag = parseTagObject(eng, o)
				}
			}
		}
		if m, err := eng.CommitMeta(n.SHA); err == nil {
			ri.meta = m
		}
		out = append(out, ri)
	}
	return out, nil
}

func parseTagObject(eng *change.Engine, sha string) *tagInfo {
	raw, err := eng.ObjectBytes(sha)
	if err != nil {
		return nil
	}
	head, msg, _ := strings.Cut(string(raw), "\n\n")
	ti := &tagInfo{message: msg}
	for _, ln := range strings.Split(head, "\n") {
		k, v, _ := strings.Cut(ln, " ")
		switch k {
		case "object":
			ti.deref = v
		case "type":
			ti.derefTyp = v
		case "tagger":
			if i := strings.LastIndex(v, "> "); i >= 0 {
				lt := strings.Index(v, " <")
				if lt >= 0 {
					ti.tagger.Name, ti.tagger.Email = v[:lt], v[lt+2:i]
				}
				var ts int64
				var tz string
				if _, err := fmt.Sscanf(v[i+2:], "%d %s", &ts, &tz); err == nil {
					when := time.Unix(ts, 0).UTC()
					if z, err := time.Parse("-0700", tz); err == nil {
						when = when.In(z.Location())
					}
					ti.tagger.When = when
					ti.hasTagger = true
				}
			}
		}
	}
	return ti
}

// refMatches is for-each-ref's pattern rule: a path prefix, or a glob in which
// * does not cross a /.
func refMatches(name string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if strings.HasPrefix(name, p) && (len(name) == len(p) || name[len(p)] == '/' || strings.HasSuffix(p, "/")) {
			return true
		}
		if strings.ContainsAny(p, "*?[") {
			var sb strings.Builder
			sb.WriteString("^")
			for i := 0; i < len(p); i++ {
				switch c := p[i]; c {
				case '*':
					sb.WriteString(`[^/]*`)
				case '?':
					sb.WriteString(`[^/]`)
				case '[':
					j := strings.IndexByte(p[i:], ']')
					if j < 0 {
						sb.WriteString(`\[`)
					} else {
						sb.WriteString(p[i : i+j+1])
						i += j
					}
				default:
					sb.WriteString(regexp.QuoteMeta(string(c)))
				}
			}
			sb.WriteString("$")
			if re, err := regexp.Compile(sb.String()); err == nil && re.MatchString(name) {
				return true
			}
		}
	}
	return false
}

func shortRefName(full string) string {
	for _, p := range []string{"refs/heads/", "refs/tags/", "refs/remotes/"} {
		if s, ok := strings.CutPrefix(full, p); ok {
			return s
		}
	}
	return strings.TrimPrefix(full, "refs/")
}

// refAtom evaluates one %(atom[:modifier]) for a ref.
func refAtom(eng *change.Engine, ri *refInfo, atom string, now time.Time, upstream func(*refInfo) string) (string, error) {
	name, mod, _ := strings.Cut(atom, ":")
	deref := strings.HasPrefix(name, "*")
	name = strings.TrimPrefix(name, "*")
	var subj *change.CommitMeta = ri.meta
	// Dereferenced fields of a tag name the commit; for other refs they are empty.
	if deref && ri.tag == nil {
		return "", nil
	}
	dateOf := func(sig change.Signature) string {
		if sig.When.IsZero() {
			return ""
		}
		m := mod
		if m == "" {
			m = "default"
		}
		if m == "default" {
			m = ""
		}
		return formatDate(sig.When, m, now)
	}
	sigAtom := func(prefix string, sig change.Signature, ok bool) (string, bool) {
		rest, has := strings.CutPrefix(name, prefix)
		if !has {
			return "", false
		}
		// An annotated tag object has a tagger, not an author or committer.
		if ri.tag != nil && !deref && prefix != "tagger" {
			ok = false
		}
		if !ok {
			return "", true
		}
		switch rest {
		case "name":
			return sig.Name, true
		case "email":
			e := "<" + sig.Email + ">"
			switch mod {
			case "trim":
				e = sig.Email
			case "localpart":
				e = strings.SplitN(sig.Email, "@", 2)[0]
			}
			return e, true
		case "date":
			return dateOf(sig), true
		}
		return "", false
	}
	switch name {
	case "refname":
		switch {
		case mod == "short":
			return shortRefName(ri.name), nil
		case strings.HasPrefix(mod, "lstrip=") || strings.HasPrefix(mod, "strip="):
			n, _ := strconv.Atoi(mod[strings.IndexByte(mod, '=')+1:])
			parts := strings.Split(ri.name, "/")
			if n > len(parts) {
				n = len(parts)
			}
			return strings.Join(parts[n:], "/"), nil
		case strings.HasPrefix(mod, "rstrip="):
			n, _ := strconv.Atoi(mod[len("rstrip="):])
			parts := strings.Split(ri.name, "/")
			if n > len(parts) {
				n = len(parts)
			}
			return strings.Join(parts[:len(parts)-n], "/"), nil
		}
		return ri.name, nil
	case "objectname":
		sha := ri.obj
		if deref {
			sha = ri.commit
		}
		if strings.HasPrefix(mod, "short") {
			n := 7
			if v, ok := strings.CutPrefix(mod, "short="); ok {
				n, _ = strconv.Atoi(v)
			}
			if n < len(sha) {
				sha = sha[:n]
			}
		}
		return sha, nil
	case "objecttype":
		if deref {
			return "commit", nil
		}
		return ri.typ, nil
	case "objectsize":
		sha := ri.obj
		if deref {
			sha = ri.commit
		}
		_, n, err := eng.ObjectInfo(sha)
		return strconv.FormatInt(n, 10), err
	case "HEAD":
		if ri.head {
			return "*", nil
		}
		return " ", nil
	case "upstream":
		u := upstream(ri)
		if mod == "short" {
			return shortRefName(u), nil
		}
		return u, nil
	case "push", "symref", "worktreepath", "refname:strip":
		return "", nil
	case "tree":
		if subj != nil {
			return subj.Tree, nil
		}
	case "parent":
		if subj != nil {
			return strings.Join(subj.Parents, " "), nil
		}
	case "numparent":
		if subj != nil {
			return strconv.Itoa(len(subj.Parents)), nil
		}
	case "subject", "contents", "body":
		if deref || ri.tag == nil {
			if subj == nil {
				return "", nil
			}
			switch {
			case name == "subject" || mod == "subject":
				return subj.Subject(), nil
			case name == "body" || mod == "body":
				return subj.Body() + "\n", nil
			}
			return subj.Message + "\n", nil
		}
		msg := strings.TrimRight(ri.tag.message, "\n")
		subject, body, _ := strings.Cut(msg, "\n\n")
		switch {
		case name == "subject" || mod == "subject":
			return strings.ReplaceAll(subject, "\n", " "), nil
		case name == "body" || mod == "body":
			if body == "" {
				return "", nil
			}
			return body + "\n", nil
		}
		return msg + "\n", nil
	}
	if v, ok := sigAtom("author", subjSig(subj, true), subj != nil); ok {
		return v, nil
	}
	if v, ok := sigAtom("committer", subjSig(subj, false), subj != nil); ok {
		return v, nil
	}
	if ri.tag != nil && !deref {
		if v, ok := sigAtom("tagger", ri.tag.tagger, ri.tag.hasTagger); ok {
			return v, nil
		}
	} else if v, ok := sigAtom("tagger", change.Signature{}, false); ok {
		return v, nil
	}
	if name == "creatordate" || name == "creator" {
		if ri.tag != nil && !deref && ri.tag.hasTagger {
			if name == "creator" {
				return ri.tag.tagger.Name + " <" + ri.tag.tagger.Email + ">", nil
			}
			return dateOf(ri.tag.tagger), nil
		}
		if subj != nil {
			if name == "creator" {
				return subj.Committer.Name + " <" + subj.Committer.Email + ">", nil
			}
			return dateOf(subj.Committer), nil
		}
		return "", nil
	}
	return "", fmt.Errorf("for-each-ref: unknown field name: %s", atom)
}

func subjSig(m *change.CommitMeta, author bool) change.Signature {
	if m == nil {
		return change.Signature{}
	}
	if author {
		return m.Author
	}
	return m.Committer
}

// cmdForEachRef: cairn for-each-ref [--format=<fmt>] [--sort=<key>] [--count=<n>] [<pattern>...]
func cmdForEachRef(args []string) error {
	g := newGitOpts("for-each-ref", "format= sort= count= shell perl python tcl merged= no-merged= contains= no-contains= points-at= color?")
	if err := g.parse(args); err != nil {
		return err
	}
	for _, o := range []string{"merged", "no-merged", "contains", "no-contains", "points-at"} {
		if g.on(o) {
			return fmt.Errorf("for-each-ref: --%s is not supported; filter with `cairn branch --%s` / `cairn tag --%s`", o, o, o)
		}
	}
	r, err := openRepo(g.repoDir(), defaultAuthor())
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	eng := r.Engine()
	patterns := append(append([]string{}, g.pos...), g.paths...)
	refs, err := loadRefInfos(eng, patterns)
	if err != nil {
		return mapErr(err)
	}
	allRefs, err := loadRefInfos(eng, nil)
	if err != nil {
		return mapErr(err)
	}
	have := map[string]bool{}
	for _, ri := range allRefs {
		have[ri.name] = true
	}
	upstream := func(ri *refInfo) string {
		if ri.kind != "head" {
			return ""
		}
		if u := "refs/remotes/origin/" + strings.TrimPrefix(ri.name, "refs/heads/"); have[u] {
			return u
		}
		return ""
	}
	now := time.Now()
	sorts := g.strs("sort")
	if len(sorts) == 0 {
		sorts = []string{"refname"}
	}
	var sortErr error
	key := func(ri *refInfo, k string) (string, int64, bool) {
		k = strings.TrimPrefix(k, "-")
		base, _, _ := strings.Cut(k, ":")
		switch strings.TrimPrefix(base, "*") {
		case "committerdate", "authordate", "creatordate", "taggerdate":
			sig := change.Signature{}
			switch strings.TrimPrefix(base, "*") {
			case "committerdate", "creatordate":
				sig = subjSig(ri.meta, false)
				if base == "committerdate" && ri.tag != nil {
					sig = change.Signature{}
				}
				if base == "creatordate" && ri.tag != nil && ri.tag.hasTagger {
					sig = ri.tag.tagger
				}
			case "authordate":
				sig = subjSig(ri.meta, true)
				if ri.tag != nil {
					sig = change.Signature{}
				}
			case "taggerdate":
				if ri.tag != nil {
					sig = ri.tag.tagger
				}
			}
			return "", sig.When.Unix(), true
		}
		v, err := refAtom(eng, ri, k, now, upstream)
		if err != nil && sortErr == nil {
			sortErr = err
		}
		return v, 0, false
	}
	sort.SliceStable(refs, func(i, j int) bool {
		for _, k := range sorts {
			desc := strings.HasPrefix(k, "-")
			if k == "version:refname" || k == "v:refname" || k == "-version:refname" || k == "-v:refname" {
				k = map[bool]string{true: "-refname", false: "refname"}[desc]
			}
			a, an, num := key(refs[i], k)
			b, bn, _ := key(refs[j], k)
			c := strings.Compare(a, b)
			if num {
				switch {
				case an < bn:
					c = -1
				case an > bn:
					c = 1
				default:
					c = 0
				}
			}
			if c == 0 {
				continue
			}
			return (c < 0) != desc
		}
		return false
	})
	if sortErr != nil {
		return sortErr
	}
	if g.has("count") {
		n, err := g.integer("count", 0)
		if err != nil || n < 0 {
			return errors.New("for-each-ref: invalid --count argument")
		}
		if n > 0 && n < len(refs) {
			refs = refs[:n]
		}
	}
	format := "%(objectname) %(objecttype)\t%(refname)"
	if g.has("format") {
		format = g.str("format")
	}
	out := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer out.Flush()
	atomRE := regexp.MustCompile(`%\(([^)]*)\)|%(%|[0-9a-fA-F]{2})`)
	for _, ri := range refs {
		var ferr error
		line := atomRE.ReplaceAllStringFunc(format, func(m string) string {
			if m == "%%" {
				return "%"
			}
			if m[1] != '(' {
				v, _ := strconv.ParseUint(m[1:], 16, 8)
				return string([]byte{byte(v)})
			}
			v, err := refAtom(eng, ri, m[2:len(m)-1], now, upstream)
			if err != nil && ferr == nil {
				ferr = err
			}
			return v
		})
		if ferr != nil {
			return ferr
		}
		_, _ = out.WriteString(line + "\n")
	}
	return nil
}

// ── name-rev ────────────────────────────────────────────────────────────────

// nameRevOptions are the knobs name-rev and describe --contains share.
type nameRevOptions struct {
	tagsOnly, nameOnly, noUndefined, always, peelTag bool
	refs                                             []string
}

// nameRevNames names each commit, "" when undefined.
func nameRevNames(eng *change.Engine, commits []string, o nameRevOptions) (map[string]string, error) {
	infos, err := loadRefInfos(eng, nil)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(infos, func(i, j int) bool { return infos[i].name < infos[j].name })
	var tips []change.NameRevTip
	for _, ri := range infos {
		if o.tagsOnly && !strings.HasPrefix(ri.name, "refs/tags/") {
			continue
		}
		if len(o.refs) > 0 {
			ok := false
			for _, p := range o.refs {
				if refMatches(ri.name, []string{p}) || (!strings.ContainsAny(p, "*?[") && strings.HasSuffix(ri.name, "/"+p)) {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		var name string
		switch {
		case o.tagsOnly && o.nameOnly:
			name = shortRefName(ri.name)
		case strings.HasPrefix(ri.name, "refs/heads/"):
			name = strings.TrimPrefix(ri.name, "refs/heads/")
		default:
			name = strings.TrimPrefix(ri.name, "refs/")
		}
		date := int64(0)
		if ri.meta != nil {
			date = ri.meta.Committer.When.Unix()
		}
		if ri.tag != nil && ri.tag.hasTagger {
			date = ri.tag.tagger.When.Unix()
		}
		tips = append(tips, change.NameRevTip{Name: name, Commit: ri.commit, TaggerDate: date,
			FromTag: strings.HasPrefix(ri.name, "refs/tags/"), Deref: ri.tag != nil && !o.peelTag})
	}
	return eng.NameRev(tips, commits)
}

// cmdNameRev: cairn name-rev [--tags] [--refs=<pat>] [--name-only] [--no-undefined] [--always] <rev>...
func cmdNameRev(args []string) error {
	g := newGitOpts("name-rev", "tags refs= exclude= all name-only no-undefined always stdin annotate-stdin peel-tag")
	if err := g.parse(args); err != nil {
		return err
	}
	if g.on("all") || g.on("stdin") || g.on("annotate-stdin") {
		return errors.New("name-rev: --all and --stdin are not supported; name revisions given as arguments")
	}
	if len(g.pos) == 0 {
		return errors.New("usage: cairn name-rev [--tags] [--name-only] [--no-undefined] [--always] <rev>...")
	}
	r, err := openRepo(g.repoDir(), defaultAuthor())
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	eng := r.Engine()
	if _, err := currentLine(r, g.repoDir()); err != nil {
		return err
	}
	commits := make([]string, len(g.pos))
	for i, rev := range g.pos {
		if commits[i], err = eng.ResolveCommit(rev); err != nil {
			return mapErr(err)
		}
	}
	opt := nameRevOptions{tagsOnly: g.on("tags"), nameOnly: g.on("name-only"), peelTag: g.on("peel-tag"), refs: g.strs("refs")}
	names, err := nameRevNames(eng, commits, opt)
	if err != nil {
		return mapErr(err)
	}
	for i, rev := range g.pos {
		name := names[commits[i]]
		if _, typ, err := eng.ResolveObject(rev); err == nil && typ == "tag" {
			// naming a tag object (not its commit) gives the tag's own name
			name = strings.TrimSuffix(name, "^0")
		}
		if name == "" {
			switch {
			case g.on("no-undefined"):
				return fmt.Errorf("name-rev: cannot describe '%s'", commits[i])
			case g.on("always"):
				name = commits[i][:7]
			default:
				name = "undefined"
			}
		}
		if g.on("name-only") {
			fmt.Println(name)
		} else {
			fmt.Printf("%s %s\n", rev, name)
		}
	}
	return nil
}

// cmdShowBranch: not implemented — a one-line pointer to the equivalent.
func cmdShowBranch(args []string) error {
	return errors.New("show-branch is not supported; `cairn log --graph --oneline <line>...` draws the same picture, `cairn merge-base <a> <b>` finds where they meet")
}
