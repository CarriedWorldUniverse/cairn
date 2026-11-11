package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
	cairnv1 "github.com/CarriedWorldUniverse/cwb-proto/gen/go/cwb/cairn/v1"
)

// cmdRemote is git's `remote`: bare lists names, -v shows the fetch/push URLs;
// add, remove|rm, rename, get-url, set-url, show and prune manage them (the
// latter in cmd_remote_verbs.go). `remote add --cairn` records the remote as a
// cairn server, enabling full-fidelity push.
func cmdRemote(args []string) error {
	// Sub-forms dispatch before flag parsing so their own flags (--cairn, -n)
	// don't collide with the list form.
	if len(args) > 0 {
		if args[0] == "add" {
			return cmdRemoteAdd(args[1:])
		}
		if handled, err := remoteSub(args[0], args[1:]); handled {
			return err
		}
	}
	fs := flag.NewFlagSet("remote", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	verbose := fs.Bool("v", false, "show the fetch and push URLs")
	fs.BoolVar(verbose, "verbose", false, "show the fetch and push URLs")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unknown remote subcommand %q (add, remove, rename, get-url, set-url, show, prune)", fs.Arg(0))
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	rems, err := r.Remotes()
	if err != nil {
		return mapErr(err)
	}
	for _, rem := range rems {
		if *verbose {
			u := redactURL(rem.URL)
			fmt.Printf("%s\t%s (fetch)\n%s\t%s (push)\n", rem.Name, u, rem.Name, u)
		} else {
			fmt.Println(rem.Name)
		}
	}
	return nil
}

func cmdRemoteAdd(args []string) error {
	fs := flag.NewFlagSet("remote add", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	cairn := fs.Bool("cairn", false, "register as a cairn remote — enables full-fidelity push (line tree + change-ids + open conflicts); default is plain git projection")
	fetchAfter := fs.Bool("f", false, "fetch the remote after adding it")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		return errors.New("usage: cairn remote add <name> <url> [--cairn] [-f]")
	}
	name := fs.Arg(0)
	url := absLocalURL(fs.Arg(1))
	kind := "git"
	if *cairn {
		kind = "cairn"
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if err := r.AddRemote(name, url, kind); err != nil {
		return mapErr(err)
	}
	fmt.Fprintf(os.Stderr, "cairn: added remote %s  %s  (%s)\n", name, redactURL(url), kind)
	if *fetchAfter {
		if err := r.Fetch(name); err != nil {
			return mapRemoteErr(err)
		}
	}
	return nil
}

// anyValueFlag is a boolean-style flag that also tolerates `--flag=value`
// (git's --force-with-lease[=<ref>[:<expect>]]): the value is ignored, the
// flag's presence is what cairn honours.
type anyValueFlag struct{ set bool }

func (a *anyValueFlag) String() string   { return fmt.Sprint(a.set) }
func (a *anyValueFlag) IsBoolFlag() bool { return true }
func (a *anyValueFlag) Set(string) error { a.set = true; return nil }

// cmdPush publishes the change-graph's branches + tags to a remote (default
// "origin"). --force overwrites a diverged remote branch. --reconcile (single-
// line push only) pulls + retries just that one line on divergence instead of
// surfacing the guided "diverged" error; it is rejected together with --all
// (which has its own all-lines auto-reconcile) or --force (which never pulls).
// A line with an open conflict is refused on a push to a plain git remote
// (conflict markers would otherwise ship as plain file content) unless
// --force is passed; a --cairn remote is exempt (conflicts-as-data travels
// with it by design).
//
// git's other spellings: `push <remote> <src>:<dst>` publishes a line under a
// different remote branch name; `push <remote> :<ref>` and --delete remove a
// remote branch or tag; --tags publishes only tags; --force-with-lease checks
// the remote against the tracking ref first; -n/--dry-run reports without
// sending; -u/--set-upstream is accepted (a line always tracks the same-named
// remote branch).
func cmdPush(args []string) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	force := fs.Bool("force", false, "force-overwrite a diverged remote branch")
	fs.BoolVar(force, "f", false, "force-overwrite a diverged remote branch")
	all := fs.Bool("all", false, "push all lines, not just the current one")
	reconcile := fs.Bool("reconcile", false, "single-line push: pull+retry just this line on a diverged remote")
	tagsOnly := fs.Bool("tags", false, "publish only the tags")
	del := fs.Bool("delete", false, "delete the named remote branch(es)/tag(s)")
	fs.BoolVar(del, "d", false, "delete the named remote branch(es)/tag(s)")
	dry := fs.Bool("dry-run", false, "report what would be pushed; send nothing")
	fs.BoolVar(dry, "n", false, "report what would be pushed; send nothing")
	quiet := fs.Bool("quiet", false, "no output on success")
	fs.BoolVar(quiet, "q", false, "no output on success")
	fs.Bool("set-upstream", false, "accepted: a line always tracks the same-named remote branch")
	fs.Bool("u", false, "accepted: a line always tracks the same-named remote branch")
	for _, n := range []string{"no-verify", "follow-tags", "atomic", "progress", "porcelain", "prune", "verbose", "v"} {
		fs.Bool(n, false, "accepted, no effect")
	}
	lease := &anyValueFlag{}
	fs.Var(lease, "force-with-lease", "force-push only if the remote branch still matches the last fetch")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if *reconcile && *all {
		return errors.New("--reconcile applies to a single-line push (not --all)")
	}
	if *reconcile && (*force || lease.set) {
		return errors.New("--reconcile and --force are contradictory")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	// push [remote] [branch...]: the first argument is the remote when it
	// names one (or when it is the only argument, as before).
	remote := "origin"
	specs := fs.Args()
	if len(specs) > 0 {
		isRemote := fs.NArg() == 1 && !*del && !strings.HasPrefix(specs[0], ":") && !strings.HasPrefix(specs[0], "+")
		if !isRemote {
			if _, gerr := r.GetRemote(specs[0]); gerr == nil {
				isRemote = true
			}
		}
		if *del && fs.NArg() == 1 {
			if _, gerr := r.GetRemote(specs[0]); gerr == nil {
				return errors.New("usage: cairn push --delete <remote> <branch|tag>...")
			}
		}
		if isRemote {
			remote, specs = specs[0], specs[1:]
		}
	}

	// Deletions: --delete names and `:ref` refspecs.
	var deletes, pushes []string
	for _, sp := range specs {
		switch {
		case *del:
			deletes = append(deletes, sp)
		case strings.HasPrefix(sp, ":"):
			deletes = append(deletes, sp[1:])
		default:
			pushes = append(pushes, sp)
		}
	}
	for _, name := range deletes {
		if name == "" {
			return errors.New("an empty ref name cannot be deleted")
		}
		if *dry {
			fmt.Printf("would delete %s on %s\n", name, remote)
			continue
		}
		ref, err := r.PushDelete(remote, name, lease.set)
		if err != nil {
			return mapRemoteErr(err)
		}
		if !*quiet {
			fmt.Printf("deleted %s on %s\n", ref, remote)
		}
	}
	if len(deletes) > 0 && len(pushes) == 0 {
		return nil
	}
	if *del && len(deletes) == 0 {
		return errors.New("--delete needs a branch or tag name")
	}

	// git's `push <remote> tag <name>` spelling.
	if len(pushes) == 2 && pushes[0] == "tag" {
		pushes = pushes[1:]
	}
	// A plain invocation (no git-only option) takes the original path — unless
	// a name is a tag (and no line), which only the tag path publishes.
	exotic := *tagsOnly || lease.set || *dry
	if len(pushes) == 1 && !strings.ContainsAny(pushes[0], ":+") && pushIsTag(r, pushes[0]) {
		exotic = true
	}
	for _, sp := range pushes {
		if strings.Contains(sp, ":") || strings.HasPrefix(sp, "+") {
			exotic = true
		}
	}
	if len(pushes) > 1 {
		exotic = true
	}

	branch := ""
	if len(pushes) == 1 {
		branch = pushes[0]
	}
	if branch == "" && !*all && !*tagsOnly && len(pushes) == 0 {
		if b, ok := r.CWDBranch(); ok {
			branch = b // inside a branch folder → push just that line
		}
	}

	if !exotic {
		return pushPlain(r, remote, branch, *force, *all, *reconcile, *quiet)
	}

	// Exotic path: one PushWith per refspec (or one for the whole thing).
	type target struct {
		src, dst string
		force    bool
	}
	var targets []target
	for _, sp := range pushes {
		t := target{force: *force}
		if strings.HasPrefix(sp, "+") {
			t.force, sp = true, sp[1:]
		}
		t.src, t.dst, _ = strings.Cut(sp, ":")
		if t.src == "HEAD" {
			if b, err := r.DefaultBranch(); err == nil {
				t.src = b
			}
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 && !*tagsOnly && branch != "" {
		targets = append(targets, target{src: branch, force: *force})
	}
	if *dry {
		var pairs [][2]string
		for _, t := range targets {
			pairs = append(pairs, [2]string{t.src, t.dst})
		}
		return pushDryRun(r, remote, pairs, *tagsOnly)
	}
	if len(targets) == 0 {
		if err := r.PushWith(worktree.PushOptions{Remote: remote, Force: *force, Lease: lease.set, TagsOnly: *tagsOnly}); err != nil {
			return mapRemoteErr(err)
		}
		if !*quiet {
			if *tagsOnly {
				fmt.Printf("pushed tags -> %s\n", remote)
			} else {
				fmt.Printf("pushed -> %s\n", remote)
			}
		}
		return nil
	}
	for _, t := range targets {
		src := t.src
		// A name that is a tag and not a line is a tag push: just that tag.
		if t.dst == "" && pushIsTag(r, src) {
			if err := r.PushWith(worktree.PushOptions{Remote: remote, Tag: src, Force: t.force}); err != nil {
				return mapRemoteErr(err)
			}
			if !*quiet {
				fmt.Printf("pushed tag %s -> %s\n", src, remote)
			}
			src = ""
		}
		if src == "" {
			continue
		}
		if err := r.PushWith(worktree.PushOptions{Remote: remote, Branch: src, Dst: t.dst, Force: t.force, Lease: lease.set}); err != nil {
			return mapRemoteErr(err)
		}
		if !*quiet {
			if t.dst != "" && t.dst != src {
				fmt.Printf("pushed %s -> %s:%s\n", src, remote, t.dst)
			} else {
				fmt.Printf("pushed %s -> %s\n", src, remote)
			}
		}
	}
	return nil
}

func lineExists(r *worktree.Repo, name string) bool {
	bs, err := r.Branches()
	if err != nil {
		return false
	}
	for _, b := range bs {
		if b.Name == name {
			return true
		}
	}
	return false
}

// pushDryRun reports what a push would send, from the state as of the last
// fetch or push (no network).
func pushDryRun(r *worktree.Repo, remote string, targets [][2]string, tagsOnly bool) error {
	if _, err := r.GetRemote(remote); err != nil {
		return mapErr(err)
	}
	states, err := r.RemoteStatesFor(remote)
	if err != nil {
		return mapErr(err)
	}
	if tagsOnly {
		fmt.Printf("would push all tags -> %s\n", remote)
		return nil
	}
	if len(targets) == 0 {
		bs, err := r.Branches()
		if err != nil {
			return mapErr(err)
		}
		for _, b := range bs {
			targets = append(targets, [2]string{b.Name, ""})
		}
	}
	for _, t := range targets {
		dst := t[1]
		if dst == "" {
			dst = t[0]
		}
		desc := "up to date"
		if st, ok := states[t[0]]; ok {
			switch st.Kind {
			case "unpushed":
				desc = "new branch"
			case "gone":
				desc = "new branch (the remote deleted it)"
			case "ahead":
				desc = fmt.Sprintf("%d commit(s) ahead", st.Ahead)
			case "behind":
				desc = fmt.Sprintf("remote is %d commit(s) ahead — would be rejected", st.Behind)
			case "diverged":
				desc = "diverged — would be rejected without --force"
			}
		}
		fmt.Printf("would push %s -> %s/%s (%s)\n", t[0], remote, dst, desc)
	}
	fmt.Printf("would also push all tags -> %s\n", remote)
	return nil
}

// pushPlain is the original push: the line you stand in (or the named one),
// or every line with --all / at the root.
func pushPlain(r *worktree.Repo, remote, branch string, force, all, reconcile, quiet bool) error {
	say := func(f string, a ...any) {
		if !quiet {
			fmt.Printf(f, a...)
		}
	}
	if branch != "" {
		if reconcile {
			// Opt-in single-line reconcile: pulls + retries just this line on
			// divergence (scoped, unlike Push's all-lines auto-reconcile).
			if err := r.PushBranchReconcile(remote, branch); err != nil {
				return mapRemoteErr(err)
			}
			say("pushed %s -> %s\n", branch, remote)
			return nil
		}
		// Single-line push: no auto-pull-retry (a diverged branch surfaces a
		// guided "diverged" error naming --reconcile/--pull/--force).
		if err := r.PushBranch(remote, branch, force); err != nil {
			return mapRemoteErr(err)
		}
		say("pushed %s -> %s\n", branch, remote)
		return nil
	}
	if reconcile {
		// No single line resolved: --all was NOT passed (that conflict is
		// already rejected by the caller) and cwd isn't inside a branch folder, so
		// there's nothing for --reconcile to scope to. Distinct from the
		// --reconcile+--all message: the operator never typed --all
		// here, so telling them "not --all" would be confusing.
		return errors.New("--reconcile needs a single line to push — pass a branch, or run it from inside an expressed branch folder")
	}
	// r.Push auto-reconciles a diverged remote (pull + 3-way merge, then retry
	// once) so "push just works". A successful auto-retry is intentionally silent
	// for v1: detecting whether the retry happened would need engine I/O the CLI
	// layer deliberately avoids. A merge that conflicts surfaces as a non-nil
	// "resolve, then push" error mapped to stderr below.
	if err := r.Push(remote, force); err != nil {
		return mapRemoteErr(err)
	}
	say("pushed -> %s\n", remote)
	return nil
}

// cmdFetch fetches a remote (default "origin") into tracking refs without
// touching local lines. `fetch <remote> <branch>...` fetches just those
// branches; --all fetches every remote. Fetch always prunes tracking refs
// the remote no longer has (so --prune/-p is accepted) and always takes tags
// (--tags accepted).
func cmdFetch(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	all := fs.Bool("all", false, "fetch every remote")
	quiet := fs.Bool("quiet", false, "no output")
	fs.BoolVar(quiet, "q", false, "no output")
	for _, n := range []string{"tags", "prune", "p", "force", "f", "verbose", "v", "no-tags", "progress"} {
		fs.Bool(n, false, "accepted, no effect (fetch always prunes and takes tags)")
	}
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if *all {
		rems, err := r.Remotes()
		if err != nil {
			return mapErr(err)
		}
		for _, rem := range rems {
			if err := r.Fetch(rem.Name); err != nil {
				return mapRemoteErr(fmt.Errorf("%s: %w", rem.Name, err))
			}
			if !*quiet {
				fmt.Printf("fetched <- %s\n", rem.Name)
			}
		}
		return nil
	}
	remote := "origin"
	if fs.NArg() > 0 {
		remote = fs.Arg(0)
	}
	if fs.NArg() > 1 {
		if err := r.FetchBranches(remote, fs.Args()[1:]); err != nil {
			return mapRemoteErr(err)
		}
	} else if err := r.Fetch(remote); err != nil {
		return mapRemoteErr(err)
	}
	if !*quiet {
		fmt.Printf("fetched <- %s\n", remote)
	}
	return nil
}

// cmdPull fetches a remote (default "origin") and reconciles each local line
// against its remote branch, re-materializing expressed folders. Each line's
// outcome is printed; conflicts are reported but non-fatal (exit 0) so the
// operator can resolve them and push.
//
// git's pull options: --rebase / --no-rebase / --ff / --no-ff are accepted and
// change nothing — cairn already replays each line's unpushed commits onto its
// moved parent and merges a diverged remote line with conflicts as data.
// --ff-only refuses (before touching anything) when any line has diverged from
// its remote branch. `pull <remote> <branch>` reconciles just that line.
func cmdPull(args []string) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	repo, author := repoFlags(fs)

	keepGone := fs.Bool("keep-gone", false, "do not abandon un-expressed lines whose branch is gone from the remote")
	ffOnly := fs.Bool("ff-only", false, "refuse when a line has diverged from its remote branch")
	quiet := fs.Bool("quiet", false, "print only conflicts and warnings")
	fs.BoolVar(quiet, "q", false, "print only conflicts and warnings")
	rebase := &anyValueFlag{}
	fs.Var(rebase, "rebase", "accepted: cairn already replays unpushed commits onto each line's moved parent")
	fs.Var(rebase, "r", "accepted: cairn already replays unpushed commits onto each line's moved parent")
	for _, n := range []string{"no-rebase", "ff", "no-ff", "autostash", "no-autostash", "tags", "prune", "p", "verbose", "v", "no-edit", "commit", "no-commit"} {
		fs.Bool(n, false, "accepted, no effect")
	}
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	remote := "origin"
	if fs.NArg() > 0 {
		remote = fs.Arg(0)
	}
	// pull re-materializes every expressed folder, so it MUST run the sync
	// prelude first (#182): opening without it meant the folders were rewritten
	// from their line tips with un-sealed edits never snapshotted — reverted,
	// and untracked new files deleted by the materialize sweep. Repo.Pull also
	// syncs defensively; this keeps the CLI's timing and progress consistent
	// with the other slow verbs.
	r, pullStarted, err := openRepoSyncedVerbose(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	defer reportElapsed("pull", pullStarted)
	r.SetKeepGone(*keepGone)
	if *ffOnly {
		if err := r.Fetch(remote); err != nil {
			return mapRemoteErr(err)
		}
		states, err := r.RemoteStatesFor(remote)
		if err != nil {
			return mapErr(err)
		}
		for name, st := range states {
			if st.Kind == "diverged" {
				return fmt.Errorf("not possible to fast-forward: line %q has diverged from %s/%s (+%d/-%d); drop --ff-only to reconcile it", name, remote, name, st.Ahead, st.Behind)
			}
		}
	}
	var sum change.PullSummary
	if fs.NArg() > 1 {
		for _, branch := range fs.Args()[1:] {
			s, err := r.PullBranch(remote, branch)
			if err != nil {
				return mapRemoteErr(err)
			}
			sum.Lines = append(sum.Lines, s.Lines...)
			sum.Rebased = append(sum.Rebased, s.Rebased...)
		}
	} else {
		sum, err = r.Pull(remote)
		if err != nil {
			return mapRemoteErr(err)
		}
	}
	anyConflicts := false
	for _, lr := range sum.Lines {
		if lr.Conflicts > 0 {
			anyConflicts = true
			fmt.Printf("%s: %s (%d conflicts)\n", lr.Line, lr.Status, lr.Conflicts)
		} else if !*quiet {
			fmt.Printf("%s: %s\n", lr.Line, lr.Status)
		}
	}
	if anyConflicts {
		fmt.Fprintln(os.Stderr, "cairn: resolve the conflicts above, then push")
	}
	for _, rb := range sum.Rebased {
		printRebase(rb)
	}
	if len(sum.Pruned) > 0 {
		fmt.Fprintf(os.Stderr, "cairn: pruned %d line(s) whose branch is gone from %s: %s (cairn undo restores)\n", len(sum.Pruned), remote, strings.Join(sum.Pruned, ", "))
	}
	if len(sum.KeptGone) > 0 {
		fmt.Fprintf(os.Stderr, "cairn: kept %d expressed line(s) whose branch is gone from %s: %s — unexpress and abandon them, or push them back\n", len(sum.KeptGone), remote, strings.Join(sum.KeptGone, ", "))
		return errConflicts
	}
	return nil
}

// printPull prints a pull in the one-line list/view format.
func printPull(p *cairnv1.Pull) {
	fmt.Printf("%s\t%s\t%s -> %s\t%s\t%s\n", p.GetId(), p.GetState(), p.GetSource(), p.GetTarget(), p.GetTitle(), p.GetLedgerIssueKey())
}

// absLocalURL canonicalizes a remote given as a LOCAL path (relative or not) to
// an absolute one. A relative path stored as typed resolves against whatever
// directory a later fetch/push runs from — inside copy/ it points nowhere.
// URLs (scheme://, scp-style host:path) and paths that do not exist are left
// untouched.
func absLocalURL(u string) string {
	if u == "" || strings.Contains(u, "://") {
		return u
	}
	if i := strings.IndexByte(u, ':'); i > 0 && !strings.ContainsRune(u[:i], '/') {
		return u // scp-style host:path
	}
	if _, err := os.Stat(u); err != nil {
		return u
	}
	abs, err := filepath.Abs(u)
	if err != nil {
		return u
	}
	return abs
}

// pushIsTag reports whether name is a tag and not a line (a line of the same
// name wins, as a branch does in git's ambiguity rules for a bare push).
func pushIsTag(r *worktree.Repo, name string) bool {
	if r.IsExpressed(name) || lineExists(r, name) {
		return false
	}
	infos, err := r.TagInfos()
	if err != nil {
		return false
	}
	for _, ti := range infos {
		if ti.Name == name {
			return true
		}
	}
	return false
}
