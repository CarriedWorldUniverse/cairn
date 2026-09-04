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
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, errConflicts) {
			os.Exit(2)
		}
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "cairn:", err)
		os.Exit(1)
	}
}

const usage = `cairn — cairn working-copy CLI

usage: cairn <subcommand> [flags] [args]

subcommands:
  init [dir]                bootstrap a repo (expresses main)
  clone <url> [dir]         import a remote repo and express its default branch
  express <branch>          materialize a branch folder (--from <parent>)
  unexpress <branch>        remove a branch folder (--force to discard un-sealed work)
  commit <branch> [-m msg]  seal the working change (stamps msg, starts a fresh change)
  fold <branch>             fold a branch into its parent (--force to discard un-sealed work)
  reparent <branch> <parent>  set a branch's parent line (fix stacked topology after a git import)
  abandon <branch>          discard a branch's line (--force to discard un-sealed work)
  status [branch]           report a branch's state — the working change vs its parent (default: root)
  diff [branch] [-- <path>...]  show the working change vs its parent (optionally one file/dir)
  diff <a> <b> [-- <path>...]   show commit-vs-commit (optionally filtered to paths)
  tree                          print the line tree
  ls                            list expressed branches
  resolve <branch> <path>       resolve a conflict on a branch — takes the file's on-disk content, or its ABSENCE to resolve as a deletion; refuses lingering <<<<<<< markers (--force to accept)
  remote                        list configured remotes
  remote add <name> <url>       register a remote (--cairn for a cairn remote)
  push [remote] [branch]        publish all lines + tags, or just <branch> (default origin, --force)
  fetch [remote]                fetch a remote into tracking refs (default origin)
  pull [remote]                 fetch + reconcile each line (default origin)
  pr open <source> <target> -m <title>  open a pull request against cairn-server (idempotent)
  pr list                       list a repo's pull requests (--state open|merged|all)
  pr view <id>                  show one pull request
  pr diff <id>                  print the pull request's unified diff (--remote)
  pr merge <id>                 fast-forward-merge an open pull request
  blame <path> [branch]         show per-line author/date/commit
  log [branch] [-n N]           show commit history
  show <commit>                 show a commit's metadata + diff
  undo                          revert the last operation
  oplog                         show the operation history
  setup                         set your commit identity (name/email), stored globally
  login <host>                  store an access token for a host (stdin: echo $TOK | cairn login github.com)
  logout <host>                 remove a host's stored credential
  auth                          list hosts with stored credentials (never the tokens)
  config [--global] <key> [value]  get/set a config value (--global = user-level, all repos)
  tag <name> [branch]           tag the tip of a branch (default: root branch)
  private <path> [--shape-only] withhold a file/folder from every push (omit by default)
  private ls                    list withheld paths
  embargo <commit>              hold a commit (+ all after it) out of the public projection
  embargo ls                    list embargoed commits
  disclose <path|commit>        stop withholding a path, or lift an embargo (make public)
  version [--target eco] [--release]  print the derived version (stdout only, CI-safe)
  version bump <level>          record explicit bump intent (major|minor|patch)
  release --target eco          cut a clean release: tag + stamp + publish (--dry-run)
  update                        replace this binary with the latest release (--check to only report, --force to reinstall)
  stash [-m msg] [branch]   shelve the working change; reset the folder to the sealed state
  stash pop [branch] [id]   restore a stash onto branch (default: most recent)
  stash list                list the stash stack
  stash drop [id]           discard a stash (default: most recent)
  reword <commit> <message> change the message of a sealed commit (history edit)
  squash <commit>           fold a sealed commit into its parent (history edit)
  drop <commit>             remove a sealed commit from its line (history edit)
  reauthor --old-email <glob> --name <n> --email <e> [--dry-run]  bulk-retag commit identity (whole repo)
  cherry-pick <commit> [branch]  apply a commit from another line onto your branch
  bisect start --good <c> --bad <c> [branch]  begin a bisect (materializes the midpoint)
  bisect good | bad             mark the current commit; materializes the next midpoint
  bisect skip                   step over an untestable midpoint
  bisect status                 show the active bisect session
  bisect reset                  end the bisect; restore the working folder
  bisect run [--repo d] -- <cmd>   automate: 0=good, 125=skip, else=bad

git spellings (aliases for the cairn verb above):
  merge <branch>                = fold <branch>
  branch <name>                 = express <name>
  checkout -b <name>            = express <name>
  switch -c <name>              = express <name>
(other git verbs — add, checkout <branch>, rebase, reset, rm, mv — have no cairn
equivalent; running one prints the translation.)

config keys: user.name, user.email, autosync, pr.org, pr.repo-slug
common flags (repo subcommands): --repo <dir> (default .), --author <name>`

// run dispatches a subcommand. args is os.Args[1:].
func run(args []string) error {
	if len(args) == 0 {
		fmt.Println(usage)
		return errors.New("no subcommand")
	}
	sub, rest := args[0], args[1:]
	if sub == "--version" || sub == "-v" {
		fmt.Println("cairn", buildVersion)
		return nil
	}
	switch sub {
	case "help", "-h", "--help":
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
	default:
		// A git spelling gets either its cairn alias or a one-line correction
		// (#139) — printed INSTEAD of the usage wall, so the correction is what
		// the reader actually sees at the moment the reflex misfires.
		if handled, err := runGitShaped(sub, rest); handled {
			return err
		}
		fmt.Println(usage)
		return fmt.Errorf("unknown subcommand %q", sub)
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
	for {
		if fi, serr := os.Stat(filepath.Join(dir, ".cairn")); serr == nil && fi.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached the filesystem root
			return "", fmt.Errorf("not a cairn repo (no .cairn in %q or any parent directory; run 'cairn init')", start)
		}
		dir = parent
	}
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
