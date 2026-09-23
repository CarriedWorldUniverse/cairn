package change

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// generation returns h's generation number: 1 for a root commit, otherwise 1 +
// the highest generation among its parents. It is git's commit-graph
// generation, and it is what makes the graph walks exact: every ancestor of a
// commit has a LOWER generation, so a walk that pops the highest generation
// first has seen every descendant of a commit before it pops that commit —
// whatever the timestamps say.
//
// A commit's generation never changes, so it is cached: in memory for the
// engine's life, and in the commit_gen table across runs. The first query on
// a fresh clone walks the history once; after that, new commits cost only
// the walk down to the nearest cached ancestor.
func (e *Engine) generation(h plumbing.Hash) (uint32, error) {
	e.genMu.Lock()
	defer e.genMu.Unlock()
	if e.gens == nil {
		e.gens = map[plumbing.Hash]uint32{}
	}
	if g, ok := e.gens[h]; ok {
		return g, nil
	}

	var lookup *sql.Stmt
	if e.db != nil {
		st, err := e.db.Prepare(`SELECT gen FROM commit_gen WHERE sha=?`)
		if err != nil {
			return 0, fmt.Errorf("change.generation: %w", err)
		}
		defer func() { _ = st.Close() }()
		lookup = st
	}
	known := func(x plumbing.Hash) (uint32, bool, error) {
		if g, ok := e.gens[x]; ok {
			return g, true, nil
		}
		if lookup == nil {
			return 0, false, nil
		}
		var g uint32
		err := lookup.QueryRow(x.String()).Scan(&g)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		if err != nil {
			return 0, false, fmt.Errorf("change.generation: %w", err)
		}
		e.gens[x] = g
		return g, true, nil
	}

	// Iterative post-order: a commit is numbered once all its parents are.
	pending := map[plumbing.Hash]*object.Commit{}
	var fresh []plumbing.Hash
	stack := []plumbing.Hash{h}
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		if _, ok, err := known(top); err != nil {
			return 0, err
		} else if ok {
			stack = stack[:len(stack)-1]
			continue
		}
		c, ok := pending[top]
		if !ok {
			var err error
			if c, err = e.git.CommitObject(top); err != nil {
				return 0, fmt.Errorf("change.generation: commit %s: %w", top, err)
			}
			pending[top] = c
		}
		var highest uint32
		ready := true
		for _, p := range c.ParentHashes {
			g, ok, err := known(p)
			if err != nil {
				return 0, err
			}
			if !ok {
				ready = false
				stack = append(stack, p)
				continue
			}
			if g > highest {
				highest = g
			}
		}
		if !ready {
			continue
		}
		e.gens[top] = highest + 1
		delete(pending, top)
		fresh = append(fresh, top)
		stack = stack[:len(stack)-1]
	}
	if err := e.persistGenerations(fresh); err != nil {
		return 0, err
	}
	return e.gens[h], nil
}

// persistGenerations writes newly computed generations in one transaction.
// A failure here only costs a recomputation next run, but it is still
// reported: a catalogue that cannot be written is worth knowing about.
func (e *Engine) persistGenerations(fresh []plumbing.Hash) error {
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
	for _, h := range fresh {
		if _, err := st.Exec(h.String(), e.gens[h]); err != nil {
			return fmt.Errorf("change.generation: store: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("change.generation: commit: %w", err)
	}
	return nil
}
