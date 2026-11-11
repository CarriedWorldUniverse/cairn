# cairn CLI — usage reference

`cairn` is the working-copy command-line tool: a local version-control system built
on [`go-git`](https://github.com/go-git/go-git) that replaces day-to-day `git`. It
borrows the good ideas from Jujutsu (jj) — the working copy is always a commit,
conflicts are data instead of blockers, every operation is undoable — while staying
a plain CLI you can drive by hand or from a script/agent.

> **For LLM agents:** run `cairn help agent` first. It is a compact page (about 100 lines, compiled into
> the binary) covering the model, git-to-cairn deltas, hazards, exit codes and script contracts of
> the binary you are running; this document is the long-form reference.

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
  - [History editing](#history-editing) · [Stash](#stash) · [Working-copy verbs](#working-copy-verbs) · [Bisect](#bisect)
  - [Remotes & collaboration](#remotes--collaboration) · [Pull requests](#pull-requests) · [Privacy](#privacy-withholding-from-pushes) · [Versioning & release](#versioning--release)
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
- **History editing is bounded on purpose.** `reword`/`squash`/`drop`, `commit --amend` and
  `reset <rev>` rewrite only commits nobody else stands on: never a commit a child line is
  based on, never folded-in history, and — on the root line — never a commit that is already
  on the remote. This preserves the multi-agent guarantee that nobody's foundation can be
  rewritten under them, while `commit --amend` on `main` right after a commit just works.
  The one deliberate exception is `reauthor`, a whole-repo identity rewrite (every line, root
  included) — its job is to fix attribution everywhere at once.

## Global flags, config & environment

Subcommands that operate on an existing repo accept:

| Flag | Default | Meaning |
|------|---------|---------|
| `--repo <dir>` | `.` | Repo root — cairn walks up from here to find `.cairn`, so you can run from any subfolder (like git finds `.git`). Run from inside a branch folder and commands default to **that branch** (like git's current branch); `commit -m …` needs no branch argument |
| `--author <name>` | `$CAIRN_AUTHOR` (else cairn's configured identity) | Author override for a single operation |

### Identity

cairn owns its commit identity — it does **not** silently inherit your git config. It
resolves the author of a sealed commit from, in order:

1. the `--author` flag / `$CAIRN_AUTHOR` (one-off override), then
2. the **repo** config (`cairn config user.name`), then
3. the **global** config (`cairn config --global user.name`, shared by all your repos), then
4. on a terminal with nothing set, a one-time first-use prompt (see [`cairn setup`](#cairn-setup));
   non-interactively (CI/agents) it falls back to your `git config` as a last resort.

The global config lives under your OS user-config dir (`~/.config/cairn/config` on Linux,
`%AppData%\cairn\config` on Windows). Email resolves the same way, also honouring
`$CAIRN_EMAIL` / `$GIT_AUTHOR_EMAIL`.

Config keys (repo or `--global`):

| Key | Meaning |
|-----|---------|
| `user.name` | Name written into sealed commits |
| `user.email` | Email written into sealed commits |
| `autosync` | (repo only) When truthy, `commit` does a best-effort `pull` from `origin` afterward |

Environment:

| Variable | Used for |
|----------|----------|
| `CAIRN_AUTHOR` | One-off `--author` default (name) |
| `CAIRN_EMAIL` / `GIT_AUTHOR_EMAIL` | One-off author email override |
| `CAIRN_TOKEN` / `GITHUB_TOKEN` | HTTPS auth for `push`/`fetch`/`pull`/`clone` (also reads `git credential` and ssh-agent/keys for SSH remotes) |

Auth never prompts interactively (`GIT_TERMINAL_PROMPT=0`) — a missing credential fails
with a clear message instead of hanging.

### Ignore files

cairn honors **`.gitignore` in every directory** (standard gitignore syntax — anchoring `/`,
dir-only `bin/`, `**` globs, negation `!`, with deeper directories and later lines overriding
shallower ones), exactly like git, plus a cairn-specific **`.cairnignore`** with the same syntax
(applied alongside `.gitignore` per directory; cairn rules win ties). Ignore rules only affect
**untracked** paths — a file that's already committed is never silently dropped even if it later
matches a pattern. Only **in-tree** ignore files are read (no global/`core.excludesFile` or
`info/exclude`), so a repo snapshots the same file set on every machine — important for
reproducible multi-agent convergence. For *secrets* that must never be pushed, use
[`cairn private`](#privacy-withholding-from-pushes) (enforced + tracked), not ignore (which only
skips *untracked* files).

## Quick start

```sh
# 1. make a repo (expresses the root line "main" as ./main)
cairn init myproject
cd myproject

# 2. tell cairn who you are (once, globally, for every repo)
cairn setup                                  # interactive; or set it directly:
cairn config --global user.name  "Ada Lovelace"
cairn config --global user.email "ada@example.com"

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

#### `cairn init [dir]` — `-b <name>` `-q`
Bootstrap a new repo in `dir` (default `.`) and express the root line `main` (or `-b <name>` /
the global `init.defaultBranch`). `--bare` is refused: a cairn repo is always a set of line folders.
`dir` must be empty (or absent): files already sitting in it would stay outside every line and
never be captured, so `init` refuses — init into a new directory and move the files into
`<newdir>/main/`. Line names follow `git check-ref-format --branch` (no `.cairn`, `..`, empty,
leading `.`/`-`, …) everywhere one is accepted.
```sh
cairn init                 # initialise the current directory
cairn init myproject       # create + initialise ./myproject
```

#### `cairn clone <url> [dir]` — `-b <line>` `-o <name>` `-q`
`-b` also expresses that remote line; `-o` names the remote (default `origin`); `-q` silences
progress. `--depth`, `--single-branch` and `--bare` are refused: a clone imports the full
history and every line so the line tree, change-ids and conflicts reconstruct exactly.

Import a remote repo and express its default branch. Works with any git remote; if the
remote is a cairn remote (carries `refs/cairn/meta`) the full cairn change-graph — line
tree, change-ids, open conflicts — is reconstructed, not just the flat git projection.
```sh
cairn clone https://github.com/me/proj.git
cairn clone git@github.com:me/proj.git proj
```
Progress is reported for every phase, not just the download: after git's own sideband
(`Enumerating/Compressing/Total …`) cairn reports resolving the default branch, mapping
branches onto the line tree, recording tags, and a running file count while materializing.
On a large repo the phases after the pack can take a while, and each one says so rather
than leaving the terminal looking hung.

The branch that becomes the root line is the one the remote's own `HEAD` names —
`develop` stays `develop`. If cairn cannot reach the remote for that answer, it
uses a sole branch when there is only one and otherwise **fails rather than
guess**: a clone that quietly checked out the wrong branch just looks like an
incomplete checkout, so re-run it instead. A remote with no default at all (a
bare push target whose `HEAD` was never pointed anywhere) falls back to a
conventional trunk name and says so on stderr.

#### `cairn setup`
Set your commit identity (name + email), stored **globally** for every repo. Run once
on a new machine. It pre-fills suggestions from your `git config` (so it feels like
`gh`'s git-config step) — press Enter to accept or type your own. cairn also runs this
automatically the first time you `commit` on a terminal with no identity set yet.
```sh
cairn setup
# cairn setup — your commit identity (stored globally for all repos).
# Your name [Ada Lovelace]:
# Your email [ada@example.com]:
```

#### `cairn config [--global|--local] <key> [value]`
Get (one argument) or set (two arguments) a config value. Without a scope flag it reads this
repo's config falling through to your user-level config, and writes the repo's; `--global`
reads/writes your **user-level** config (shared by all repos — repo settings override it).
Any `section.key` is stored (`alias.*`, `core.editor`, `pull.rebase`, `init.defaultBranch`,
`user.*` …); `autosync`, `push.default`, `pull.rebase`, `color.ui` and `init.defaultBranch`
are validated. Keys are case-insensitive in the section and name, like git.
```sh
cairn config --global user.name "Ada Lovelace"  # set your global identity
cairn config user.email "ada@work.example"       # override email in this repo only
cairn config user.email                          # get (repo, falling through to global)
cairn config autosync true                       # enable commit-time auto-pull
cairn config -l [--show-origin]                  # list the merged config
cairn config --get core.editor                   # print; exit 1 if unset
cairn config --unset core.editor                 # remove (add --global for the user config)
```

**Aliases.** `alias.<name>` defines a command: `cairn config --global alias.st status` then
`cairn st`. An expansion starting with `!` runs through the shell, extra arguments appended,
from the repo root. An alias never shadows a built-in verb; a self-referencing one is refused.

**Exec-capable config is trusted only from you.** `core.pager`, `pager.<cmd>` and `!` aliases run
shell commands, so a repo's own config is honoured for them only when its catalogue
(`.cairn/cairn.db`) is a regular file owned by the current user (git's `safe.directory` idea;
not checked on Windows); otherwise the repo value is ignored (a `!` alias prints a note) and
your user-level config applies. `core.editor` is never read — the editor comes from
`$CAIRN_EDITOR`/`$GIT_EDITOR`/`$VISUAL`/`$EDITOR`. Separately, `.cairn` and `.git` (any case, plus
NTFS/HFS+ aliases such as `.cairn.`, `CAIRN~1`, `.g\u200cit`) are refused as path components in
cloned/pulled trees and applied patches ("invalid path component"), and repo discovery only
accepts a `.cairn` that is a real directory with a catalogue and is not nested inside an outer
repo's line folder.

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

**Gitlinks (submodules).** A tree entry recording a nested checkout (git mode `160000`,
whether or not the repo has a `.gitmodules`) expresses as an **empty directory** — the same
thing git does for a submodule it has not initialised. cairn does not fetch, populate, or
update the nested repository: fill it yourself (`git submodule update`, or your own clone)
and cairn leaves that subtree alone — it is neither scanned into your commits nor swept as
untracked clutter. The pointer is carried forward unchanged by every commit, so **removing a
submodule is not expressible through cairn**; do that in a git client.

#### `cairn reparent <branch> <new-parent>`
Set a line's parent. Cloning from a **git** remote flat-projects every branch as a child
of the root (git records no branch parentage), so a *stacked* branch — `base/5-0` forked
from `rc/4-1`, not `main` — arrives rooted at trunk. Reparenting restores the real
topology, which fixes the `lineage`, the `fold` destination, and the reconcile base in one
move. Refuses to reparent the root, onto itself, or onto a descendant (a cycle).
```sh
cairn reparent base/5-0 rc/4-1   # lineage becomes main → rc/4-1 → base/5-0
```
(cairn→cairn clones preserve the real tree via `refs/cairn/meta`; this is only needed after
importing from a plain git remote.)

#### `cairn unexpress <branch>` — `--force`
Remove a line's working folder (the line itself is kept). Refuses if the folder has
un-sealed work unless `--force` (the work remains recoverable via `undo`).

#### `cairn fold <branch>` — `--force`
Fold a finished line into its parent (merge-forward), then retire it. The line's **sealed**
commits become part of the parent's history; if the parent moved on since the line last
adopted it, the fold adopts it first (a `fold: adopt <parent>` merge), refusing — with nothing
changed — if that conflicts (seal the line to record the conflict, resolve, fold again).
Un-sealed edits in the **parent's** folder ride across untouched and stay un-sealed; if they
overlap what the line changes, the fold is refused until you commit or stash them.

A line with un-sealed work is refused without `--force`; with it, that work is carried into
the parent as a commit of its own (`fold <line>: un-sealed work`). `cairn undo` reverts a
fold and re-opens the line (`cairn express <line>` brings its folder back).

Folding into a **remote-tracked** line (one that arrived from a remote — e.g. the `main`
you cloned) is **refused by default**: advancing an upstream branch locally diverges from
how the remote integrates the change (a PR / its own merge), and a protected remote will
reject the push. Push your line and open a PR instead — or `--force` to fold anyway. (Lines
you created locally with `express` are never guarded; `cairn undo` reverts a fold.)

`merge` is the same verb. A line folds into **its parent**: standing in line `a`, `cairn merge b`
where `b` forks from `main` is refused (it would land in `main`, not `a`). `--no-ff` always
records a merge commit, `--ff-only` refuses unless the line already contains the parent's tip,
`-m <msg>` names the merge commit; `--squash`, `--abort` and `--continue` are refused with the
cairn equivalent (`cairn squash`, `cairn undo`, `resolve` + `commit`).

#### `cairn abandon <branch>` — `--force`
Discard a line entirely (it is not folded anywhere). `--force` to drop un-sealed work.

#### `cairn tree`
Print the line tree (parent/child structure of all lines).

#### `cairn ls`
List the currently expressed lines (which folders exist on disk).

### Saving work

#### `cairn commit [branch]` — `-m <msg>` `-F <file>` `--amend` `--author` `--date`
**Seal** the working change of `<branch>`: stamp the message, merge the work forward, and
open a fresh working change. This is the "make a commit I mean to keep" verb.
Exits `2` if sealing recorded a conflict (the commit still happens — see [conflicts](#conflicts)).
```sh
cairn commit main -m "fix the parser"
cairn commit -m "subject" -m "a body paragraph"     # from inside the line's folder
cairn commit -F message.txt                         # or -F - for stdin
```
The message comes from `-m` (repeat for paragraphs) or `-F`; else from a stopped
`revert`/`cherry-pick`; else, on a terminal, from your editor (`$CAIRN_EDITOR`, `$GIT_EDITOR`,
`$VISUAL`, `$EDITOR`, then `vi` — `#` lines are stripped, an empty message aborts); a script
with no message still gets the historical `snapshot`. `-a`/`--all` is accepted and changes
nothing (every commit already takes all edits). As in git, a commit with nothing to record
fails (`nothing to commit, working tree clean`, exit 1); `--allow-empty` seals it anyway.

**Partial commit — cairn's staging area.** Name paths and only those are sealed; every other
edit stays, untouched, in the working change:
```sh
cairn commit -m "just the parser" -- src/parser.go docs/     # files, new/deleted files, directories
```
The first positional is the branch when it names an expressed line; anything else is a path
(`--` settles a clash). A partial commit appends one commit without adopting the parent line;
the next full commit reconciles as usual.

**`--amend`** folds the working change into the previous sealed commit (keeping its message
unless `-m`/`-F` replaces it, and its author unless `--reset-author`). It is a history edit,
under [the one rule](#history-editing) they all share: fine on `main` until the commit is on
the remote (then use `cairn revert`), and never on a commit a child line is based on. On a
non-root line an already-pushed commit can be amended; cairn says the next push needs `--force`.

**`--author` has two shapes.** `--author="Name <email>"` is git's: the *author* of this one
commit (`--date` sets its date); the committer stays you. A bare `--author name` is the
identity flag every repo command shares (who this invocation acts as).

### Inspecting

#### `cairn status [branch]` — `-s` `--porcelain` `-b` `-z`
Report a line's state: the working change versus its parent commit, file-by-file
(`M`/`A`/`D`), plus how far ahead the line is. Defaults to the root line.

`-s`/`--short` and `--porcelain[=v1]` print git's stable `XY path` lines instead (`-z` for
NUL-terminated, `-b` for the `## line...origin/line [ahead N, behind M]` header, `-- <path>`
to filter). cairn has no index, so there is one comparison — working change vs sealed tip —
and it is reported in the **X** column, exactly as git reports staged changes, because that
is what the next `cairn commit` seals: `M ` modified, `A ` new, `D ` deleted, `UU` an
unresolved conflict, `!!` ignored (with `--ignored`). `??` never appears — a new, un-ignored
file is already in the working change. Renames show as a `D` and an `A`.

#### `cairn diff [branch]` / `cairn diff <a> <b>`
With zero/one argument: show the working change versus its parent for that line. With two
commit arguments: show the diff between two commits. The output is a git-format patch
(`diff --git a/… b/…`), so it can be fed to `git apply` / `patch -p1`.
```sh
cairn diff feature          # working change vs parent
cairn diff <shaA> <shaB>    # commit-to-commit
cairn diff HEAD~1           # the working tree vs a commit
cairn diff --stat main...feature
```
`--classic` prints cairn's original format instead (`--- <sha>/path` headers, `added: path`,
`Binary files differ: path`).

#### `cairn log [<range>]` — `-n <N>`
Show commit history in git's format (`commit`/`Author:`/`Date:`), from `HEAD` — the sealed tip
of the line you stand in — or from the named line/revision/range, following every parent. It is
unbounded (there is no pager): use `-n`, or pipe it.

The in-progress working change is not a commit, so it is not listed; `cairn log --working` puts
it first (marked `(working change)`) when it holds anything. `cairn log --classic [line] [-n N]`
is cairn's original compact view: one line per commit, first-parent, the working change on top
labelled `(working)`, 20 entries by default.

#### `cairn show [<rev>]`
Show one commit's message and its patch, as git does (default `HEAD`). `--classic` prints the
original cairn format.

#### `cairn blame <path> [branch]`
Per-line provenance for a file, in git's format (`<sha> (<author> <date> <n>) <line>`):
`-L <a>,<b>`, `-s -e -l -n -f -t --abbrev=<n> -w --porcelain --line-porcelain`; `-C`/`-M` are
accepted (renames are always followed). Lines still in an un-sealed working change show as
`Not Committed Yet`. `--classic` prints cairn's original author/date columns (`(working)`).

#### Git-shaped inspection

The inspection commands take git's spelling, options and output, so git habits and scripts
carry over. `log`, `show` and `diff` accept `--classic` for cairn's original output.

| Command | Options |
|---|---|
| `cairn log [<range>] [-- <path>...]` | `-L <a>,<b>:<file>` / `-L :<func>:<file>` (the history of a line range), `-m` (a diff per merge parent), `--no-abbrev --pretty=reference --decorate=full`, `--oneline --stat --shortstat --numstat -p --name-only --name-status --graph --format=/--pretty= --date= --decorate --author= --committer= --grep= -i -E -F --all-match --invert-grep --since/--after --until/--before -n/--max-count/-<N> --skip --reverse --first-parent --merges --no-merges --all --follow -S -G -U<n> -w -m` |
| `cairn show [<rev>[:<path>]...]` | `--stat --name-only --name-status -s --format= --oneline -m -c --cc`; a blob prints its content, a tree its listing |
| `cairn diff [<rev> \| <a> <b> \| <a>..<b> \| <a>...<b>] [-- <path>...]` | `-p --stat --numstat --shortstat --dirstat --name-only --name-status -U<n> -R -w --full-index --binary --color[=when] --word-diff[=plain\|color] --color-words --color-moved --exit-code --quiet -M[<n>] --no-renames` — renames are detected as git does by default (`R087 old new`, `{a => b}/f` in stats) |
| `cairn grep <pattern> [<rev>...] [-- <path>...]` | `-n -i -l -L -c -w -v -E -F -e -h -o -q -I -A/-B/-C` (exit 1 when nothing matches) |
| `cairn blame` | `-L <a>,<b>`; `cairn blame <rev> -- <path>` |
| `cairn ls-files` | `-c -m -d -o --exclude-standard -s -z --full-name --error-unmatch` |
| `cairn ls-tree <tree-ish> [<path>...]` | `-r -d -t -l --name-only -z --full-name --full-tree` |
| `cairn cat-file` | `-t -s -p -e <object>`, `<type> <object>`, `--batch-check`, `--batch` |
| `cairn rev-parse` | `<rev>... --verify -q --short[=n] --abbrev-ref --symbolic-full-name --show-toplevel --git-dir --show-prefix --is-inside-work-tree` |
| `cairn rev-list <range>...` | `--oneline --pretty[=<fmt>] --count -n --skip --reverse --all --first-parent --merges --no-merges --parents --topo-order --left-right --count a...b` |
| `cairn shortlog [<range>]` | `-s -n -e -c` |
| `cairn describe [<rev>]` | `--tags --always --abbrev=<n> --dirty[=mark] --long --exact-match --match <glob> --contains` — without `--tags` only annotated tags name a commit, as in git |
| `cairn merge-base` | `[--all] <a> <b>`, `--is-ancestor <a> <b>` (`--fork-point` is refused: cairn has an oplog, not git's reflog) |
| `cairn for-each-ref [<pattern>...]` | `--format=%(refname[:short]) %(objectname[:short]) %(objecttype) %(subject) %(authorname/email/date) %(committer…) %(tagger…) %(*objectname) %(HEAD) %(upstream)… --sort=[-]<key> --count=<n>` |
| `cairn symbolic-ref [--short] HEAD` | prints `refs/heads/<the line you stand in>`; setting it is refused (HEAD is a folder) |
| `cairn hash-object` | `[-w] [-t <type>] [--stdin\|--stdin-paths] [--literally] <file>...` |
| `cairn name-rev <rev>...` | `--tags --refs=<pat> --name-only --no-undefined --always` (`show-branch` is not provided: use `log --graph --oneline`) |
| `cairn status` | `--porcelain=v2` (with `-b` the `# branch.*` headers); a deleted and an added file that are the same file are listed as `R  old -> new` (`--no-renames` to turn off) |
| `cairn format-patch [-<N>] [<since> \| <range>]` | `-o <dir> --stdout --root -N -n --start-number --subject-prefix` — mbox patches `git am` applies |

**Revisions.** A commit can be named by full or abbreviated hash (an ambiguous abbreviation is
an error), a tag, a line name, `origin/<branch>`, or `HEAD`/`@`, with `~N`, `^`, `^N` suffixes
chained (`main~3^2`); `<rev>:<path>` names a file or directory, `<line>@{u}` its upstream.
`HEAD` — and a line name — is the line's **sealed** tip (the line whose folder you stand in,
else the root line). The open working change is the working tree: what `cairn diff <rev>`
compares *against* a revision, never a revision itself.

**Paths** are read relative to where you stand inside the line's folder, as git reads them
relative to the cwd; output paths follow the same rule.

**No index.** Everything on disk that is not ignored already belongs to the working change, so
`ls-files` lists the working change's tree, `-m`/`-d` its modifications/deletions against the
sealed tip, and `-o --exclude-standard` is always empty.

**Colour.** `--color[=always|never|auto]` / `--no-color`, else the `color.ui` config key, else
auto: a terminal only, never with `NO_COLOR` set. There is no pager.

**Not git's.** No rename detection (a rename shows as a delete plus an add; `log --follow`
does continue across one). `show` of a merge diffs against its first parent. Diffs are
minimal, so `--stat` counts can be slightly smaller than git's on heavily rewritten files.

Blame agrees with `git blame` on almost every line, but not byte-for-byte: cairn diffs
each parent/child pair with a Myers **minimal** edit script, while git's default xdiff
is a heuristic that may return a longer, differently placed one. Where several equally
short scripts exist (typically repeated lines such as `}` or blank lines) the two tools
can attribute a line to different commits; on real histories that is roughly 0.2-2% of
lines, occasionally including a unique line inside a heavy rewrite. Neither answer is
wrong. Passing `--minimal` (or `--diff-algorithm=myers --minimal`) to `git blame` does
not make it match, because the residue is tie-breaking, not script length (measured on
the test fixtures in `blame_git_test.go`). cairn does not emulate xdiff's tie-breaking.

### Conflicts

cairn never blocks on a conflict. A command that produces overlapping work records the
conflict on the file, finishes, and **exits with code `2`**. The file holds conflict
markers; `status` lists it.

You can keep working, but **`commit` refuses while a conflict is unresolved** (like git's
unmerged-paths block): you must edit out the `<<<<<<<` markers and `cairn resolve <path>`
each conflicted file first. This prevents a later commit from silently baking the marker
text into history and dropping the conflict.

#### `cairn resolve <branch> <path>` — `--force`
Mark a conflicted file resolved after you have edited it to the desired content. The
file's current on-disk bytes become the resolution — so `resolve` **refuses** while the
file still contains the `<<<<<<< / ======= / >>>>>>>` markers (otherwise the conflict
would vanish from `status` with the markers still in the file). Pass `--force` only if
the marker-like text is intentional content.
```sh
cairn status feature        # shows the conflicted path
$EDITOR feature/foo.txt      # remove the markers, keep what you want
cairn resolve feature foo.txt
cairn commit feature -m "resolve foo"
```
A **modify/delete** conflict — you removed a file the parent line changed — is resolved
the same way, by the file's absence: leave it deleted and run `resolve`, and the removal
becomes the resolution.
```sh
rm feature/foo.txt           # (already gone if you deleted it)
cairn resolve feature foo.txt   # resolves in favour of the deletion
cairn commit feature -m "drop foo"
```
To keep the file instead, restore the parent's content and `resolve` normally.

### Undo & history

#### `cairn undo`
Revert the most recent operation — commit, merge, fold, history edit, anything in the
op-log. Repeatable.

#### `cairn oplog`
Show the operation history (the list `undo` walks back through). A burst of auto-snapshots
coalesces into a single undoable step.

### History editing

Safe, discrete history rewrites — no interactive rebase. Commits after the edit point on the
same line are auto-rebased (commits before it are untouched — same hashes); `reword`/`squash`
stay conflict-free, `drop` records any conflict as data.

**One rule for every history edit** — `reword`, `squash`, `drop`, `commit --amend`,
`reset <rev>`. With "the rewritten commits" meaning the edit point and everything after it on
the line (for `reset`, everything the line would lose), the edit is refused when:

- the line is folded or abandoned;
- a rewritten commit came in with a **fold** — folded history is immutable;
- an open **child line is based on** a rewritten commit — its foundation would move. The error
  names the child. A child forked from an *older* commit is unaffected and does not block;
- on the **root line**, a rewritten commit is **already on the remote** (reachable from the
  line's remote-tracking ref as of the last fetch/push). The root line is never force-pushed,
  so its published history is only added to — use `cairn revert`. Un-pushed root commits are
  freely editable.

On a non-root line a pushed commit may be rewritten; cairn warns that the next push needs
`--force`.

#### `cairn reword <commit> <message>`
Change the message of a sealed commit.

#### `cairn squash <commit>`
Fold a sealed commit into its parent (combine two commits into one).

#### `cairn drop <commit>`
Remove a sealed commit from its line.

#### `cairn cherry-pick <commit|A..B>... [branch]` — `-n` `-x`
Apply commits from another line onto `<branch>` (default: the current/root line), each as a
new sealed commit with a fresh identity. Your in-progress working change is kept separate and
rebased on top. Several commits and `A..B` ranges are applied in order; `-x` appends git's
`(cherry picked from commit …)`; `-n` leaves the pick un-sealed in the working change.
Conflicts are recorded as data: the run stops (exit `2`), you `resolve` and `commit`.
`--continue` is that commit (it needs no message), `--abort` rewinds the pick; there is no
sequencer, so after a stop re-run with the commits still to apply.

#### `cairn revert <commit>...` — `-n` `-m <parent>`
Add a new sealed commit that takes `<commit>` back out, with git's message (`Revert
"<subject>"` / `This reverts commit <sha>.`). Works on any line, the root included. A revert
that does not apply cleanly is left in the working change with the conflict recorded (exit
`2`): `resolve`, then `cairn commit` — the revert message is kept — or `cairn revert --abort`.

#### `cairn reset --hard` / `cairn reset [--soft|--hard] <rev>`
`reset --hard` discards the line's whole working change (any line; undoable). With `<rev>` it
moves the line's sealed tip back: `--soft`/`--mixed` (the same thing — no index) keep the
files, so the dropped commits' changes become the working change; `--hard` rewrites the
folder. `<rev>` must be an ancestor of the tip at or above the line's base, and — as a history
edit — it follows [the one rule](#history-editing): on the root line it cannot drop commits
that are already on the remote, and nowhere can it drop a commit a child line is based on. On
a non-root line cairn warns when the remote still has the dropped commits (the next push
needs `--force`). `reset <path>` is a
no-op: there is nothing to unstage.

#### `cairn reauthor --old-email <glob> [--old-name <glob>] --name <new> --email <new> [--dry-run]`
Bulk-rewrite the author **and** committer identity of every matching commit across the
**whole repo** — every line, the root included — like `git filter-repo`'s mailmap. Unlike
`reword`/`squash`/`drop`, this is *not* bounded to a leaf line: it exists precisely to fix
attribution everywhere at once (e.g. commits made before you ran `cairn setup` that carry the
`cairn <name@users.noreply.cairn>` placeholder).

- **Match** the old identity by `--old-email` and/or `--old-name`. Both accept glob syntax
  (`*@users.noreply.cairn` catches every placeholder). At least one filter is required —
  reauthor refuses to match the entire history by accident.
- **Set** the new `--name` and/or `--email` (omit one to leave it unchanged).
- **Preserves** each commit's tree, message, and original timestamp exactly — only identity
  and the resulting parent links change. Because changing identity changes a commit's hash,
  every descendant is transparently rebuilt onto the rewritten parent and all internal
  references (line tips, tags, stashes, bisect state) are remapped in one transaction.
- `--dry-run` reports how many commits would change without writing anything.

```sh
# Fix every placeholder commit in the repo, in one shot:
cairn reauthor --old-email '*@users.noreply.cairn' \
  --name "Jacinta" --email "jacinta@darksoft.co.nz"

# Preview first:
cairn reauthor --old-name cairn --name "Jacinta" --email me@x.io --dry-run
```

After a reauthor, `push --force` to publish the rewritten history to a remote (the commit
SHAs changed, so it is not a fast-forward).

### Stash

A shelve stack for the rare case the always-saved working copy and multiple expressed
folders don't already cover. A stash is restored with a three-way merge, so it applies over
un-sealed edits and onto a line that has moved; a conflict is recorded (exit `2`) and the
stash kept. An `id` is the number `stash list` prints, or git's `stash@{N}`.

| Command | Action |
|---------|--------|
| `cairn stash [push] [-m <msg>] [branch] [-- <path>...]` | Shelve the working change (or only the edits under `<path>`); `stash save <msg>` also works |
| `cairn stash pop [branch] [id]` | Restore a stash onto `branch` and drop it (default: most recent) |
| `cairn stash apply [branch] [id]` | Restore a stash, keeping it on the stack |
| `cairn stash list` / `show [-p] [id]` | List the stack / show what an entry holds |
| `cairn stash drop [id]` / `clear` | Discard one stash (default: most recent) / all of them |
| `cairn stash branch <name> [id]` | Express a new line off the stash's line and pop the stash onto it |

### Working-copy verbs

These act on the line whose folder you are in (or `--branch <line>`), take paths relative to
where you stand, and are each one `cairn undo` step.

| Command | Action |
|---------|--------|
| `cairn restore [--source=<rev>] <path>...` | Discard working-change edits to paths: a deleted file comes back, a file the source lacks is removed. Also `cairn checkout [<rev>] -- <path>...` |
| `cairn rm [-r] [-f] [-n] <path>...` | Remove files from the line and the folder (`-f` if they hold un-sealed changes) |
| `cairn mv [-f] [-n] <src>... <dst>` | Rename files/directories in the line |
| `cairn apply [--check] [-R] [--stat] [-p<n>] <patch>\|-` | Apply a unified (git) diff to the working change, all or nothing |
| `cairn am [<mbox>...\|-]` | Apply `git format-patch` output as sealed commits (author, date, message preserved) |
| `cairn clean -fdx [-n] [<path>...]` | Remove **ignored** files (build output, caches) |
| `cairn add <anything>` | Succeeds and does nothing — there is no staging area — so `add -A && commit` scripts work |

**`clean` is not git's clean.** git removes *untracked* files; cairn has none — a new file
that is not ignored is already part of the working change (your new source, not junk), and
discarding it is `restore`/`reset --hard`, which are undoable. So `clean` only ever removes
files matched by `.gitignore`/`.cairnignore`: `-x` (or `-X`, the same here) selects them, `-d`
includes wholly-ignored directories (never one holding a nested repository), `-f` is required
(or `-n` to preview). Without `-x` there is nothing to remove and it says so. Ignored files
were never in cairn's store, so a clean — alone among these — cannot be undone.

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

#### `cairn remote [-v]` / `cairn remote add <name> <url>` — `--cairn` `-f`
`remote` lists names (`-v`: fetch/push URLs, like git). Also: `remote remove|rm <name>`,
`remote rename <old> <new>` (tracking refs move), `remote get-url <name>`,
`remote set-url <name> <url>`, `remote show [-n] <name>` (URL, HEAD, branches, how each line
stands), `remote prune [-n] <name>`. Remove/rename/set-url are oplog entries, so `cairn undo`
reverts them (tracking refs of a removed remote come back with the next fetch). A cairn remote
has exactly one URL: `set-url --push/--add/--delete` are refused.

`remote add` registers one. `--cairn` marks it a cairn remote, so `push` writes the
full-fidelity `refs/cairn/meta` (line tree + change-ids + open conflicts). Without it, a
remote is treated as plain git and receives the flat projection.
```sh
cairn remote add origin https://github.com/me/proj.git
cairn remote add team git@host:team/proj.git --cairn
```

#### `cairn push [remote] [refspec...]` — `--force` `--all` `--reconcile`
git's other spellings: `push origin feat:release` (a line under another remote branch name),
`--tags` (only tags), `--delete origin <branch|tag>` / `push origin :<ref>` (delete on the
remote; a name that is both a branch and a tag must be written `refs/heads/x`/`refs/tags/x`),
`--force-with-lease` (refuses unless the remote still equals your last fetch), `-n/--dry-run`
(reports from the last-fetched state, sends nothing), `-q`; `-u/--set-upstream` is accepted
(a line always tracks the same-named remote branch). Annotated tags are published as tag
objects.

Publish to `origin` (default). By default push publishes **only the line you're standing in**
(like git pushes the current branch) — so a push from inside a feature folder never touches
`main`. Pass an explicit `branch`, or `--all` to publish every line:
```sh
cd feat && cairn push            # publishes just 'feat'
cairn push origin feat           # explicit single line
cairn push --all                 # every line + tags
cd feat && cairn push --reconcile  # single-line: pull+retry just this line on divergence
```
Only **sealed** commits are published — the auto-snapshot working state is local, like git's
working tree, and never pushed. `--force` overwrites a diverged remote branch; the all-lines
push auto-pulls + retries once on divergence. A single-line push does NOT auto-reconcile — a
diverged remote branch surfaces a guided error naming the branch and the remedies: `cairn push
--reconcile` (pull + retry just this line), `cairn pull` (reconciles ALL lines) then push, or
`cairn push --force` to overwrite. `--reconcile` is single-line only — it is rejected together
with `--all` or `--force`.

**Conflict gate.** A line with an open conflict (a reconcile merge left unresolved) is refused
on push to a plain git remote — its file content is literal diff3 conflict markers, and a git
remote has no way to represent "conflicted", so publishing it would silently ship broken text.
The error names the branch; resolve with `cairn resolve <branch> <path>` then push again, or
`cairn undo` to rewind the reconcile merge, or `--force` to publish the markers anyway. This
gate does not apply to a `--cairn` remote: conflicts-as-data travels with the push there by
design, the same as any other change-graph state.

#### `cairn fetch [remote] [branch...]`
Fetch a remote into tracking refs (default `origin`) without reconciling. `--all` fetches
every remote; `<remote> <branch>` only those branches; `-q`. Fetch always prunes and takes
tags, so `--prune`/`--tags` are accepted.

#### `cairn pull [remote] [branch]`
Fetch and reconcile each line (default `origin`), or just `<branch>`. `--rebase`,
`--no-rebase`, `--ff`, `--no-ff` are accepted and change nothing: cairn already replays each
line's unpushed commits onto its moved parent and merges a diverged remote line with the
conflicts kept as data. `--ff-only` refuses (before touching anything) if any line has
diverged from its remote branch. `-q` prints only conflicts.

### Pull requests

`cairn pr` talks to cairn-**server**'s gRPC `PullService` directly — a separate transport
from `push`/`fetch`/`pull` above (which speak git/cairn-native protocols to a remote).
Opening a PR files a linked tracking issue in the ledger on your behalf; merging is
**fast-forward only** (cairn never generates a merge commit) — a diverged source is
rejected with guidance to rebase first, same as a protected-branch push rejection.

#### `cairn pr open <source> <target> -m <title> --project <key>`
Open a pull request from `source` into `target`. **Idempotent**: reopening the same
`(repo, source, target)` returns the existing open PR unchanged instead of filing a
duplicate ledger issue. `--description` and `--dod` (definition of done) are optional.
```sh
cairn pr open feature main -m "Add X" --project WID
```

#### `cairn pr list` — `--state open|merged|all`
List the repo's pull requests (default `--state open`).

#### `cairn pr view <id>`
Show one pull request's id, state, branches, title, and linked ledger issue key.

#### `cairn pr diff <id>` — `--repo <dir>` `--remote <name>`
Print the pull request's unified diff to stdout — the `gh pr diff` equivalent, suitable
for piping into a review/judge gate. This is the one `pr` verb that is **not** a pure
gRPC call: it resolves the PR's branch names via `pr view`, then **prune-fetches**
`--remote`'s tracking refs into `--repo` (default `.`; read-only, no reconcile) and diffs
**locally** from those tracking refs — so it works even in a clone where neither line
was ever `express`ed. `--remote` (default `origin`) must be a git remote of `--repo` that
addresses the **same** repo the gRPC server (`--org`/`--repo-slug`) is addressing — cairn
does not itself correlate a gRPC org/slug to a git remote URL, so pick the remote name
deliberately in v1.
```sh
cairn pr diff a1b2c3 --repo . --remote origin
```
**Semantics: `target...source` (merge-base), not a literal tip-to-tip diff.** The output
is the merge-base of `target` and `source` diffed against `source`'s tip — exactly what
`target` gained on `source`'s line since it forked, and nothing else. This matters once
`target` has moved on: if `target` picks up commits `source` never saw, a naive tip-to-tip
diff would show them as spurious deletions (reverting content `source` simply never had).
`cairn diff <a> <b>` (the plain two-ref form, [above](#inspecting)) is unaffected — it
stays a literal tip-to-tip diff, by design.

`target` and `source` sharing no history at all (unrelated branches) is a clear error, not
an empty or nonsensical diff. The tracking-ref fetch **prunes** stale refs, so if the PR's
source (or target) branch has since been deleted on the remote, `pr diff` fails clearly
instead of silently diffing against its last-known (now-deleted) tip.

#### `cairn pr merge <id>`
Fast-forward-merge an open pull request and best-effort comment the linked ledger issue.
A diverged source fails with the server's exact guidance, e.g.:
```
not fast-forwardable; rebase feature onto main
```

#### Connection & identity (every `pr` verb)

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `--org` | `CAIRN_ORG` | `pr.org` global config | Org the repo belongs to (**required**) |
| `--repo-slug` | `CAIRN_REPO_SLUG` | `pr.repo-slug` global config | Repo slug (**required**) |
| `--server` | `CAIRN_GRPC_ADDR` | `127.0.0.1:8102` | cairn-server's gRPC address |
| `--subject` | `CAIRN_SUBJECT` | configured `user.email` | Caller identity, forwarded as `cwb-subject` |
| `--tls-cert` / `--tls-key` / `--tls-ca` | `CAIRN_TLS_CERT` / `CAIRN_TLS_KEY` / `CAIRN_TLS_CA` | — | mTLS client cert/key + the `cwb-ca` — same trio cairn-server itself uses to dial ledger/herald |
| `--insecure` | `CAIRN_DEV_INSECURE=1` | off | Skip mTLS (local dev only; cairn-server must also opt in with its own `CAIRN_DEV_INSECURE=1`) |

The channel is **mTLS by default**: `pr` dials cairn-server's gRPC API directly and
carries your identity as `cwb-*` gRPC metadata (the same mechanism the gateway injects
for in-cluster callers) — set `--org`/`--subject` (or their config/env defaults) so the
server can authorize the call; a request missing identity is rejected with
`Unauthenticated`, and one for the wrong org/without the required scope with
`PermissionDenied`. Set `--org`/`--repo-slug` once with `cairn config --global pr.org
<org>` and `cairn config --global pr.repo-slug <slug>` to avoid repeating them.

### Privacy (withholding from pushes)

Keep a path **out of every push** — the way you'd keep secrets out of a repo, but tracked and
enforced instead of just hoped-for. The withheld file stays **real on disk and in your local
commits** (you need the secret to run the thing); cairn strips it from whatever it publishes, so
**no withheld byte ever reaches a remote** — never in plaintext, never as ciphertext. A flag
covers the path **and everything beneath it**, and applies **retroactively** (the path is redacted
from all history on push, not just new commits).

This is the local-over-git behaviour. Per-identity gating, embargo, and recoverable-but-redacted
projections are deferred **cairn-server** features (see the convergence-core spec §6); over a plain
git (or even cairn) remote, private simply means *not shipped*.

#### `cairn private <path> [--shape-only]`
Withhold `<path>` (and its subtree) from every push. Default is **omit** — the path is absent from
the pushed repo entirely (no name, no bytes), like an enforced `.gitignore`. `--shape-only` keeps
the path but replaces its bytes with a `<<private>>` placeholder (useful when you want the public
projection to *show* that a secret belongs there).
```sh
cairn private secrets            # the secrets/ folder never leaves your machine
cairn private config/prod.env    # a single file
cairn private docs --shape-only  # docs/ paths visible, contents withheld
```
If the path is **already on a remote** (it's in a remote-tracking ref — i.e. you cloned it, or
pushed it before withholding), `cairn private` prints a **warning**: withholding only stops *future*
pushes from carrying it — the copy already on the remote is **not removed** (it lingers as a
recoverable object and exists in any clones/forks). **Rotate the secret.** This is the same hard
truth as `git filter-repo` + force-push: a pushed secret is compromised.

#### `cairn private ls`
List withheld paths and their modes.

#### `cairn embargo <commit>` / `cairn embargo ls`
Hold a commit — and **everything after it** — out of the **public** projection, while still
intending to *distribute* it. This is **distinct from `private`**: a private path is a secret that
is *never* pushed; an embargoed commit is content you *do* ship, just **gated and not-yet-public**.
On a push to a plain git remote, the public tip is **frozen at the commit before the embargo**, so
the embargoed commits are held back. The patch-gap (NEX-25): land a security fix, embargo it, and a
cairn server hands the *real* bytes to authorized recipients **now** while the public repo stays
frozen — so it can't be patch-diffed into an n-day before customers are protected.
```sh
cairn commit main -m "fix CVE-..."   # seal the fix
cairn embargo <its-sha>              # held out of public pushes until disclosed
cairn embargo ls                     # list embargoed commits
```
> Gated distribution of embargoed content to a **cairn server** (real-to-authorized) is the server
> tier (in progress). For now an embargo push to a *cairn* remote is refused (to avoid leaking it
> via `refs/cairn/*`); the public-freeze applies to plain git remotes.

#### `cairn disclose <path|commit>`
Make something public again. If the argument is an **embargoed commit**, it **lifts the embargo**
(the public tip advances on the next push). Otherwise it **stops withholding a private path** (the
next push includes its real content). (A secret already pushed before you withheld it is already on
the remote — withhold *before* the first push.)

> **What's redacted:** every pushed surface — `refs/heads/*` (sealed history), `refs/tags/*`,
> and, for a cairn remote, the live working snapshots (`refs/cairn/change/*`) and the change-graph
> metadata (`refs/cairn/meta`). The redaction is a push-time projection: your local refs and the
> object store are never altered, so the next ordinary command sees real content. With no privacy
> flags set, a push is byte-for-byte identical to one without this feature.

### Versioning & release

cairn derives versions from the change-graph rather than hand-typed numbers.

#### `cairn tag`
`tag [-l [pattern]] [-n[N]] [--contains <rev>] [--points-at <rev>] [--sort=<key>] [--format=…]`
lists tags (refname order like git; sort keys `refname`, `creatordate`, `version:refname`,
each reversible with `-`). `tag [-a] [-m <msg>|-F <file>] [-f] <name> [<rev>]` tags any commit
— `<rev>` is a line name (its tip; default: the line you stand in), a sha, another tag or
`origin/x`, with `~N`/`^` suffixes; a message makes it an annotated tag object. `tag -d <name>…`
deletes. Create/move/delete are oplog entries (`cairn undo`). Signed tags (`-s/-u`) are refused.

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

#### `cairn update` — `--check` `--force`
Replace this binary in place with the latest published cairn release (repo-free —
it never touches a working copy). Queries GitHub releases, compares against the
build version (`cairn --version`), downloads the matching platform archive,
verifies it against the release's `checksums.txt` (SHA-256), and atomically swaps
the executable. `--check` only reports whether a newer release exists; `--force`
reinstalls the latest release even when this build is not older (and is required
for source builds, which report version `dev`). If the binary lives in a
root-owned directory, run `sudo cairn update`. An API token is optional (the repo
is public) and resolves like push auth: `CAIRN_TOKEN` > `GITHUB_TOKEN` > credstore.
```sh
cairn update --check       # "0.1.20 is available (running 0.1.19)"
cairn update               # download, verify, swap; no-op if already current
```

### Refs & maintenance

- `cairn branch` — git's `branch` over lines. Bare/`--list [pattern]` lists (`*` = the line you
  stand in, `+` = other expressed lines); `-v`/`-vv` add the tip + subject and the upstream
  state (`[origin/x: ahead 1]`); `-a`/`-r` include remote-tracking branches; `--merged`,
  `--no-merged`, `--contains <rev>` use cairn's own graph walk; `--format`, `--show-current`.
  `branch <name> [<start>]` creates (= `express [--from]`); `-d` (refuses unfolded, unpushed
  work) / `-D` delete (= `abandon`); `-m`/`-M [<old>] <new>` rename a line and its folder
  (children follow; one oplog entry); `-c` copy; `-u/--set-upstream-to` is accepted as a no-op.
- `cairn reflog [show] [<line>] [-n N]` — the oplog through one line: `<sha> <line>@{N}: <op>` (`HEAD@{N}` for the line you stand in). `HEAD@{N}` and `<line>@{N}` also
  work as revisions (`rev-parse HEAD@{1}`): where the line's tip was N operations ago.
- `cairn gc [--prune=now] [-q]` — repack objects into one pack, drop loose copies, vacuum +
  analyze the catalogue, prune stale caches. Everything the catalogue refers to (undo targets,
  working snapshots, stashes) counts as reachable; unreferenced loose objects and superseded
  packs/caches go only after a 14-day grace (`--prune=now` drops it). `--aggressive` accepted.
- `cairn fsck [-v]` — verify every commit/tree/blob reachable from line tips, open changes and
  tags exists, and the catalogue is consistent; exit 1 on problems.
- `cairn count-objects [-v] [-H]` — loose/packed object counts.
- `cairn archive [--format=tar|tar.gz|zip] [-o <file>] [--prefix=<p>/] <rev> [<path>...]`.
- `cairn check-ignore [-v] [-n] <path>...` — which paths the line's `.gitignore`/`.cairnignore`
  ignore (the rules the scan applies); exit 1 when none.
- `cairn help [<verb>]` / `cairn <verb> -h` — usage section (and flags) for one verb.

### Misc

- `cairn --version` / `cairn -v` — print the **build** version of the binary (the release
  you installed), distinct from the `version` subcommand above which derives the repo's version.
- `cairn help` / `cairn -h` / `cairn --help` — print the usage summary.

## Exit codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `2` | Completed, but recorded one or more conflicts (e.g. `commit`/`pull`) — so `cairn commit && cairn push` won't push a conflicted state |
| `1` | Hard error (bad arguments, not a cairn repo, auth failure, …) | Also the status-only "no": `diff --exit-code`/`--quiet` found differences, `grep` matched nothing, `cat-file -e` / `rev-parse --verify -q` found no such object (nothing is printed) |

## Coming from git

| You'd reach for… | In cairn |
|------------------|----------|
| `git init` | `cairn init` |
| `git clone` | `cairn clone` |
| `git checkout -b feat` | `cairn express feat --from main` |
| `git add` + `git commit -m` | just edit, then `cairn commit <line> -m` (no staging; `cairn add` is an accepted no-op) |
| `git add -p` / staging some files | `cairn commit -m <msg> -- <path>...` (seals only those paths) |
| `git restore` / `git checkout -- <path>` | `cairn restore <path>` |
| `git rm` / `git mv` / `git apply` / `git am` | `cairn rm` / `mv` / `apply` / `am` |
| `git clean -fdx` | `cairn clean -fdx` (ignored files only — see above) |
| `git revert` | `cairn revert` |
| `git status` / `git diff` | `cairn status` / `cairn diff` |
| `git log` / `git show` / `git blame` | `cairn log` / `cairn show` / `cairn blame` |
| `git stash` | `cairn stash` (often unneeded — work is always saved + you can express many folders) |
| `git commit --amend` / interactive rebase | `cairn commit --amend` / `reword` / `squash` / `drop` (any commit nobody else stands on — see [History editing](#history-editing)) |
| `git cherry-pick` | `cairn cherry-pick` |
| `git bisect` | `cairn bisect` |
| `git merge` (into parent) | `cairn fold` |
| `git reset --hard` | `cairn reset --hard` (discard the working change) |
| `git reset [--soft\|--hard] <rev>` / `git reflog` + reset | `cairn reset <rev>` (un-pushed commits on `main`; any on your own line); `cairn undo` walks the op-log |
| `git push` / `git fetch` / `git pull` | `cairn push` / `fetch` / `pull` |
| `git branch [-d/-m/-v]` / `git tag` / `git remote` | `cairn branch` / `tag` / `remote` (same flags) |
| `git reflog` / `git gc` / `git fsck` / `git archive` | `cairn reflog` / `gc` / `fsck` / `archive` |
| resolving a merge conflict (blocks you) | keep working; `cairn resolve <line> <path>` when ready |

### Git spellings cairn answers to

Where a git verb means exactly what a cairn verb means, cairn accepts the git
spelling as a plain alias — same command, same flags, no behaviour of its own:

| Git spelling | Runs |
|---|---|
| `cairn merge <branch>` | `cairn fold <branch>` |
| `cairn branch <name> [<start>]` | `cairn express <name> [--from <start>]` |
| `cairn checkout -b <name>` | `cairn express <name>` |
| `cairn switch -c <name>` | `cairn express <name>` |
| `cairn checkout [<rev>] -- <path>...` | `cairn restore [--source=<rev>] <path>...` |

Where a git verb has **no** cairn equivalent, cairn refuses to fake it and
prints the translation instead of a usage dump — `rebase`, `worktree`, and
`checkout`/`switch` onto an existing branch each explain what to run in its
place, as do the index-only forms of the verbs above (`add -p`, `rm --cached`). (`git checkout -b <name> <start-point>`
is one of these: it names a parent, so cairn points you at
`cairn express <name> --from <start-point>` rather than silently forking off the
wrong line.)
