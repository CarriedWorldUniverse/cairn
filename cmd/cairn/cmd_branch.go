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

// cmdBranch is git's `branch` over cairn lines. With a name it creates a line
// (`branch <name>` = express, `branch <name> <start>` = express --from);
// otherwise it lists, deletes (-d/-D = abandon), renames (-m/-M), copies
// (-c/-C) or filters (--merged / --contains) lines. The graph questions use
// cairn's own generation-ordered walk.
func cmdBranch(args []string) error {
	fs := flag.NewFlagSet("branch", flag.ContinueOnError)
	repo, author := repoFlags(fs)
	var verbose int
	var list, all, remotes, del, delForce, move, moveForce, cp, cpForce, force, showCurrent, quiet, unsetUp, track, noTrack bool
	fs.BoolVar(&list, "list", false, "list lines")
	fs.BoolVar(&list, "l", false, "list lines")
	fs.BoolVar(&all, "a", false, "list local and remote-tracking branches")
	fs.BoolVar(&all, "all", false, "list local and remote-tracking branches")
	fs.BoolVar(&remotes, "r", false, "list remote-tracking branches")
	fs.BoolVar(&remotes, "remotes", false, "list remote-tracking branches")
	fs.BoolVar(&del, "d", false, "delete a line (refuses unfolded, unpushed work)")
	fs.BoolVar(&del, "delete", false, "delete a line")
	fs.BoolVar(&delForce, "D", false, "delete a line, discarding unfolded work")
	fs.BoolVar(&move, "m", false, "rename a line")
	fs.BoolVar(&move, "move", false, "rename a line")
	fs.BoolVar(&moveForce, "M", false, "rename a line, replacing an existing one")
	fs.BoolVar(&cp, "c", false, "copy a line")
	fs.BoolVar(&cp, "copy", false, "copy a line")
	fs.BoolVar(&cpForce, "C", false, "copy a line, replacing an existing one")
	fs.BoolVar(&force, "f", false, "force")
	fs.BoolVar(&force, "force", false, "force")
	fs.BoolVar(&showCurrent, "show-current", false, "print the current line")
	fs.BoolVar(&quiet, "q", false, "quiet")
	fs.BoolVar(&quiet, "quiet", false, "quiet")
	fs.BoolVar(&unsetUp, "unset-upstream", false, "no-op (cairn tracks the same-named branch)")
	fs.BoolVar(&track, "t", false, "no-op")
	fs.BoolVar(&track, "track", false, "no-op")
	fs.BoolVar(&noTrack, "no-track", false, "no-op")
	setUp := fs.String("set-upstream-to", "", "no-op (cairn tracks the same-named branch)")
	fs.StringVar(setUp, "u", "", "no-op (cairn tracks the same-named branch)")
	format := fs.String("format", "", "output format: %(refname:short) %(objectname:short) %(subject) %(HEAD) %(upstream:short) ...")
	fs.Bool("no-color", false, "accepted")
	fs.Bool("color", false, "accepted")
	fs.Bool("no-column", false, "accepted")

	rest, merged, hasMerged := takeOptionalRev(args, "merged")
	rest, noMerged, hasNoMerged := takeOptionalRev(rest, "no-merged")
	rest, contains, hasContains := takeOptionalRev(rest, "contains")
	rest, noContains, hasNoContains := takeOptionalRev(rest, "no-contains")
	rest = expandShortCluster(rest, "vladrDmMcCfq")
	// -v is a counter: pre-count, then strip so the flag package never sees it.
	var stripped []string
	for _, a := range rest {
		if a == "-v" || a == "--verbose" {
			verbose++
			continue
		}
		stripped = append(stripped, a)
	}
	if err := parseArgs(fs, stripped); err != nil {
		return err
	}
	pos := fs.Args()
	if *setUp != "" || unsetUp {
		fmt.Fprintln(os.Stderr, "cairn: a line always tracks the same-named branch on the remote; there is no upstream to set")
		return nil
	}

	r, err := openRepo(*repo, *author)
	if err != nil {
		return mapErr(err)
	}
	defer r.Close()
	cur, _ := r.DefaultBranch()
	if del || delForce || move || moveForce || cp || cpForce {
		// Mutations act on sealed + working state alike: snapshot first.
		if err := r.SyncWorking(); err != nil {
			return fmt.Errorf("snapshotting working copy: %w", err)
		}
	}

	switch {
	case showCurrent:
		if b, ok := r.CWDBranch(); ok {
			fmt.Println(b)
		} else {
			fmt.Println(cur)
		}
		return nil
	case del || delForce:
		if len(pos) == 0 {
			return errors.New("branch name required")
		}
		return branchDelete(r, pos, delForce || force, quiet)
	case move || moveForce:
		var oldName, newName string
		switch len(pos) {
		case 1:
			b, ok := r.CWDBranch()
			if !ok {
				return errors.New(`to rename a line from outside its folder, name both: "cairn branch -m <old> <new>"`)
			}
			oldName, newName = b, pos[0]
		case 2:
			oldName, newName = pos[0], pos[1]
		default:
			return errors.New("usage: cairn branch -m [<old>] <new>")
		}
		if err := r.RenameLine(oldName, newName, moveForce || force); err != nil {
			return mapErr(err)
		}
		return nil
	case cp || cpForce:
		var oldName, newName string
		switch len(pos) {
		case 1:
			oldName, newName = cur, pos[0]
		case 2:
			oldName, newName = pos[0], pos[1]
		default:
			return errors.New("usage: cairn branch -c [<old>] <new>")
		}
		if cpForce || force {
			return errors.New("-C/--force copy over an existing line is not supported; delete the target with 'cairn branch -D' first")
		}
		return mapErr(r.CopyLine(oldName, newName))
	}

	listing := list || all || remotes || verbose > 0 || hasMerged || hasNoMerged || hasContains || hasNoContains || *format != ""
	if !listing && len(pos) > 0 {
		if len(pos) > 2 {
			return errors.New("usage: cairn branch <name> [<start-point>]")
		}
		r.Close()
		ex := []string{"--repo", *repo, "--author", *author}
		if len(pos) == 2 {
			ex = append(ex, "--from", pos[1])
		}
		return cmdExpress(append(ex, pos[0]))
	}
	return branchList(r, branchListOpts{
		patterns: pos, all: all, remotes: remotes, verbose: verbose, format: *format, cur: cur,
		merged: merged, hasMerged: hasMerged, noMerged: noMerged, hasNoMerged: hasNoMerged,
		contains: contains, hasContains: hasContains, noContains: noContains, hasNoContains: hasNoContains,
	})
}

func branchDelete(r *worktree.Repo, names []string, force, quiet bool) error {
	// Both the folder --repo names and the one the process stands in count:
	// --repo can point elsewhere than the shell's cwd.
	standing := map[string]bool{}
	if b, ok := r.CWDBranch(); ok {
		standing[b] = true
	}
	if b, ok := branchFromDir(r, ".", r.Root()); ok {
		standing[b] = true
	}
	var errs []string
	for _, n := range names {
		if standing[n] {
			errs = append(errs, fmt.Sprintf("cannot delete the line %q: you are standing in its folder; cd out first", n))
			continue
		}
		tip, err := r.DeleteBranch(n, force)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if !quiet {
			if tip != "" {
				fmt.Printf("Deleted branch %s (was %s).\n", n, short7(tip))
			} else {
				fmt.Printf("Deleted branch %s.\n", n)
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "\n"))
	}
	return nil
}

type branchListOpts struct {
	patterns                                           []string
	all, remotes                                       bool
	verbose                                            int
	format, cur                                        string
	merged, noMerged, contains, noContains             []string
	hasMerged, hasNoMerged, hasContains, hasNoContains bool
}

type branchRow struct {
	display, name, tip, subject, upstream string
	marker                                string
	remote                                bool
}

func branchList(r *worktree.Repo, o branchListOpts) error {
	infos, err := r.Branches()
	if err != nil {
		return mapErr(err)
	}
	remoteName := r.RemoteLabel()
	var rows []branchRow
	if !o.remotes {
		for _, b := range infos {
			row := branchRow{display: b.Name, name: b.Name, tip: b.Tip, subject: b.Subject, marker: " "}
			switch {
			case b.Name == o.cur:
				row.marker = "*"
			case b.Expressed:
				row.marker = "+"
			}
			if b.Remote != nil && b.Remote.Kind != "unpushed" && remoteName != "" {
				row.upstream = remoteName + "/" + b.Name
				switch b.Remote.Kind {
				case "ahead":
					row.upstream += fmt.Sprintf(": ahead %d", b.Remote.Ahead)
				case "behind":
					row.upstream += fmt.Sprintf(": behind %d", b.Remote.Behind)
				case "diverged":
					row.upstream += fmt.Sprintf(": ahead %d, behind %d", b.Remote.Ahead, b.Remote.Behind)
				case "gone":
					row.upstream += ": gone"
				}
			}
			rows = append(rows, row)
		}
	}
	if o.all || o.remotes {
		tracking, err := r.TrackingBranches()
		if err != nil {
			return mapErr(err)
		}
		for rem, heads := range tracking {
			for short, sha := range heads {
				full := rem + "/" + short
				disp := full
				if o.all {
					disp = "remotes/" + full
				}
				rows = append(rows, branchRow{display: disp, name: full, tip: sha, subject: r.CommitSubject(sha), marker: " ", remote: true})
			}
		}
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
	mergedInto, err := resolve(o.merged)
	if err != nil {
		return err
	}
	noMergedInto, err := resolve(o.noMerged)
	if err != nil {
		return err
	}
	containsRev, err := resolve(o.contains)
	if err != nil {
		return err
	}
	noContainsRev, err := resolve(o.noContains)
	if err != nil {
		return err
	}
	keep := rows[:0]
	for _, row := range rows {
		ok, err := matchAnyGlob(o.patterns, row.name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		// Graph filters. A line with nothing sealed yet has no tip to place.
		anc := func(a, b string) (bool, error) {
			if a == "" || b == "" {
				return false, nil
			}
			return r.IsAncestor(a, b)
		}
		for _, into := range mergedInto {
			v, err := anc(row.tip, into)
			if err != nil {
				return mapErr(err)
			}
			if !v {
				ok = false
			}
		}
		for _, into := range noMergedInto {
			v, err := anc(row.tip, into)
			if err != nil {
				return mapErr(err)
			}
			if v {
				ok = false
			}
		}
		for _, c := range containsRev {
			v, err := anc(c, row.tip)
			if err != nil {
				return mapErr(err)
			}
			if !v {
				ok = false
			}
		}
		for _, c := range noContainsRev {
			v, err := anc(c, row.tip)
			if err != nil {
				return mapErr(err)
			}
			if v {
				ok = false
			}
		}
		if ok {
			keep = append(keep, row)
		}
	}
	rows = keep
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].remote != rows[j].remote {
			return !rows[i].remote
		}
		return rows[i].display < rows[j].display
	})

	if o.format != "" {
		for _, row := range rows {
			row := row
			line, err := expandFormat(o.format, func(f, mod string) (string, bool) {
				switch f {
				case "refname":
					if mod == "short" {
						return row.name, true
					}
					if row.remote {
						return "refs/remotes/" + row.name, true
					}
					return "refs/heads/" + row.name, true
				case "objectname":
					if mod == "short" {
						return short7(row.tip), true
					}
					return row.tip, true
				case "subject", "contents:subject":
					return row.subject, true
				case "HEAD":
					if row.marker == "*" {
						return "*", true
					}
					return " ", true
				case "upstream":
					up := row.upstream
					if i := strings.Index(up, ":"); i >= 0 {
						up = up[:i]
					}
					if mod == "short" || up == "" {
						return up, true
					}
					return "refs/remotes/" + up, true
				}
				return "", false
			})
			if err != nil {
				return err
			}
			fmt.Println(line)
		}
		return nil
	}
	width := 0
	for _, row := range rows {
		if len(row.display) > width {
			width = len(row.display)
		}
	}
	for _, row := range rows {
		if o.verbose == 0 {
			fmt.Printf("%s %s\n", row.marker, row.display)
			continue
		}
		extra := ""
		if o.verbose > 1 && row.upstream != "" {
			extra = "[" + row.upstream + "] "
		}
		fmt.Printf("%s %-*s %s %s%s\n", row.marker, width, row.display, short7(row.tip), extra, row.subject)
	}
	return nil
}
