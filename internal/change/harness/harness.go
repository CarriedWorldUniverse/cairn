// Package harness drives N simulated agents against one change.Engine with a
// seedable deterministic scheduler to prove the Phase-1 convergence properties.
package harness

import (
	"math/rand"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
)

// Step is one scripted action: commit Files on Line (created off Base if new).
type Step struct {
	Line, Base, Author string
	Files              map[string][]byte
}

// Run executes steps in a seed-shuffled order against e, each on a fresh change.
// Returns the change id per step (index-aligned to the input steps).
//
// steps must be topologically ordered by base dependency: every Base value must
// be "main" or appear as some other step's Line, so a line's base line exists
// before it is created.
func Run(e *change.Engine, steps []Step, seed int64) ([]string, error) {
	order := rand.New(rand.NewSource(seed)).Perm(len(steps))
	lineID := map[string]string{}
	root, err := e.LineByName("main")
	if err != nil {
		return nil, err
	}
	lineID["main"] = root.ID
	ids := make([]string, len(steps))
	for _, idx := range order {
		s := steps[idx]
		if _, ok := lineID[s.Line]; !ok {
			l, err := e.CreateLine(s.Line, lineID[s.Base])
			if err != nil {
				return nil, err
			}
			lineID[s.Line] = l.ID
		}
		ch, err := e.CreateChange(lineID[s.Line], s.Author)
		if err != nil {
			return nil, err
		}
		if _, err := e.Commit(ch.ID, s.Files, ""); err != nil {
			return nil, err
		}
		ids[idx] = ch.ID
	}
	return ids, nil
}
