// Command cairn is the working-copy CLI: a thin dispatcher over the
// internal/worktree Repo. Each subcommand opens (or bootstraps) a repo and
// drives one Repo method — expressing branches as folders on disk, committing
// their contents, folding/abandoning lines, and inspecting the line tree.
//
// Usage:
//
//	cairn <subcommand> [flags] [args]
//
// Subcommands operating on an existing repo accept --repo (default ".") and
// --author (default $CAIRN_AUTHOR, else $USER, else "cairn").
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/prclient"
	"github.com/CarriedWorldUniverse/cairn/internal/release"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// buildVersion is the release version of this binary, injected at link time by
// GoReleaser (-ldflags "-X main.buildVersion=..."). It defaults to "dev" for a
// plain `go build`/`go run`. Reported by the top-level `--version` flag — this
// is distinct from the `version` subcommand, which derives the repo's semver.
var buildVersion = "dev"

// Publisher/probe seams, overridable in tests.
var newPublisher = func() release.Publisher { return release.ExecPublisher{} }
var newProbe = func() release.RegistryProbe { return release.ExecProbe{} }

// errConflicts is returned by cmdCommit and cmdPull when conflicts were
// recorded. main() maps this to os.Exit(2) so that `commit && push` is safe
// in scripts (exit 2 ≠ success, but distinct from a hard error at exit 1).
// The stderr notice is printed by the cmd function; main must NOT print it again.
var errConflicts = errors.New("completed with conflicts")

func main() {
	args, gerr := peelGlobals(os.Args[1:]) // git's -C / -c / --git-dir before the subcommand
	if gerr != nil {
		fmt.Fprintln(os.Stderr, "cairn:", gerr)
		os.Exit(128)
	}
	args, pmode := pagerGlobals(args)
	stopProfile := startProfile()
	stopPager := startPager(args, pmode, stdoutIsTTY())
	err := run(args)
	stopPager()
	stopProfile()
	if err != nil {
		if errors.Is(err, errConflicts) {
			os.Exit(2)
		}
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		if errors.Is(err, errSilent) {
			os.Exit(1)
		}
		var ec exitCodeError
		if errors.As(err, &ec) {
			os.Exit(ec.code)
		}
		// A status-only answer (diff --quiet, grep with no match, cat-file -e):
		// the exit code IS the result, so nothing is printed.
		if code, ok := exitCodeOf(err); ok {
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, "cairn:", err)
		if isFatal(err) {
			os.Exit(128)
		}
		os.Exit(1)
	}
}

const usage = `cairn — cairn working-copy CLI

usage: cairn [-C <dir>] [-c <key>=<value>] [--no-pager|-P] [--paginate|-p] <subcommand> [flags] [args]
       (on a terminal, log/show/diff/grep/blame/shortlog/listings go through $CAIRN_PAGER, $GIT_PAGER, core.pager, $PAGER or less)

subcommands:
  init [-b <name>] [-q] [dir]   bootstrap a repo in an EMPTY dir (expresses the root line: main, or -b / init.defaultBranch; --bare is refused)
  clone [-b <line>] [-o <name>] [-q] <url> [dir]  import a remote repo and express its default branch (-b also expresses <line>; --depth/--single-branch are refused)
  express <branch>          materialize a branch folder (--from <parent>)
  unexpress <branch>        remove a branch folder (--force to discard un-sealed work)
  commit [branch] [-m msg]  seal the working change (stamps msg, starts a fresh change; -F file, --author "N <e>", --date; nothing to commit = error unless --allow-empty; --fixup/--squash=<c>)
  commit [branch] -m msg -- <path>...  seal ONLY those paths; the rest stays in the working change (cairn's staging area)
  commit [branch] --amend [-m msg]     fold the working change into the previous sealed commit (history edit)
  fold <branch>             fold a branch into its parent (--force to discard un-sealed work; --no-ff, --ff-only, -m <msg>)
  reparent <branch> <parent>  set a branch's parent line (fix stacked topology after a git import)
  reparent --infer [--dry-run]  re-derive every line's parent from topology, as a fresh clone would
  abandon <branch>          discard a branch's line (--force to discard un-sealed work)
  status [branch]           report a branch's state — the working change vs its parent (default: root)
  status -s|--porcelain [-b] [-z]  git's short/porcelain-v1 format (changes in the X column; no index)
  diff [branch] [-- <path>...]  show the working change vs its parent as a git patch (optionally one file/dir)
  diff <a> <b> | <a>..<b> | <a>...<b> | <rev>  commit-vs-commit, since-fork, or a commit vs the working tree
                                options as git: --stat --numstat --name-only --name-status -U<n> -R -w --exit-code --quiet (--classic: cairn's old format)
  tree [--fetch] [--gone]       print the line tree with each line's remote state (--fetch refreshes it; --gone lists lines whose branch is gone; --flat for scripts)
  ls                            list expressed branches
  resolve <branch> <path>       resolve a conflict on a branch — takes the file's on-disk content, or its ABSENCE to resolve as a deletion; refuses lingering <<<<<<< markers (--force to accept)
  remote [-v]                   list configured remotes (-v: fetch/push URLs)
  remote add <name> <url>       register a remote (--cairn for a cairn remote, -f to fetch)
  remote remove|rm <name>       delete a remote and its tracking refs
  remote rename <old> <new>     rename a remote (tracking refs move with it)
  remote get-url <name>         print a remote's URL
  remote set-url <name> <url>   re-point a remote
  remote show [-n] <name>       URL, HEAD and branches of a remote (-n: no network)
  remote prune [-n] <name>      drop tracking refs for branches the remote deleted
  push [remote] [refspec...]    publish all lines + tags, or <branch> / <src>:<dst> (default origin); --force, --force-with-lease, --tags, -n/--dry-run, -q, --all; -u accepted
  push --delete <remote> <name> / push <remote> :<ref>   delete a remote branch or tag
  fetch [remote] [branch...]    fetch a remote into tracking refs (default origin); --all, -q; --tags/--prune accepted
  pull [remote] [branch]        fetch + reconcile each line (default origin); --ff-only refuses diverged lines; --rebase/--no-rebase/--ff accepted, -q
  pr open <source> <target> -m <title>  open a pull request against cairn-server (idempotent)
  pr list                       list a repo's pull requests (--state open|merged|all)
  pr view <id>                  show one pull request
  pr diff <id>                  print the pull request's unified diff (--remote)
  pr merge <id>                 fast-forward-merge an open pull request
  blame <path> [branch]         show per-line author/date/commit (-L a,b; or: blame <rev> -- <path>)
  log [<range>] [-n N] [-- <path>...]  show commit history, as git: --oneline --stat -p --graph --format= --author= --grep= --since= …
                                (--working: list the open working change first; --classic [line] [-n N]: cairn's compact view)
  show [<rev>[:<path>]...]      show a commit (message + patch; --stat, --name-only, -s, --format=), a file's content, a tree (--classic: old format)
  grep <pattern> [<rev>] [-- <path>...]  search file contents (-n -i -l -L -c -w -v -E -F -e; exit 1 when nothing matches)
  ls-files | ls-tree | cat-file | rev-parse | rev-list | shortlog | describe | format-patch
                                read-only git plumbing, same spelling and output as git
  merge-base | symbolic-ref | hash-object | for-each-ref | name-rev
                                more git plumbing (merge-base [--all|--is-ancestor], for-each-ref --format --sort, name-rev [--tags])
  undo                          revert the last operation
  oplog                         show the operation history
  setup                         set your commit identity (name/email), stored globally
  login <host>                  store an access token for a host (stdin: echo $TOK | cairn login github.com)
  logout <host>                 remove a host's stored credential
  auth                          list hosts with stored credentials (never the tokens)
  config [--global|--local] <key> [value]  get/set a config value (any section.key; alias.<name> defines a command alias, '!cmd' runs the shell)
  config -l | --get <key> | --unset <key>  list the merged config / print (exit 1 if unset) / remove a key (--show-origin)
  tag [-l [pattern]] [-n[N]] [--contains <rev>] [--points-at <rev>] [--sort=<key>]  list tags (sorted by refname; keys: refname, -creatordate, version:refname)
  tag [-a] [-m <msg>|-F <file>] [-f] <name> [<rev>]  tag a commit (a line name = its tip; default: current line); -a/-m/-F write an annotated tag
  tag -d <name>...              delete tags
  branch [-v|-vv] [-a|-r] [--list <pattern>] [--merged|--no-merged|--contains <rev>]  list lines (* = the one you stand in, + = other expressed lines)
  branch <name> [<start>]       create a line (= express <name> [--from <start>])
  branch -d|-D <name>...        delete a line (-d refuses unfolded, unpushed work; -D forces) (= abandon)
  branch -m|-M [<old>] <new>    rename a line and its folder (child lines follow; undoable); -c copies
  branch --show-current         print the current line
  reflog [show] [<line>] [-n N] operation history of a line, git-style: <sha> <line>@{N}: <op>
  gc [--prune=now] [-q]         repack objects, vacuum the catalogue, prune stale caches (--aggressive accepted)
  fsck [-v]                     verify object connectivity from every line tip/tag and catalogue consistency
  count-objects [-v] [-H]       count loose and packed objects
  archive [--format=tar|tar.gz|zip] [-o <file>] [--prefix=<p>/] <rev> [<path>...]  write a commit's tree as an archive
  check-ignore [-v] [-n] <path>...  report which paths the line's .gitignore/.cairnignore rules ignore (exit 1 if none)
  help [<verb>...]              this table, or one verb's usage and flags (also: <verb> -h)
  private <path> [--shape-only] withhold a file/folder from every push (omit by default)
  private ls                    list withheld paths
  embargo <commit>              hold a commit (+ all after it) out of the public projection
  embargo ls                    list embargoed commits
  disclose <path|commit>        stop withholding a path, or lift an embargo (make public)
  version [--target eco] [--release]  print the derived version (stdout only, CI-safe)
  version bump <level>          record explicit bump intent (major|minor|patch)
  release --target eco          cut a clean release: tag + stamp + publish (--dry-run)
  update                        replace this binary with the latest release (--check to only report, --force to reinstall)
  stash [push] [-m msg] [branch] [-- <path>...]  shelve the working change (or only some paths)
  stash pop|apply [branch] [id]  restore a stash onto branch (default: most recent; apply keeps it)
  stash list | show [-p] [id] | drop [id] | clear   inspect / discard stashes (id or stash@{N})
  stash branch <name> [id]  express a new line and pop a stash onto it
  restore [--source=<rev>] <path>...  discard working-change edits to paths (git restore / checkout -- <path>)
  reset --hard              discard the line's whole working change
  reset [--soft|--hard] <rev>  move the line's sealed tip back to <rev> (history edit: not over pushed root commits or a child line's base)
  revert <commit>... [-n] [-m N]  add a commit that takes <commit> back out (--continue/--abort)
  rm [-r] [-f] [-n] <path>...   remove files from the line (and the folder)
  mv [-f] [-n] <src>... <dst>   rename files/directories in the line
  clean -fdx [-n] [<path>...]   remove IGNORED files (build junk); new source is never touched
  apply [--check] [-R] [--stat] [-p<n>] <patch>|-  apply a unified diff to the working change
  am [<mbox>...|-]              apply git format-patch output as sealed commits
  add <anything>                no-op (there is no staging area), so 'add -A && commit' scripts work
  reword <commit> <message> change the message of a sealed commit (history edit)
  squash <commit>           fold a sealed commit into its parent (history edit)
  drop <commit>             remove a sealed commit from its line (history edit)
  reauthor --old-email <glob> --name <n> --email <e> [--dry-run]  bulk-retag commit identity (whole repo)
  cherry-pick <commit|A..B>... [branch]  apply commits from another line onto your branch (-n, -x, --continue/--abort)
  bisect start --good <c> --bad <c> [branch]  begin a bisect (materializes the midpoint)
  bisect good | bad             mark the current commit; materializes the next midpoint
  bisect skip                   step over an untestable midpoint
  bisect status                 show the active bisect session
  bisect reset                  end the bisect; restore the working folder
  bisect run [--repo d] -- <cmd>   automate: 0=good, 125=skip, else=bad

git spellings (aliases for the cairn verb above):
  merge <branch>                = fold <branch> (a line folds into ITS PARENT; --no-ff/--ff-only/-m; --squash/--abort/--continue are translated)
  branch <name> [<start>]       = express <name> [--from <start>]
  checkout -b <name>            = express <name>
  switch -c <name>              = express <name>
  checkout [<rev>] -- <path>... = restore [--source=<rev>] <path>...
(other git verbs — checkout <branch>, rebase, worktree — have no cairn
equivalent; running one prints the translation.)

revisions: a commit can be named by full/short hash, tag, line (its sealed tip), origin/<branch>,
HEAD or @ (the sealed tip of the line you stand in), with ~N ^ ^N suffixes; <rev>:<path> names a file.

config keys: user.name, user.email, autosync, pr.org, pr.repo-slug, color.ui (and any section.key: alias.*, core.editor, core.pager, pager.<cmd>, init.defaultBranch, pull.rebase, push.default ...)
common flags (repo subcommands): --repo <dir> (default .), --author <name>

` + agentHelpDiscovery

// run dispatches a subcommand. args is os.Args[1:].
func run(args []string) error {
	if len(args) == 0 {
		fmt.Println(usage)
		return errors.New("no subcommand")
	}
	if isGlobalOpt(args[0]) { // git's -C/-c/--git-dir ahead of the subcommand (main peels them first)
		peeled, gerr := peelGlobals(args)
		if gerr != nil {
			return gerr
		}
		args, _ = pagerGlobals(peeled)
		if len(args) == 0 {
			fmt.Println(usage)
			return errors.New("no subcommand")
		}
	}
	sub, rest := args[0], args[1:]
	if sub == "--version" || sub == "-v" {
		fmt.Println("cairn", buildVersion)
		return nil
	}
	// `<verb> -h` / `<verb> --help` print that verb's usage section.
	if wantsHelp(rest) && sub != "help" && !(sub == "grep" && rest[0] == "-h") && printVerbHelp(sub) {
		return flag.ErrHelp // main maps this to exit 0
	}
	switch sub {
	case "help":
		return cmdHelp(rest)
	case "-h", "--help":
		fmt.Println(usage)
		return nil
	case "init":
		return cmdInit(rest)
	case "clone":
		return cmdClone(rest)
	case "express":
		return cmdExpress(rest)
	case "unexpress":
		return cmdUnexpress(rest)
	case "commit":
		return cmdCommit(rest)
	case "fold":
		return cmdFold(rest)
	case "reparent":
		return cmdReparent(rest)
	case "abandon":
		return cmdAbandon(rest)
	case "status":
		return cmdStatus(rest)
	case "diff":
		return cmdDiff(rest)
	case "blame":
		return cmdBlame(rest)
	case "log":
		return cmdLog(rest)
	case "show":
		return cmdShow(rest)
	case "grep":
		return cmdGrep(rest)
	case "ls-files":
		return cmdLsFiles(rest)
	case "ls-tree":
		return cmdLsTree(rest)
	case "cat-file":
		return cmdCatFile(rest)
	case "rev-parse":
		return cmdRevParse(rest)
	case "rev-list":
		return cmdRevList(rest)
	case "merge-base":
		return cmdMergeBase(rest)
	case "symbolic-ref":
		return cmdSymbolicRef(rest)
	case "hash-object":
		return cmdHashObject(rest)
	case "for-each-ref":
		return cmdForEachRef(rest)
	case "name-rev":
		return cmdNameRev(rest)
	case "show-branch":
		return cmdShowBranch(rest)
	case "shortlog":
		return cmdShortlog(rest)
	case "describe":
		return cmdDescribe(rest)
	case "format-patch":
		return cmdFormatPatch(rest)
	case "undo":
		return cmdUndo(rest)
	case "oplog":
		return cmdOplog(rest)
	case "tree":
		return cmdTree(rest)
	case "ls":
		return cmdLs(rest)
	case "resolve":
		return cmdResolve(rest)
	case "remote":
		return cmdRemote(rest)
	case "push":
		return cmdPush(rest)
	case "fetch":
		return cmdFetch(rest)
	case "pull":
		return cmdPull(rest)
	case "pr":
		return cmdPR(rest)
	case "config":
		return cmdConfig(rest)
	case "setup":
		return cmdSetup(rest)
	case "login":
		return cmdLogin(rest)
	case "logout":
		return cmdLogout(rest)
	case "auth":
		return cmdAuth(rest)
	case "tag":
		return cmdTag(rest)
	case "private":
		return cmdPrivate(rest)
	case "embargo":
		return cmdEmbargo(rest)
	case "disclose":
		return cmdDisclose(rest)
	case "version":
		return cmdVersion(rest)
	case "release":
		return cmdRelease(rest)
	case "update":
		return cmdUpdate(rest)
	case "stash":
		return cmdStash(rest)
	case "reword":
		return cmdReword(rest)
	case "squash":
		return cmdSquash(rest)
	case "drop":
		return cmdDrop(rest)
	case "cherry-pick":
		return cmdCherryPick(rest)
	case "reauthor":
		return cmdReauthor(rest)
	case "bisect":
		return cmdBisect(rest)
	case "reflog":
		return cmdReflog(rest)
	case "gc":
		return cmdGC(rest)
	case "fsck":
		return cmdFsck(rest)
	case "count-objects":
		return cmdCountObjects(rest)
	case "archive":
		return cmdArchive(rest)
	case "check-ignore":
		return cmdCheckIgnore(rest)
	// working-copy / mutation verbs (git parity) — cmd_restore.go, cmd_files.go,
	// cmd_apply.go.
	case "restore":
		return cmdRestore(rest)
	case "reset":
		return cmdReset(rest)
	case "revert":
		return cmdRevert(rest)
	case "add", "stage":
		return cmdAdd(rest)
	case "rm":
		return cmdRm(rest)
	case "mv":
		return cmdMv(rest)
	case "clean":
		return cmdClean(rest)
	case "apply":
		return cmdApply(rest)
	case "am":
		return cmdAm(rest)
	default:
		// A git spelling gets either its cairn alias or a one-line correction
		// (#139) — printed INSTEAD of the usage wall, so the correction is what
		// the reader actually sees at the moment the reflex misfires.
		if handled, err := runGitShaped(sub, rest); handled {
			return err
		}
		// alias.<name> from config: reached only for a non-built-in word.
		if handled, err := runAlias(sub, rest); handled {
			return err
		}
		return fmt.Errorf("'%s' is not a cairn command. See 'cairn help'.", sub)
	}
}

// stdinReader is a single buffered reader over stdin shared across prompts — a
// fresh bufio.Reader per call would discard input already buffered past the
// first newline.
var stdinReader = bufio.NewReader(os.Stdin)

func firstNonEmptyStr(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// openRepo opens a Repo from already-parsed flag values. It refuses to open
// (and thus silently bootstrap) a directory that has no .cairn sub-directory;
// the caller should run `cairn init` first.
// discoverRepoRoot walks up from start to the nearest ancestor directory that
// contains a .cairn directory, mirroring how git locates .git. This lets cairn
// run from any subfolder of a repo (e.g. inside an expressed branch folder), not
// only the root. Returns the repo root, or an error if no .cairn is found up to
// the filesystem root.
func discoverRepoRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("not a cairn repo: %w", err)
	}
	cand := ""
	for {
		if cand == "" {
			if isRepoDir(dir) {
				cand = dir
			}
		} else if isRepoDir(dir) && insideExpressedFolder(dir, cand) {
			// cand is not a repo: it is content inside one of this outer
			// repo's line folders (e.g. a ".cairn" a clone or patch planted).
			cand = dir
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached the filesystem root
			if cand != "" {
				return cand, nil
			}
			return "", fmt.Errorf("not a cairn repo (no .cairn in %q or any parent directory; run 'cairn init')", start)
		}
		dir = parent
	}
}

// isRepoDir reports whether dir is a cairn repo root: its .cairn must be a
// real directory (not a symlink) holding a regular-file catalogue.
func isRepoDir(dir string) bool {
	meta := filepath.Join(dir, ".cairn")
	if fi, err := os.Lstat(meta); err != nil || !fi.IsDir() {
		return false
	}
	fi, err := os.Lstat(filepath.Join(meta, "cairn.db"))
	return err == nil && fi.Mode().IsRegular()
}

// insideExpressedFolder reports whether inner lies within one of outer's
// expressed line folders (per outer's wc.json). An inner ".cairn" found there
// is repo CONTENT, not a repo.
func insideExpressedFolder(outer, inner string) bool {
	rel, err := filepath.Rel(outer, inner)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false
	}
	first := strings.Split(filepath.ToSlash(rel), "/")[0]
	st, err := worktree.LoadState(filepath.Join(outer, ".cairn", "wc.json"))
	if err != nil {
		return false
	}
	for _, e := range st.Expressed {
		if strings.EqualFold(strings.TrimRight(e.Path, ". "), strings.TrimRight(first, ". ")) {
			return true
		}
	}
	return false
}

// repoConfigTrusted reports whether exec-capable settings (core.pager,
// pager.*, '!' aliases) may be read from the repo at root: its catalogue must
// be a real file owned by the current user, like git's safe.directory rule.
// User-level config is always trusted.
func repoConfigTrusted(root string) bool {
	fi, err := os.Lstat(filepath.Join(root, ".cairn", "cairn.db"))
	return err == nil && fi.Mode().IsRegular() && ownedByCurrentUser(fi)
}

func openRepo(repo, author string) (*worktree.Repo, error) {
	root, err := discoverRepoRoot(repo)
	if err != nil {
		return nil, err
	}
	r, err := worktree.Open(root, author)
	if err != nil {
		return nil, err
	}
	// If the user is standing inside an expressed branch folder, record it as the
	// branch hint so commands that omit a branch act on it (like git's current
	// branch). repo (default ".") is where they stand; map its top folder under
	// the root back to a branch.
	if b, ok := branchFromDir(r, repo, root); ok {
		r.SetBranchHint(b)
	}
	return r, nil
}

// branchFromDir resolves the expressed branch whose folder dir (default ".") sits
// inside, relative to the repo root. Returns ("", false) at the root or outside
// any branch folder.
func branchFromDir(r *worktree.Repo, dir, root string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	first := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		first = rel[:i]
	}
	return r.BranchForFolder(first)
}

// openRepoSynced opens a repo and immediately snapshots every expressed folder
// into its open working change (SyncWorking), so working-copy-aware commands see
// live on-disk edits. A sync failure closes the repo and surfaces a clear error.
// Use this for read/inspect commands and pre-op safety checks; do NOT use it for
// history operations (undo/oplog) — snapshotting first would record an op that
// undo then targets.
func openRepoSynced(repo, author string) (*worktree.Repo, error) {
	r, err := openRepo(repo, author)
	if err != nil {
		return nil, err
	}
	if err := r.SyncWorking(); err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("snapshotting working copy: %w", err)
	}
	return r, nil
}

// openRepoSyncedVerbose is openRepoSynced for the verbs that can run for
// minutes on a large tree — express, unexpress, fold, pull. Progress is turned
// on BEFORE the working-copy sync, because on a big repo that sync is itself a
// slow prelude, and a command that prints nothing while it runs is
// indistinguishable from one that has hung (#153). The quick read-only verbs
// keep openRepoSynced and stay silent, so their output remains scriptable.
//
// It also returns the instant the command STARTED, which is before the sync.
// Timing the verb from after this call reported a "total" that excluded the
// sync prelude — and since that prelude can outlast everything after it, the
// reported total could be smaller than a phase printed inside it (#154).
// Returning the start here means no call site can reintroduce that gap.
func openRepoSyncedVerbose(repo, author string) (*worktree.Repo, time.Time, error) {
	started := time.Now()
	r, err := openRepo(repo, author)
	if err != nil {
		return nil, started, err
	}
	r.SetProgress(os.Stderr)
	if err := r.SyncWorking(); err != nil {
		_ = r.Close()
		return nil, started, fmt.Errorf("snapshotting working copy: %w", err)
	}
	return r, started, nil
}

// reportElapsed prints how long a slow verb took, so a long run is attributable
// afterwards rather than merely endured. Sub-second runs print nothing.
func reportElapsed(verb string, started time.Time) {
	if d := time.Since(started); d >= time.Second {
		fmt.Fprintf(os.Stderr, "cairn: %s took %s\n", verb, change.FormatDur(d))
	}
}

// repoFlags registers --repo and --author on fs, returning the bound vars.
func repoFlags(fs *flag.FlagSet) (repo, author *string) {
	repo = fs.String("repo", ".", "repo root directory")
	author = fs.String("author", defaultAuthor(), "commit author")
	return repo, author
}

// parseArgs parses fs while tolerating flags that appear AFTER positional
// arguments. Go's flag package stops at the first non-flag token, so
// `cairn commit <branch> -m msg` would silently drop -m (the message would
// default to "snapshot"). parseArgs reorders the tokens so every flag precedes
// the positionals, then parses once — leaving fs.Arg/fs.NArg/fs.Args working as
// usual. A literal "--" ends flag scanning: everything after it is positional.
func parseArgs(fs *flag.FlagSet, args []string) error {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			positionals = append(positionals, args[i+1:]...)
			i = len(args)
		case len(a) > 1 && a[0] == '-':
			// Git-style attached short-flag value, e.g. -n5 or -mmsg: split into the
			// flag and its value so go-git's flag parser (which only accepts -n 5 /
			// -n=5) sees a valid form. Only when the whole token isn't itself a flag
			// name and its first char IS a non-boolean flag, so "-author"/"-force"
			// are left intact.
			if a[1] != '-' && len(a) > 2 && !strings.Contains(a, "=") &&
				fs.Lookup(a[1:]) == nil && fs.Lookup(a[1:2]) != nil && !isBoolFlag(fs, a[1:2]) {
				flags = append(flags, "-"+a[1:2], a[2:])
				continue
			}
			flags = append(flags, a)
			// A non-boolean flag written as "-flag value" (no "=") consumes the
			// next token as its value, so keep them adjacent when reordering.
			name := strings.SplitN(strings.TrimLeft(a, "-"), "=", 2)[0]
			if !strings.Contains(a, "=") && !isBoolFlag(fs, name) && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
		default:
			positionals = append(positionals, a)
		}
	}
	return fs.Parse(append(flags, positionals...))
}

// isBoolFlag reports whether the named flag is a boolean flag (consumes no
// value), e.g. --force / --cairn / --dry-run.
func isBoolFlag(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

const prUsage = `cairn pr — pull requests against a cairn-server repo

usage: cairn pr <verb> [flags] [args]

verbs:
  open <source> <target> -m <title>   open a PR (idempotent: reopening the same
                                       source/target returns the existing PR)
  list                                 list a repo's PRs (--state open|merged|all)
  view <id>                            show one PR
  diff <id>                            print the PR's unified diff (--remote, default origin)
  merge <id>                           fast-forward-merge an open PR

connection flags (every verb):
  --org <org>          the org the repo belongs to (default $CAIRN_ORG, else
                        the 'pr.org' global config key)
  --repo-slug <slug>   the repo's slug (default $CAIRN_REPO_SLUG, else the
                        'pr.repo-slug' global config key)
  --server <addr>       cairn-server's gRPC address (default $CAIRN_GRPC_ADDR,
                        else 127.0.0.1:8102)
  --subject <id>        caller identity forwarded as cwb-subject (default
                        $CAIRN_SUBJECT, else the configured user.email)
  --tls-cert/--tls-key/--tls-ca <path>  mTLS client cert/key/CA (default
                        $CAIRN_TLS_CERT/_KEY/_CA)
  --insecure             skip mTLS (local dev only; requires cairn-server's
                        own CAIRN_DEV_INSECURE=1)

'pr open' additionally needs --project <key> (the ledger project the tracking
issue is filed under); --description and --dod are optional.

'pr diff' additionally accepts --repo (default .) and --remote (default origin):
it resolves the PR's source/target branch names via 'pr view', prune-fetches
--remote's tracking refs (no reconcile — works even if neither line was ever
expressed locally), and prints the unified diff MERGE-BASE(target,source)..source
— target...source semantics, like 'gh pr diff': only what source introduced
since it forked, never a spurious revert of target commits source never saw.
Unrelated histories (no common ancestor) error clearly. --remote must be a git
remote of --repo that addresses the SAME repo the gRPC server (--org/--repo-slug)
is addressing — cairn does not itself correlate the two.`

// prConn is the connection + identity config shared by every `pr` verb,
// bound to a flag.FlagSet by prConnFlags.
type prConn struct {
	org, slug, server, subject, tlsCert, tlsKey, tlsCA *string
	insecure                                           *bool
}

// dial validates the required org/slug are set and opens a prclient.Client +
// an identity-bearing context for the call.
func (c *prConn) dial(ctx context.Context, scopes ...string) (*prclient.Client, context.Context, error) {
	if *c.org == "" {
		return nil, nil, errors.New("pr: --org required (or $CAIRN_ORG / 'cairn config --global pr.org <org>')")
	}
	if *c.slug == "" {
		return nil, nil, errors.New("pr: --repo-slug required (or $CAIRN_REPO_SLUG / 'cairn config --global pr.repo-slug <slug>')")
	}
	if *c.subject == "" {
		return nil, nil, errors.New("pr: --subject required (or $CAIRN_SUBJECT, or run 'cairn setup' to set user.email)")
	}
	cli, err := prclient.NewClient(*c.server, *c.tlsCert, *c.tlsKey, *c.tlsCA, *c.insecure)
	if err != nil {
		return nil, nil, fmt.Errorf("pr: %w", err)
	}
	idCtx := prclient.WithIdentity(ctx, prclient.Identity{Subject: *c.subject, Org: *c.org, Scopes: scopes})
	return cli, idCtx, nil
}

// mapRemoteErr translates go-git transport/network failures into actionable
// guidance. It falls through to mapErr for anything it doesn't recognize.
func mapRemoteErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, transport.ErrAuthenticationRequired),
		errors.Is(err, transport.ErrAuthorizationFailed):
		return errors.New("authentication failed — set $CAIRN_TOKEN (a personal access token) for an HTTPS remote, or check your ssh-agent/key for an SSH remote")
	case errors.Is(err, transport.ErrRepositoryNotFound):
		return errors.New("repository not found — check the remote URL and that you have access")
	}
	// Network-shaped failures (no typed sentinel): match by shape/substring.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return errors.New("could not reach the remote — check the URL and your network connection")
	}
	msg := err.Error()
	for _, s := range []string{"no such host", "connection refused", "i/o timeout", "network is unreachable", "dial tcp"} {
		if strings.Contains(msg, s) {
			return errors.New("could not reach the remote — check the URL and your network connection")
		}
	}
	// A protected-branch / pre-receive-hook rejection: the update is a valid
	// fast-forward but the remote's policy declined it (changes must go through a
	// PR). This is what you hit after folding into an upstream branch locally.
	low := strings.ToLower(msg)
	for _, s := range []string{"protected branch", "gh006", "hook declined", "[remote rejected]", "remote: error", "push declined", "cannot lock ref"} {
		if strings.Contains(low, s) {
			return fmt.Errorf("the remote rejected the push — the branch is likely protected (changes need a pull request). If you folded or committed into this branch locally, 'cairn undo' rewinds it; then push your own line and open a PR. (%v)", err)
		}
	}
	return mapErr(err)
}

// mapErr translates change-engine sentinels into operator-facing messages.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, change.ErrPushHasConflict):
		// The gate's own error already names the branch(es) and the full set of
		// remedies — pass it through rather than restating them.
		return err
	case errors.Is(err, change.ErrHasConflict):
		return fmt.Errorf("resolve conflicts before folding: %w", err)
	case errors.Is(err, change.ErrNotFound):
		return err
	default:
		return err
	}
}
