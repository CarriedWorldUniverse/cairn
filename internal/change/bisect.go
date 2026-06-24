package change

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// BisectInfo is the snapshot of an active bisect session for status reporting.
type BisectInfo struct {
	Active             bool
	Branch             string
	Good, Bad, Current string
	CandidatesLeft     int
	Done               bool   // bounds are adjacent — Current is the first bad commit
	FirstBad           string // the answer, when Done
}

// BisectStep is the outcome of a bisect transition: either the next commit to
// test (Current, when !Done) or the converged answer (FirstBad, when Done).
type BisectStep struct {
	Done     bool
	Current  string
	FirstBad string
}

// bisectSession mirrors the single-row bisect table.
type bisectSession struct {
	lineID, branch              string
	good, bad, current, restore string
}

// chainIndex returns the index of the step in chain whose .Commit equals sha
// (full-sha match), or whose .Commit has sha as a prefix (short-sha convenience),
// else -1. A full match is preferred over a prefix match.
func chainIndex(chain []sealStep, sha string) int {
	if sha == "" {
		return -1
	}
	for i := range chain {
		if chain[i].Commit == sha {
			return i
		}
	}
	for i := range chain {
		if strings.HasPrefix(chain[i].Commit, sha) {
			return i
		}
	}
	return -1
}

// BisectActive reports whether a bisect session row exists.
func (e *Engine) BisectActive() (bool, error) {
	var n int
	if err := e.db.QueryRow(`SELECT count(*) FROM bisect`).Scan(&n); err != nil {
		return false, fmt.Errorf("change.BisectActive: %w", err)
	}
	return n > 0, nil
}

// loadBisect reads the single bisect session row. Returns ErrNotFound-style
// errors via sql.ErrNoRows for the caller to translate.
func (e *Engine) loadBisect() (bisectSession, error) {
	var s bisectSession
	err := e.db.QueryRow(
		`SELECT line_id, branch, good_sha, bad_sha, current_sha, restore_tip FROM bisect WHERE id=1`).
		Scan(&s.lineID, &s.branch, &s.good, &s.bad, &s.current, &s.restore)
	return s, err
}

// BisectInfo returns the active session's status, including how many candidate
// commits remain between the good and bad bounds.
func (e *Engine) BisectInfo() (BisectInfo, error) {
	s, err := e.loadBisect()
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return BisectInfo{Active: false}, nil
	case err != nil:
		return BisectInfo{}, fmt.Errorf("change.BisectInfo: %w", err)
	}
	chain, err := e.sealedChain(s.lineID)
	if err != nil {
		return BisectInfo{}, fmt.Errorf("change.BisectInfo: %w", err)
	}
	gi := chainIndex(chain, s.good)
	bi := chainIndex(chain, s.bad)
	left := 0
	done := false
	firstBad := ""
	if gi >= 0 && bi >= 0 {
		left = bi - gi
		if bi == gi+1 {
			done = true
			firstBad = s.bad
		}
	}
	return BisectInfo{
		Active:         true,
		Branch:         s.branch,
		Good:           s.good,
		Bad:            s.bad,
		Current:        s.current,
		CandidatesLeft: left,
		Done:           done,
		FirstBad:       firstBad,
	}, nil
}

// BisectStart begins a session searching lineID's sealed chain for the first bad
// commit between good (a known-good ancestor) and bad (known-bad). It records the
// session (restore_tip = the line's current tip) and returns the first midpoint
// to test — or Done immediately, with no session created, when bad is the commit
// directly after good (nothing to test).
func (e *Engine) BisectStart(lineID, branch, good, bad string) (BisectStep, error) {
	if active, err := e.BisectActive(); err != nil {
		return BisectStep{}, err
	} else if active {
		return BisectStep{}, errors.New("a bisect is already in progress; reset first")
	}

	chain, err := e.sealedChain(lineID)
	if err != nil {
		return BisectStep{}, fmt.Errorf("change.BisectStart: %w", err)
	}
	gi := chainIndex(chain, good)
	bi := chainIndex(chain, bad)
	if gi < 0 || bi < 0 || gi >= bi {
		return BisectStep{}, errors.New("good is not an ancestor of bad on this line")
	}

	line, err := e.lineByID(lineID)
	if err != nil {
		return BisectStep{}, err
	}

	// Nothing to test: bad sits directly after good.
	if bi == gi+1 {
		return BisectStep{Done: true, FirstBad: chain[bi].Commit}, nil
	}

	mi := gi + (bi-gi)/2
	current := chain[mi].Commit

	ts := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(
		`INSERT INTO bisect(id, line_id, branch, good_sha, bad_sha, current_sha, restore_tip, started_at)
		 VALUES(1,?,?,?,?,?,?,?)`,
		lineID, branch, chain[gi].Commit, chain[bi].Commit, current, line.TipCommit, ts); err != nil {
		return BisectStep{}, fmt.Errorf("change.BisectStart: insert session: %w", err)
	}
	return BisectStep{Current: current}, nil
}

// BisectMark records the verdict ("good" | "bad") for the current commit and
// returns the next midpoint to test, or Done (with the session deleted) once the
// bounds become adjacent. All catalogue writes happen in one transaction.
func (e *Engine) BisectMark(verdict string) (BisectStep, error) {
	s, err := e.loadBisect()
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return BisectStep{}, errors.New("no bisect in progress")
	case err != nil:
		return BisectStep{}, fmt.Errorf("change.BisectMark: %w", err)
	}

	chain, err := e.sealedChain(s.lineID)
	if err != nil {
		return BisectStep{}, fmt.Errorf("change.BisectMark: %w", err)
	}
	gi := chainIndex(chain, s.good)
	bi := chainIndex(chain, s.bad)
	ci := chainIndex(chain, s.current)
	if gi < 0 || bi < 0 || ci < 0 {
		return BisectStep{}, errors.New("change.BisectMark: session refers to a commit no longer on the line")
	}

	// Already converged: re-report the answer idempotently, change nothing. (The
	// session stays alive until reset, so a stray mark after Done is harmless.)
	if bi == gi+1 {
		return BisectStep{Done: true, FirstBad: chain[bi].Commit}, nil
	}

	switch verdict {
	case "good":
		// The regression is AFTER current: raise the lower bound.
		gi = ci
	case "bad":
		// current is bad: lower the upper bound.
		bi = ci
	default:
		return BisectStep{}, errors.New("verdict must be good or bad")
	}

	tx, err := e.db.Begin()
	if err != nil {
		return BisectStep{}, fmt.Errorf("change.BisectMark: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Converged: the first bad commit is the upper bound. KEEP the session alive
	// (in this done state) so the auto-snapshot stays suspended — the folder still
	// holds the historical first-bad commit, and only `reset` clears the session
	// and restores the working tip. Deleting here would let the next command
	// snapshot the historical commit into the working change.
	if bi == gi+1 {
		firstBad := chain[bi].Commit
		if _, err := tx.Exec(
			`UPDATE bisect SET good_sha=?, bad_sha=?, current_sha=? WHERE id=1`,
			chain[gi].Commit, firstBad, firstBad); err != nil {
			return BisectStep{}, fmt.Errorf("change.BisectMark: finalize session: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return BisectStep{}, fmt.Errorf("change.BisectMark: commit tx: %w", err)
		}
		return BisectStep{Done: true, FirstBad: firstBad}, nil
	}

	mi := gi + (bi-gi)/2
	current := chain[mi].Commit
	if _, err := tx.Exec(
		`UPDATE bisect SET good_sha=?, bad_sha=?, current_sha=? WHERE id=1`,
		chain[gi].Commit, chain[bi].Commit, current); err != nil {
		return BisectStep{}, fmt.Errorf("change.BisectMark: update session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BisectStep{}, fmt.Errorf("change.BisectMark: commit tx: %w", err)
	}
	return BisectStep{Current: current}, nil
}

// BisectSkip moves the current candidate one commit toward good without changing
// the good/bad bounds, so an untestable midpoint can be stepped over. It errors
// when there is no narrower candidate (the commit adjacent to good is skipped).
func (e *Engine) BisectSkip() (BisectStep, error) {
	s, err := e.loadBisect()
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return BisectStep{}, errors.New("no bisect in progress")
	case err != nil:
		return BisectStep{}, fmt.Errorf("change.BisectSkip: %w", err)
	}

	chain, err := e.sealedChain(s.lineID)
	if err != nil {
		return BisectStep{}, fmt.Errorf("change.BisectSkip: %w", err)
	}
	gi := chainIndex(chain, s.good)
	bi := chainIndex(chain, s.bad)
	ci := chainIndex(chain, s.current)
	if gi < 0 || bi < 0 || ci < 0 {
		return BisectStep{}, errors.New("change.BisectSkip: session refers to a commit no longer on the line")
	}
	// The new candidate must stay strictly inside the (good, bad) range — never the
	// known-good lower bound nor at/above the known-bad upper bound.
	if ci-1 <= gi || ci-1 >= bi {
		return BisectStep{}, errors.New("cannot narrow further — adjacent commits skipped")
	}
	current := chain[ci-1].Commit
	if _, err := e.db.Exec(`UPDATE bisect SET current_sha=? WHERE id=1`, current); err != nil {
		return BisectStep{}, fmt.Errorf("change.BisectSkip: update session: %w", err)
	}
	return BisectStep{Current: current}, nil
}

// BisectReset clears the session and returns the recorded restore tip (the line
// tip captured at start), so the caller can put the folder back.
func (e *Engine) BisectReset() (restoreTip string, err error) {
	tx, err := e.db.Begin()
	if err != nil {
		return "", fmt.Errorf("change.BisectReset: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	switch err := tx.QueryRow(`SELECT restore_tip FROM bisect WHERE id=1`).Scan(&restoreTip); {
	case errors.Is(err, sql.ErrNoRows):
		return "", errors.New("no bisect in progress")
	case err != nil:
		return "", fmt.Errorf("change.BisectReset: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM bisect WHERE id=1`); err != nil {
		return "", fmt.Errorf("change.BisectReset: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("change.BisectReset: commit tx: %w", err)
	}
	return restoreTip, nil
}
