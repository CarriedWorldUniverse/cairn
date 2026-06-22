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
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cairn:", err)
		os.Exit(1)
	}
}

const usage = `cairn — cairn working-copy CLI

usage: cairn <subcommand> [flags] [args]

subcommands:
  init [dir]                bootstrap a repo (expresses main)
  express <branch>          materialize a branch folder (--from <parent>)
  unexpress <branch>        remove a branch folder
  commit <branch> [-m msg]  snapshot a branch folder onto its change
  fold <branch>             fold a branch into its parent
  abandon <branch>          discard a branch's line
  status [branch]           report a branch's state (default main)
  tree                      print the line tree
  ls                        list expressed branches
  resolve <path>            resolve a conflict (--branch <branch>)

common flags (repo subcommands): --repo <dir> (default .), --author <name>`

// run dispatches a subcommand. args is os.Args[1:].
func run(args []string) error {
	if len(args) == 0 {
		fmt.Println(usage)
		return errors.New("no subcommand")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	case "init":
		return cmdInit(rest)
	case "express":
		return cmdExpress(rest)
	case "unexpress":
		return cmdUnexpress(rest)
	case "commit":
		return cmdCommit(rest)
	case "fold":
		return cmdFold(rest)
	case "abandon":
		return cmdAbandon(rest)
	case "status":
		return cmdStatus(rest)
	case "tree":
		return cmdTree(rest)
	case "ls":
		return cmdLs(rest)
	case "resolve":
		return cmdResolve(rest)
	default:
		fmt.Println(usage)
		return fmt.Errorf("unknown subcommand %q", sub)
	}
}

// defaultAuthor resolves the commit author from the environment.
func defaultAuthor() string {
	if a := os.Getenv("CAIRN_AUTHOR"); a != "" {
		return a
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "cairn"
}

// openRepo wires the common --repo/--author flags onto fs and opens the repo.
// Call after fs.Parse.
func openRepo(repo, author string) (*worktree.Repo, error) {
	return worktree.Open(repo, author)
}

// repoFlags registers --repo and --author on fs, returning the bound vars.
func repoFlags(fs *flag.FlagSet) (repo, author *string) {
	repo = fs.String("repo", ".", "repo root directory")
	author = fs.String("author", defaultAuthor(), "commit author")
	return repo, author
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	author := fs.String("author", defaultAuthor(), "commit author")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}
	r, err := worktree.Open(dir, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	fmt.Printf("initialized cairn repo at %s\n", dir)
	return nil
}

func cmdExpress(args []string) error {
	fs := flag.NewFlagSet("express", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	from := fs.String("from", "", "parent branch to fork from")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	branch := fs.Arg(0)
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	if err := r.Express(branch, *from); err != nil {
		return mapErr(err)
	}
	fmt.Printf("%s/%s\n", *repo, branch)
	return nil
}

func cmdUnexpress(args []string) error {
	fs := flag.NewFlagSet("unexpress", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.Unexpress(fs.Arg(0)))
}

func cmdCommit(args []string) error {
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	msg := fs.String("m", "", "commit message")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	branch := fs.Arg(0)
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	res, err := r.Commit(branch, *msg)
	if err != nil {
		return mapErr(err)
	}
	if len(res.Conflicts) > 0 {
		paths := make([]string, 0, len(res.Conflicts))
		for _, c := range res.Conflicts {
			paths = append(paths, c.Path)
		}
		fmt.Fprintf(os.Stderr, "%d conflict(s) in: %s\n", len(res.Conflicts), strings.Join(paths, ", "))
		return nil
	}
	fmt.Println(res.HeadCommit)
	return nil
}

func cmdFold(args []string) error {
	fs := flag.NewFlagSet("fold", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.Fold(fs.Arg(0)))
}

func cmdAbandon(args []string) error {
	fs := flag.NewFlagSet("abandon", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("branch required")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.Abandon(fs.Arg(0)))
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	branch := change.RootLineName
	if fs.NArg() > 0 {
		branch = fs.Arg(0)
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	st, err := r.Status(branch)
	if err != nil {
		return mapErr(err)
	}
	fmt.Printf("branch:    %s\n", st.Branch)
	fmt.Printf("lineage:   %s\n", strings.Join(st.Lineage, " → "))
	fmt.Printf("ahead:     %d\n", st.Ahead)
	fmt.Printf("conflicts: %s\n", strings.Join(st.Conflicts, ", "))
	fmt.Printf("expressed: %s\n", strings.Join(st.Expressed, ", "))
	return nil
}

func cmdTree(args []string) error {
	fs := flag.NewFlagSet("tree", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	nodes, err := r.Tree()
	if err != nil {
		return mapErr(err)
	}
	for _, n := range nodes {
		fmt.Printf("%s (parent %s) ahead=%d\n", n.Line.Name, n.Parent, n.Ahead)
	}
	return nil
}

func cmdLs(args []string) error {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	for branch, entry := range r.Ls() {
		fmt.Printf("%s  %s\n", branch, entry.ChangeID)
	}
	return nil
}

func cmdResolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	branch := fs.String("branch", change.RootLineName, "branch to resolve on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return errors.New("path required")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.Resolve(*branch, fs.Arg(0)))
}

// mapErr translates change-engine sentinels into operator-facing messages.
func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, change.ErrHasConflict):
		return errors.New("resolve conflicts before folding")
	case errors.Is(err, change.ErrNotFound):
		return errors.New("not found")
	default:
		return err
	}
}
