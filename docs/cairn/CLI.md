# cairn CLI — usage reference

`cairn` is the working-copy command-line tool: a local version-control system built
on [`go-git`](https://github.com/go-git/go-git) that replaces day-to-day `git`. It
borrows the good ideas from Jujutsu (jj) — the working copy is always a commit,
conflicts are data instead of blockers, every operation is undoable — while staying
a plain CLI you can drive by hand or from a script/agent.

This document is the full command reference. For the design rationale see the specs
under [`docs/cairn/`](.); for installation see the [README](../../README.md#install-the-cli).

---

## Contents

- [Mental model](#mental-model) — lines, the working change, sealing, conflicts-as-data
- [Global flags, config & environment](#global-flags-config--environment)
- [Quick start](#quick-start)
- [Command reference](#command-reference)
  - [Setup](#setup) · [Lines (branches)](#lines-branches) · [Saving work](#saving-work)
  - [Inspecting](#inspecting) · [Conflicts](#conflicts) · [Undo & history](#undo--history)
  - [History editing](#history-editing) · [Stash](#stash) · [Bisect](#bisect)
  - [Remotes & collaboration](#remotes--collaboration) · [Versioning & release](#versioning--release)
- [Exit codes](#exit-codes)
- [Coming from git](#coming-from-git)

---

## Mental model

A few concepts make the whole CLI consistent:

- **Lines, not branches.** A *line* is a named track of history. Lines form a **tree**:
  every line is forked from a parent (the root line is `main`). `cairn tree` prints it.
- **Expressing a line = a folder on disk.** `cairn express <name>` materialises a line
  as a working folder next to your repo. You edit files in that folder directly. Several
  lines can be expressed at once (each its own folder) — cheap parallel checkouts.
- **The working copy is always a commit.** The expressed folder is the *working change*
  of its line — a real, in-progress commit. cairn **auto-snapshots** it into that commit
  at the **start of every command** (no daemon, no `git add`, no staging area). Your edits
  are never in an unsaved limbo; `status`/`diff` always compare the live folder against the
  line's last sealed commit.
- **`commit` seals.** `cairn commit <line>` *seals* the working change: it stamps your
  message, merges the work forward, and opens a fresh working change to keep editing in.
  Sealing is the equivalent of "make a real commit you intend to keep."
- **Conflicts are data, not stop-the-world.** When work overlaps, cairn records a conflict
  on the affected file and **keeps going** (the command exits with code `2`, never a hard
  failure). You resolve it later with `cairn resolve`. Nothing blocks while a conflict is open.
- **Everything is undoable.** Every mutating command appends to an **operation log**
  (`cairn oplog`). `cairn undo` reverts the last operation — including a bad commit, merge,
  or history edit.
- **History editing is bounded on purpose.** `reword`/`squash`/`drop` only work on **your
  own un-folded leaf line** (a line with no children, above its base, not the trunk). This
  preserves the multi-agent guarantee that nobody's foundation can be rewritten under them.

## Global flags, config & environment

Subcommands that operate on an existing repo accept:

| Flag | Default | Meaning |
|------|---------|---------|
| `--repo <dir>` | `.` | Repo root — cairn walks up from here to find `.cairn`, so you can run from any subfolder (like git finds `.git`). Run from inside a branch folder and commands default to **that branch** (like git's current branch); `commit -m …` needs no branch argument |
| `--author <name>` | `$CAIRN_AUTHOR`, else `$USER`, else `cairn` | Author recorded on operations |

Persistent config (stored in the repo, set with `cairn config`):

| Key | Meaning |
|-----|---------|
| `user.name` | Name written into sealed commits |
| `user.email` | Email written into sealed commits |
| `autosync` | When truthy, `commit` does a best-effort `pull` from `origin` afterward |

Environment:

| Variable | Used for |
|----------|----------|
| `CAIRN_AUTHOR` | Default `--author` |
| `CAIRN_TOKEN` / `GITHUB_TOKEN` | HTTPS auth for `push`/`fetch`/`pull`/`clone` (also reads `git credential` and ssh-agent/keys for SSH remotes) |

Auth never prompts interactively (`GIT_TERMINAL_PROMPT=0`) — a missing credential fails
with a clear message instead of hanging.

## Quick start

```sh
# 1. make a repo (expresses the root line "main" as ./main)
cairn init myproject
cd myproject

# 2. tell cairn who you are (written into sealed commits)
cairn config user.name  "Ada Lovelace"
cairn config user.email "ada@example.com"

# 3. edit files inside the main/ folder, then see what changed
echo "hello" > main/README.md
cairn status                 # working change vs the last sealed commit

# 4. seal a commit
cairn commit main -m "initial README"

# 5. fork a line to work on a feature in parallel
cairn express feature --from main   # creates ./feature
echo "wip" > feature/feature.txt
cairn commit feature -m "start the feature"

# 6. inspect, then fold the finished line back into its parent
cairn log feature
cairn tree
cairn fold feature
```

---

## Command reference

Synopsis conventions: `<required>`, `[optional]`. Unless noted, every command takes
`--repo`/`--author`.

### Setup

#### `cairn init [dir]`
Bootstrap a new repo in `dir` (default `.`) and express the root line `main`.
```sh
cairn init                 # initialise the current directory
cairn init myproject       # create + initialise ./myproject
```

#### `cairn clone <url> [dir]`
Import a remote repo and express its default branch. Works with any git remote; if the
remote is a cairn remote (carries `refs/cairn/meta`) the full cairn change-graph — line
tree, change-ids, open conflicts — is reconstructed, not just the flat git projection.
```sh
cairn clone https://github.com/me/proj.git
cairn clone git@github.com:me/proj.git proj
```

#### `cairn config <key> [value]`
Get (one argument) or set (two arguments) a config value. Keys: `user.name`,
`user.email`, `autosync`.
```sh
cairn config user.email "ada@example.com"   # set
cairn config user.email                      # get
cairn config autosync true                   # enable commit-time auto-pull
```

### Lines (branches)

#### `cairn express <branch>` — `--from <parent>`
Materialise a line as a working folder. With `--from`, fork a **new** line off `<parent>`;
without it, express an existing line. The folder is created next to the repo root.
```sh
cairn express feature --from main   # new line forked from main
cairn express main                  # re-express an existing line
cairn express base/5-0 --from main  # folder is base-5-0 (flat); branch stays base/5-0
```
A path-like branch name expresses as a single **flat** folder — `/` becomes `-`
(`base/5-0` → `base-5-0`) — so branch folders never nest. The branch name itself is
unchanged (`tree`/`log`/`push` use `base/5-0`). If two branches would map to the same
folder (e.g. `feat/x` and a literal `feat-x`), express refuses rather than clobber.

#### `cairn unexpress <branch>` — `--force`
Remove a line's working folder (the line itself is kept). Refuses if the folder has
un-sealed work unless `--force` (the work remains recoverable via `undo`).

#### `cairn fold <branch>` — `--force`
Fold a finished line into its parent (merge-forward), then retire it. `--force` discards
un-sealed work in the folder.

Folding into a **remote-tracked** line (one that arrived from a remote — e.g. the `main`
you cloned) is **refused by default**: advancing an upstream branch locally diverges from
how the remote integrates the change (a PR / its own merge), and a protected remote will
reject the push. Push your line and open a PR instead — or `--force` to fold anyway. (Lines
you created locally with `express` are never guarded; `cairn undo` reverts a fold.)

#### `cairn abandon <branch>` — `--force`
Discard a line entirely (it is not folded anywhere). `--force` to drop un-sealed work.

#### `cairn tree`
Print the line tree (parent/child structure of all lines).

#### `cairn ls`
List the currently expressed lines (which folders exist on disk).

### Saving work

#### `cairn commit <branch>` — `-m <msg>`
**Seal** the working change of `<branch>`: stamp the message, merge the work forward, and
open a fresh working change. This is the "make a commit I mean to keep" verb.
Exits `2` if sealing recorded a conflict (the commit still happens — see [conflicts](#conflicts)).
```sh
cairn commit main -m "fix the parser"
```

### Inspecting

#### `cairn status [branch]`
Report a line's state: the working change versus its parent commit, file-by-file
(`M`/`A`/`D`), plus how far ahead the line is. Defaults to the root line.

#### `cairn diff [branch]` / `cairn diff <a> <b>`
With zero/one argument: show the working change versus its parent for that line. With two
commit arguments: show the diff between two commits.
```sh
cairn diff feature          # working change vs parent
cairn diff <shaA> <shaB>    # commit-to-commit
```

#### `cairn log [branch]` — `-n <N>`
Show commit history for a line (default: root). `-n` caps the number of commits (default 20).
The in-progress working change is shown labelled `(working)`.

#### `cairn show <commit>`
Show one commit's metadata and its diff.

#### `cairn blame <path> [branch]`
Per-line provenance for a file: author, date, and the change that last touched each line.
Lines still in an un-sealed working change show as `(working)`.

### Conflicts

cairn never blocks on a conflict. A command that produces overlapping work records the
conflict on the file, finishes, and **exits with code `2`**. The file holds conflict
markers; `status` lists it.

You can keep working, but **`commit` refuses while a conflict is unresolved** (like git's
unmerged-paths block): you must edit out the `<<<<<<<` markers and `cairn resolve <path>`
each conflicted file first. This prevents a later commit from silently baking the marker
text into history and dropping the conflict.

#### `cairn resolve <branch> <path>`
Mark a conflicted file resolved after you have edited it to the desired content.
```sh
cairn status feature        # shows the conflicted path
$EDITOR feature/foo.txt      # remove the markers, keep what you want
cairn resolve feature foo.txt
cairn commit feature -m "resolve foo"
```

### Undo & history

#### `cairn undo`
Revert the most recent operation — commit, merge, fold, history edit, anything in the
op-log. Repeatable.

#### `cairn oplog`
Show the operation history (the list `undo` walks back through). A burst of auto-snapshots
coalesces into a single undoable step.

### History editing

Safe, discrete history rewrites — no interactive rebase. **Allowed only on your own
un-folded leaf line** (non-root, no child lines, above its base). Descendants in the
same line are auto-rebased; `reword`/`squash` stay conflict-free, `drop` records any
conflict as data.

#### `cairn reword <commit> <message>`
Change the message of a sealed commit.

#### `cairn squash <commit>`
Fold a sealed commit into its parent (combine two commits into one).

#### `cairn drop <commit>`
Remove a sealed commit from its line.

#### `cairn cherry-pick <commit> [branch]`
Apply a commit from another line onto `<branch>` (default: the current/root line) as a new
sealed commit with a fresh identity. Your in-progress working change is kept separate and
rebased on top. Conflicts are recorded as data (no `--abort`/`--continue` dance).

### Stash

A minimal shelve stack for the rare case the always-saved working copy and multiple
expressed folders don't already cover. `pop` refuses onto a dirty folder.

| Command | Action |
|---------|--------|
| `cairn stash [-m <msg>] [branch]` | Shelve the working change; reset the folder to the sealed state |
| `cairn stash pop [branch]` | Restore the most recent stash onto `branch` |
| `cairn stash list` | List the stash stack |
| `cairn stash drop [id]` | Discard a stash (default: most recent) |

### Bisect

Binary-search a line's sealed history for the commit that introduced a problem. While a
bisect is active the expressed folder shows the midpoint commit and auto-snapshot is
suspended; `bisect reset` ends the session and restores your working tip.

| Command | Action |
|---------|--------|
| `cairn bisect start --good <c> --bad <c> [branch]` | Begin; materialise the first midpoint |
| `cairn bisect good` / `cairn bisect bad` | Mark the current midpoint; materialise the next |
| `cairn bisect skip` | Step over an untestable midpoint |
| `cairn bisect status` | Show the active session |
| `cairn bisect reset` | End the bisect; restore the working folder |
| `cairn bisect run [--repo <d>] -- <cmd>` | Automate: run `<cmd>` at each step — exit `0`=good, `125`=skip, anything else=bad |

```sh
cairn bisect start --good v0.1.0 --bad HEAD feature
cairn bisect run -- go test ./...
```

### Remotes & collaboration

#### `cairn remote` / `cairn remote add <name> <url>` — `--cairn`
List remotes, or register one. `--cairn` marks it a cairn remote, so `push` writes the
full-fidelity `refs/cairn/meta` (line tree + change-ids + open conflicts). Without it, a
remote is treated as plain git and receives the flat projection.
```sh
cairn remote add origin https://github.com/me/proj.git
cairn remote add team git@host:team/proj.git --cairn
```

#### `cairn push [remote] [branch]` — `--force`
Publish lines + tags (default `origin`). If the remote moved, push auto-pulls and retries
once; `--force` overwrites a diverged remote branch.

With a `branch` argument, push **only that line** (plus tags) — e.g. feed one feature line
to the remote to open a PR, without touching the remote-tracked `main`:
```sh
cairn push origin feat      # publish just 'feat'
```
(The single-line push does not auto-pull-retry — a diverged branch surfaces the clear error.)

#### `cairn fetch [remote]`
Fetch a remote into tracking refs (default `origin`) without reconciling.

#### `cairn pull [remote]`
Fetch and reconcile each line (default `origin`).

### Versioning & release

cairn derives versions from the change-graph rather than hand-typed numbers.

#### `cairn tag <name> [branch]`
Tag the tip of a line (default: root).

#### `cairn version` — `--target <eco>` `--release`
Print the derived version (stdout only, safe to capture in CI). `--target` renders for an
ecosystem (`npm`, `nuget`, `pypi`, `oci`, `go`); `--release` prints the clean version that
`cairn release` would tag (versus the CI build-id form like `1.4.1+3.gabc`).

#### `cairn version bump <level>`
Record explicit bump intent: `major`, `minor`, or `patch`.

#### `cairn release` — `--target <eco>` `--dry-run`
Cut a clean release: tag, stamp manifests, and publish — publish happens last with
rollback on failure. `--dry-run` shows the plan without tagging or publishing.
```sh
cairn release --target npm --dry-run
cairn release --target npm
```

### Misc

- `cairn --version` / `cairn -v` — print the **build** version of the binary (the release
  you installed), distinct from the `version` subcommand above which derives the repo's version.
- `cairn help` / `cairn -h` / `cairn --help` — print the usage summary.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `2` | Completed, but recorded one or more conflicts (e.g. `commit`/`pull`) — so `cairn commit && cairn push` won't push a conflicted state |
| `1` | Hard error (bad arguments, not a cairn repo, auth failure, …) |

## Coming from git

| You'd reach for… | In cairn |
|------------------|----------|
| `git init` | `cairn init` |
| `git clone` | `cairn clone` |
| `git checkout -b feat` | `cairn express feat --from main` |
| `git add` + `git commit -m` | just edit, then `cairn commit <line> -m` (no staging) |
| `git status` / `git diff` | `cairn status` / `cairn diff` |
| `git log` / `git show` / `git blame` | `cairn log` / `cairn show` / `cairn blame` |
| `git stash` | `cairn stash` (often unneeded — work is always saved + you can express many folders) |
| `git commit --amend` / interactive rebase | `cairn reword` / `squash` / `drop` (on your own leaf line) |
| `git cherry-pick` | `cairn cherry-pick` |
| `git bisect` | `cairn bisect` |
| `git merge` (into parent) | `cairn fold` |
| `git reset --hard` / `git reflog` + reset | `cairn undo` (walks the op-log) |
| `git push` / `git fetch` / `git pull` | `cairn push` / `fetch` / `pull` |
| resolving a merge conflict (blocks you) | keep working; `cairn resolve <line> <path>` when ready |
