package main

import (
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
)

// The git-shaped `remote` sub-verbs beyond add: remove/rm, rename, get-url,
// set-url, show, prune. (`remote add` and the listing live in cmd_remote.go.)

// remoteSub dispatches a `remote <verb>` other than add and the bare listing.
// handled is false when verb is not one of them.
func remoteSub(verb string, args []string) (handled bool, err error) {
	switch verb {
	case "remove", "rm":
		return true, cmdRemoteRemove(args)
	case "rename":
		return true, cmdRemoteRename(args)
	case "get-url":
		return true, cmdRemoteGetURL(args)
	case "set-url":
		return true, cmdRemoteSetURL(args)
	case "show":
		return true, cmdRemoteShow(args)
	case "prune":
		return true, cmdRemotePrune(args)
	}
	return false, nil
}

func remoteArgs(name string, args []string, min, max int, usage string) (*flag.FlagSet, *string, *string, error) {
	fs := flag.NewFlagSet("remote "+name, flag.ContinueOnError)
	repo, author := repoFlags(fs)
	if err := parseArgs(fs, args); err != nil {
		return nil, nil, nil, err
	}
	if fs.NArg() < min || fs.NArg() > max {
		return nil, nil, nil, errors.New("usage: cairn remote " + usage)
	}
	return fs, repo, author, nil
}

func cmdRemoteRemove(args []string) error {
	fs, repo, author, err := remoteArgs("remove", args, 1, 1, "remove <name>")
	if err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.RemoveRemote(fs.Arg(0)))
}

func cmdRemoteRename(args []string) error {
	fs, repo, author, err := remoteArgs("rename", args, 2, 2, "rename <old> <new>")
	if err != nil {
		return err
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.RenameRemote(fs.Arg(0), fs.Arg(1)))
}

func cmdRemoteGetURL(args []string) error {
	fs := flag.NewFlagSet("remote get-url", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	fs.Bool("push", false, "accepted (cairn has one URL per remote)")
	fs.Bool("all", false, "accepted")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cairn remote get-url <name>")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	rem, err := r.GetRemote(fs.Arg(0))
	if err != nil {
		return mapErr(err)
	}
	fmt.Println(redactURL(rem.URL))
	return nil
}

func cmdRemoteSetURL(args []string) error {
	fs := flag.NewFlagSet("remote set-url", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	push := fs.Bool("push", false, "")
	add := fs.Bool("add", false, "")
	del := fs.Bool("delete", false, "")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if *push || *add || *del {
		return errors.New("a cairn remote has exactly one URL (no separate push URL, no --add/--delete): use 'cairn remote set-url <name> <url>'")
	}
	if fs.NArg() != 2 {
		return errors.New("usage: cairn remote set-url <name> <url>")
	}
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	return mapErr(r.SetRemoteURL(fs.Arg(0), absLocalURL(fs.Arg(1))))
}

// cmdRemoteShow prints a remote's URL, its HEAD and branches (a live listing
// unless -n) and how each line stands against it.
func cmdRemoteShow(args []string) error {
	fs := flag.NewFlagSet("remote show", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	noQuery := fs.Bool("n", false, "do not query the remote; show the last-fetched state")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cairn remote show [-n] <name>")
	}
	name := fs.Arg(0)
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	rem, err := r.GetRemote(name)
	if err != nil {
		return mapErr(err)
	}
	u := redactURL(rem.URL)
	fmt.Printf("* remote %s\n  Fetch URL: %s\n  Push  URL: %s\n  Kind: %s\n", name, u, u, rem.Kind)
	tracking, err := r.TrackingBranches()
	if err != nil {
		return mapErr(err)
	}
	have := tracking[name]
	var heads []string
	head := "(not queried)"
	if *noQuery {
		for b := range have {
			heads = append(heads, b)
		}
	} else {
		refs, h, err := r.ListRemoteRefs(name)
		if err != nil {
			return mapRemoteErr(err)
		}
		head = h
		if head == "" {
			head = "(unknown)"
		}
		for ref := range refs {
			if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
				heads = append(heads, b)
			}
		}
	}
	sort.Strings(heads)
	fmt.Printf("  HEAD branch: %s\n", head)
	if len(heads) > 0 {
		fmt.Println("  Remote branches:")
		for _, b := range heads {
			st := "tracked"
			if _, ok := have[b]; !ok {
				st = "new (next fetch will store in remotes/" + name + ")"
			}
			fmt.Printf("    %s %s\n", b, st)
		}
	}
	if !*noQuery {
		var stale []string
		for b := range have {
			found := false
			for _, h := range heads {
				if h == b {
					found = true
				}
			}
			if !found {
				stale = append(stale, b)
			}
		}
		sort.Strings(stale)
		for _, b := range stale {
			fmt.Printf("    %s stale (use 'cairn remote prune %s' to remove)\n", b, name)
		}
	}
	if states, err := r.RemoteStatesFor(name); err == nil && len(states) > 0 {
		var lines []string
		for l := range states {
			lines = append(lines, l)
		}
		sort.Strings(lines)
		fmt.Println("  Lines configured for 'cairn push':")
		desc := map[string]string{
			"pushed": "up to date", "ahead": "fast-forwardable", "behind": "local out of date",
			"diverged": "diverged", "unpushed": "not pushed yet", "gone": "gone from the remote",
		}
		for _, l := range lines {
			fmt.Printf("    %s pushes to %s (%s)\n", l, l, desc[states[l].Kind])
		}
	}
	return nil
}

// cmdRemotePrune drops tracking refs for branches the remote no longer has.
func cmdRemotePrune(args []string) error {
	fs := flag.NewFlagSet("remote prune", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	dry := fs.Bool("dry-run", false, "report what would be pruned")
	fs.BoolVar(dry, "n", false, "report what would be pruned")
	if err := parseArgs(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: cairn remote prune [-n] <name>")
	}
	name := fs.Arg(0)
	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	rem, err := r.GetRemote(name)
	if err != nil {
		return mapErr(err)
	}
	before, err := r.TrackingBranches()
	if err != nil {
		return mapErr(err)
	}
	refs, _, err := r.ListRemoteRefs(name)
	if err != nil {
		return mapRemoteErr(err)
	}
	var stale []string
	for b := range before[name] {
		if _, ok := refs["refs/heads/"+b]; !ok {
			stale = append(stale, b)
		}
	}
	sort.Strings(stale)
	if len(stale) == 0 {
		return nil
	}
	fmt.Printf("Pruning %s\nURL: %s\n", name, redactURL(rem.URL))
	if !*dry {
		if err := r.Fetch(name); err != nil {
			return mapRemoteErr(err)
		}
	}
	verb := "pruned"
	if *dry {
		verb = "would prune"
	}
	for _, b := range stale {
		fmt.Printf(" * [%s] %s/%s\n", verb, name, b)
	}
	return nil
}
