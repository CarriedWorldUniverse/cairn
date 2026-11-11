package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// --dirstat[=<param>,...]: where the change is, by directory, as git's
// diffstat_dirstat reports it. Params: changes (default; bytes changed),
// lines, files, cumulative, and a cut-off percentage (default 3).

type dirstatOpts struct {
	by         string // changes | lines | files
	cumulative bool
	permille   int
}

func parseDirstat(v string) (*dirstatOpts, error) {
	o := &dirstatOpts{by: "changes", permille: 30}
	for _, p := range strings.Split(v, ",") {
		switch p {
		case "":
		case "changes", "lines", "files":
			o.by = p
		case "cumulative":
			o.cumulative = true
		case "noncumulative":
			o.cumulative = false
		default:
			f, err := strconv.ParseFloat(p, 64)
			if err != nil || f < 0 {
				return nil, fmt.Errorf("unknown --dirstat parameter '%s'", p)
			}
			o.permille = int(f * 10)
		}
	}
	return o, nil
}

func (o *dirstatOpts) render(patches []change.FilePatch) string {
	type file struct {
		name    string
		changed int
	}
	var files []file
	total := 0
	for _, p := range patches {
		dmg := p.Damage
		switch o.by {
		case "lines":
			dmg = p.Added + p.Deleted
		case "files":
			dmg = 1
		}
		if dmg == 0 {
			continue
		}
		files = append(files, file{p.Path, dmg})
		total += dmg
	}
	if total == 0 {
		return ""
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	var sb strings.Builder
	pos := 0
	var gather func(base string) int
	gather = func(base string) int {
		sum, sources := 0, 0
		for pos < len(files) {
			f := files[pos]
			if !strings.HasPrefix(f.name, base) {
				break
			}
			if i := strings.IndexByte(f.name[len(base):], '/'); i >= 0 {
				sum += gather(f.name[:len(base)+i+1])
				sources++
			} else {
				sum += f.changed
				pos++
				sources += 2
			}
		}
		if base != "" && sources != 1 && sum > 0 {
			if permille := sum * 1000 / total; permille >= o.permille {
				fmt.Fprintf(&sb, "%4d.%01d%% %s\n", permille/10, permille%10, base)
				if !o.cumulative {
					return 0
				}
			}
		}
		return sum
	}
	gather("")
	return sb.String()
}
