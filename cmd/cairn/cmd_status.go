package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

// cmdStatus reports a line's state.
//
//	cairn status [branch]                      the long report (default)
//	cairn status -s|--short [-b] [-z] [branch] [-- <path>...]
//	cairn status --porcelain[=v1] [-b] [-z] ...
//
// The short and porcelain formats are git's `XY path` lines. cairn has no
// index, so there is exactly one comparison — the working change against the
// line's sealed tip — and it is reported in the X column, as git reports
// changes that are already staged, because that is what they are: everything
// listed is what the next `cairn commit` seals. The mapping:
//
//	M␠  modified         A␠  new file         D␠  deleted
//	UU  unresolved conflict (resolve with `cairn resolve`)
//	!!  ignored (only with --ignored)
//
// `??` never appears: a new file that is not ignored is already part of the
// working change (it shows as `A`). Renames are reported as a D and an A, and
// paths are relative to the line's root in both formats.
func cmdStatus(args []string) error {
	before, pathsAfter, _ := splitDashDash(args)
	before, porcelain, porcelainVer := takeOptionalValueFlag(before, "porcelain", "")
	// -u[mode] / --untracked-files[=mode] and --ignored[=mode]: accepted. Every
	// non-ignored file is already tracked, so there is no untracked set to
	// tune; any --ignored mode lists ignored paths.
	before, _, _ = takeOptionalValueFlag(before, "untracked-files", "u")
	before, ignored, ignoredMode := takeOptionalValueFlag(before, "ignored", "")
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	var short, showBranch, nul bool
	fs.BoolVar(&short, "s", false, "short format")
	fs.BoolVar(&short, "short", false, "short format")
	fs.BoolVar(&showBranch, "b", false, "show the branch header in the short format")
	fs.BoolVar(&showBranch, "branch", false, "show the branch header in the short format")
	fs.BoolVar(&nul, "z", false, "terminate entries with NUL instead of newline (implies --porcelain)")
	fs.Bool("long", false, "long format (the default)")
	for _, name := range []string{"ahead-behind", "no-ahead-behind", "renames", "no-renames", "column", "no-column"} {
		fs.Bool(name, false, "accepted for git compatibility (no-op)")
	}
	if err := parseGitArgs(fs, before); err != nil {
		return err
	}
	if porcelain && porcelainVer != "" && porcelainVer != "v1" && porcelainVer != "1" {
		return fmt.Errorf("status --porcelain=%s is not supported; cairn emits porcelain v1", porcelainVer)
	}
	if ignored && ignoredMode == "no" {
		ignored = false
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	machine := short || porcelain || nul
	pos := fs.Args()
	branch := ""
	switch {
	case len(pos) > 0 && (!machine || r.IsExpressed(pos[0])):
		// The long report keeps its one-positional contract: the branch.
		branch, pos = pos[0], pos[1:]
	default:
		// No branch given: default to the structural root's name, not the literal
		// "main" — after a clone of a master-default repo the root is "master".
		branch, err = r.DefaultBranch()
		if err != nil {
			return mapErr(err)
		}
	}
	specs := append(pos, pathsAfter...)
	if !machine && len(specs) > 0 {
		return errors.New("usage: cairn status [branch]  |  cairn status --short [branch] [-- <path>...]")
	}
	st, err := r.Status(branch)
	if err != nil {
		return mapErr(err)
	}
	if machine {
		return printShortStatus(r, st, specs, showBranch, nul, ignored)
	}
	fmt.Printf("branch:    %s\n", st.Branch)
	fmt.Printf("lineage:   %s\n", strings.Join(st.Lineage, " → "))
	fmt.Printf("ahead:     %d\n", st.Ahead)
	if st.Remote != "" {
		fmt.Printf("remote:    %s\n", st.Remote)
	}
	if st.Rebase != "" {
		fmt.Printf("rebase:    %s\n", st.Rebase)
	}
	fmt.Printf("conflicts: %s\n", strings.Join(st.Conflicts, ", "))
	fmt.Printf("expressed: %s\n", strings.Join(st.Expressed, ", "))
	if len(st.Modified)+len(st.Added)+len(st.Deleted) > 0 {
		fmt.Println("changes:")
		for _, p := range st.Modified {
			fmt.Printf("  M %s\n", p)
		}
		for _, p := range st.Added {
			fmt.Printf("  A %s\n", p)
		}
		for _, p := range st.Deleted {
			fmt.Printf("  D %s\n", p)
		}
	}
	// Surface unreadable-untracked skips structurally on STDOUT (#130); see
	// the identical block in cmdCommit.
	printSkippedUnreadable(st.SkippedUnreadable)
	return nil
}

// printShortStatus writes git's short / porcelain-v1 status for st.
func printShortStatus(r *worktree.Repo, st worktree.StatusInfo, specs []string, showBranch, nul, ignored bool) error {
	var err error
	if len(specs) > 0 {
		if specs, err = treePathspecs(r, st.Branch, specs); err != nil {
			return err
		}
	}
	want := func(p string) bool {
		if len(specs) == 0 {
			return true
		}
		for _, s := range specs {
			if worktree.MatchPathspec(p, s) {
				return true
			}
		}
		return false
	}
	codes := map[string]string{}
	for _, p := range st.Modified {
		codes[p] = "M "
	}
	for _, p := range st.Added {
		codes[p] = "A "
	}
	for _, p := range st.Deleted {
		codes[p] = "D "
	}
	for _, p := range st.Conflicts {
		codes[p] = "UU"
	}
	paths := make([]string, 0, len(codes))
	for p := range codes {
		if want(p) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	end := "\n"
	if nul {
		end = "\x00"
	}
	name := func(p string) string {
		if nul {
			return p // -z output is verbatim, never quoted
		}
		return quoteStatusPath(p)
	}
	out := os.Stdout
	if showBranch {
		fmt.Fprint(out, statusBranchHeader(r, st.Branch), end)
	}
	for _, p := range paths {
		fmt.Fprint(out, codes[p], " ", name(p), end)
	}
	if ignored {
		ign, ierr := r.IgnoredPaths(st.Branch, true)
		if ierr != nil {
			return mapErr(ierr)
		}
		for _, p := range ign {
			// A path listed above is tracked work, never also "ignored".
			if _, listed := codes[strings.TrimSuffix(p, "/")]; listed {
				continue
			}
			if want(strings.TrimSuffix(p, "/")) {
				fmt.Fprint(out, "!! ", name(p), end)
			}
		}
	}
	return nil
}

// statusBranchHeader is the `## line...remote/line [ahead N, behind M]` line.
func statusBranchHeader(r *worktree.Repo, branch string) string {
	remote, rs, ok, err := r.RemoteStateOf(branch)
	if err != nil || !ok || rs.Kind == "unpushed" {
		return "## " + branch
	}
	h := "## " + branch + "..." + remote + "/" + branch
	switch rs.Kind {
	case "ahead":
		h += fmt.Sprintf(" [ahead %d]", rs.Ahead)
	case "behind":
		h += fmt.Sprintf(" [behind %d]", rs.Behind)
	case "diverged":
		h += fmt.Sprintf(" [ahead %d, behind %d]", rs.Ahead, rs.Behind)
	case "gone":
		h += " [gone]"
	}
	return h
}

// quoteStatusPath quotes a path the way `git status --short` does: a name
// holding a space, a quote, a backslash, a control character or a non-ASCII
// byte is written as a C string literal with octal escapes; anything else is
// printed as is.
func quoteStatusPath(p string) string {
	need := false
	for i := 0; i < len(p); i++ {
		if c := p[i]; c <= ' ' || c == '"' || c == '\\' || c >= 0x7f {
			need = true
			break
		}
	}
	if !need {
		return p
	}
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < ' ' || c >= 0x7f:
			fmt.Fprintf(&b, `\%03o`, c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
