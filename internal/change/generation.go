package change

import (
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
)

// generation returns h's generation number: 1 for a root commit, otherwise 1 +
// the highest generation among its parents. It is git's commit-graph
// generation, and it is what makes the graph walks exact: every ancestor of a
// commit has a LOWER generation, so a walk that pops the highest generation
// first has seen every descendant of a commit before it pops that commit —
// whatever the timestamps say.
//
// A commit's generation never changes, so it is cached: in the commit graph
// (commitgraph.go) for the walks, and in the commit_gen table across runs.
// The first query on a fresh clone walks the history once; after that, new
// commits cost only the walk down to the nearest cached ancestor.
func (e *Engine) generation(h plumbing.Hash) (uint32, error) {
	g := e.lockGraph()
	defer e.cgMu.Unlock()
	id, err := g.id(h)
	if err != nil {
		return 0, fmt.Errorf("change.generation: %w", err)
	}
	if err := g.flushGenerations(); err != nil {
		return 0, err
	}
	return g.nodes[id].gen, nil
}

// persistGenerations writes newly computed generations in one transaction.
// A failure here only costs a recomputation next run, but it is still
// reported: a catalogue that cannot be written is worth knowing about.
func (e *Engine) persistGenerations(fresh []cgNode) error {
	if e.db == nil || len(fresh) == 0 {
		return nil
	}
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("change.generation: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	st, err := tx.Prepare(`INSERT OR IGNORE INTO commit_gen(sha, gen) VALUES(?, ?)`)
	if err != nil {
		return fmt.Errorf("change.generation: %w", err)
	}
	defer func() { _ = st.Close() }()
	for i := range fresh {
		if _, err := st.Exec(fresh[i].hash.String(), fresh[i].gen); err != nil {
			return fmt.Errorf("change.generation: store: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change.generation: commit: %w", err)
	}
	return nil
}
