package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/CarriedWorldUniverse/cairn/internal/worktree"
)

const zeroOID = "0000000000000000000000000000000000000000"

// gitModeString is an entry's mode as git prints it.
func gitModeString(m change.EntryMode) string {
	switch m {
	case change.ModeExecutable:
		return "100755"
	case change.ModeSymlink:
		return "120000"
	case change.ModeGitlink:
		return "160000"
	}
	return "100644"
}

// printPorcelainV2 writes git's `status --porcelain=v2`. As in the short
// format there is no index: the working change against the sealed tip is the
// one comparison, reported in the X column with the index fields equal to the
// worktree's (what git shows for a change that is already staged).
func printPorcelainV2(r *worktree.Repo, st worktree.StatusInfo, specs []string, showBranch, nul, ignored, noRenames bool) error {
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
	end := "\n"
	if nul {
		end = "\x00"
	}
	name := func(p string) string {
		if nul {
			return p
		}
		return quoteStatusPath(p)
	}
	eng := r.Engine()
	out := os.Stdout
	if showBranch {
		oid := "(initial)"
		if tip, err := eng.ResolveCommit(st.Branch); err == nil {
			oid = tip
		}
		fmt.Fprintf(out, "# branch.oid %s%s# branch.head %s%s", oid, end, st.Branch, end)
		if remote, rs, ok, err := r.RemoteStateOf(st.Branch); err == nil && ok && rs.Kind != "unpushed" {
			fmt.Fprintf(out, "# branch.upstream %s/%s%s", remote, st.Branch, end)
			if rs.Kind != "gone" {
				fmt.Fprintf(out, "# branch.ab +%d -%d%s", rs.Ahead, rs.Behind, end)
			}
		}
	}
	type row struct {
		path string
		line string
	}
	var rows []row
	base, head, err := workingPair(r, st.Branch)
	if err != nil {
		return err
	}
	conflicted := map[string]bool{}
	for _, p := range st.Conflicts {
		conflicted[p] = true
	}
	if base != head {
		changes, err := eng.TreeChanges(base, head, nil)
		if err != nil {
			return mapErr(err)
		}
		if !noRenames && len(st.Added) > 0 && len(st.Deleted) > 0 {
			if changes, err = eng.DetectRenames(changes, change.DefaultRenameScore); err != nil {
				return mapErr(err)
			}
		}
		for _, ch := range changes {
			if !want(ch.Path) || conflicted[ch.Path] {
				continue
			}
			mH, hH, mI, hI := "000000", zeroOID, "000000", zeroOID
			xy := "M."
			if ch.OldPath != "" {
				mH, hH = gitModeString(ch.Old.Mode), ch.Old.SHA
				mI, hI = gitModeString(ch.New.Mode), ch.New.SHA
				sep := "\t"
				if nul {
					sep = "\x00"
				}
				rows = append(rows, row{ch.Path, fmt.Sprintf("2 R. N... %s %s %s %s %s R%d %s%s%s", mH, mI, mI, hH, hI,
					ch.Score*100/change.MaxRenameScore, name(ch.Path), sep, name(ch.OldPath))})
				continue
			}
			switch ch.Status() {
			case 'A':
				xy = "A."
			case 'D':
				xy = "D."
			case 'T':
				xy = "T."
			}
			if xy[0] != 'A' {
				mH, hH = gitModeString(ch.Old.Mode), ch.Old.SHA
			}
			if xy[0] != 'D' {
				mI, hI = gitModeString(ch.New.Mode), ch.New.SHA
			}
			rows = append(rows, row{ch.Path, fmt.Sprintf("1 %s N... %s %s %s %s %s %s", xy, mH, mI, mI, hH, hI, name(ch.Path))})
		}
	}
	for p := range conflicted {
		if want(p) {
			rows = append(rows, row{p, fmt.Sprintf("u UU N... 100644 100644 100644 100644 %s %s %s %s", zeroOID, zeroOID, zeroOID, name(p))})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].path < rows[j].path })
	for _, rw := range rows {
		fmt.Fprint(out, rw.line, end)
	}
	if ignored {
		ign, ierr := r.IgnoredPaths(st.Branch, true)
		if ierr != nil {
			return mapErr(ierr)
		}
		for _, p := range ign {
			if want(strings.TrimSuffix(p, "/")) {
				fmt.Fprint(out, "! ", name(p), end)
			}
		}
	}
	return nil
}
