package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// log -L <start>,<end>:<file> | -L :<funcname>:<file>: the history of a range
// of lines, each commit that changed it shown with a patch of just that range.

var errLogLDone = errors.New("log -L: limit reached")

func logLineRange(eng *change.Engine, lc lineContext, g *gitOpts, o change.WalkOptions, pp *prettyPrinter, lw *logWriter, limit, skip int) error {
	specs := g.strs("L")
	if len(specs) != 1 {
		return errors.New("log: -L is supported once per command (one range of one file)")
	}
	spec := specs[0]
	i := strings.LastIndex(spec, ":")
	if i < 0 || i == len(spec)-1 {
		return errors.New("log: -L needs <start>,<end>:<file> or :<funcname>:<file>")
	}
	rangeSpec, file := spec[:i], spec[i+1:]
	path, err := lc.treePath(file)
	if err != nil {
		return err
	}
	if len(o.Include) == 0 {
		return errors.New("log: -L needs a starting revision")
	}
	start := o.Include[0]
	lines, ok, err := eng.FileLines(start, path)
	if err != nil {
		return mapErr(err)
	}
	if !ok {
		return fmt.Errorf("log: There is no path %s in the commit", file)
	}
	var rng change.LineRange
	if fn, isFunc := strings.CutPrefix(rangeSpec, ":"); isFunc {
		rng, err = change.FuncLineRange(fn, lines)
	} else {
		rng, err = change.ParseLineRange(rangeSpec, lines)
	}
	if err != nil {
		return fmt.Errorf("log: %w", err)
	}
	showPatch := !g.has("s")
	o.Paths, o.Skip, o.Limit = nil, 0, 0
	state := map[string]change.LineRange{start: rng}
	propagate := func(sha string, r change.LineRange) {
		if r.Empty() {
			return
		}
		if old, ok := state[sha]; ok {
			r = change.LineRange{Start: min(old.Start, r.Start), End: max(old.End, r.End)}
		}
		state[sha] = r
	}
	shown, seen := 0, 0
	err = eng.WalkCommits(o, func(c *change.CommitMeta) error {
		cur, ok := state[c.SHA]
		if !ok {
			return nil
		}
		delete(state, c.SHA)
		parents := c.Parents
		if g.on("first-parent") && len(parents) > 1 {
			parents = parents[:1]
		}
		var hit *change.LineLogStep
		switch len(parents) {
		case 0:
			st, err := eng.LineLog("", c.SHA, path, cur)
			if err != nil {
				return err
			}
			if st.Touched {
				hit = &st
			}
		case 1:
			st, err := eng.LineLog(parents[0], c.SHA, path, cur)
			if err != nil {
				return err
			}
			if st.Touched {
				hit = &st
			}
			propagate(parents[0], st.Parent)
		default:
			steps := make([]change.LineLogStep, len(parents))
			same := -1
			for k, p := range parents {
				if steps[k], err = eng.LineLog(p, c.SHA, path, cur); err != nil {
					return err
				}
				if !steps[k].Touched && same < 0 {
					same = k
				}
			}
			if same >= 0 {
				// The range came unchanged from one parent: follow only it.
				propagate(parents[same], steps[same].Parent)
				break
			}
			hit = &steps[0]
			for k, p := range parents {
				propagate(p, steps[k].Parent)
			}
		}
		if hit == nil {
			return nil
		}
		seen++
		if seen <= skip {
			return nil
		}
		diff := ""
		if showPatch {
			diff = hit.Patch
			if pp.f.kind == "oneline" {
				diff = "\n" + diff
			}
		}
		lw.write(pp.entry(c), diff, nil)
		shown++
		if limit > 0 && shown >= limit {
			return errLogLDone
		}
		return nil
	})
	if errors.Is(err, errLogLDone) {
		return nil
	}
	return mapErr(err)
}
