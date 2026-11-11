package change

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
)

// RemoteState is a line's relationship to its branch on one remote, read
// from the remote-tracking ref (refreshed by fetch/pull and set by a verified
// push) — so it is "as of the last fetch or push", not a live query.
type RemoteState struct {
	Kind   string // pushed | ahead | behind | diverged | unpushed | gone
	Ahead  int    // commits here the remote lacks (ahead, diverged)
	Behind int    // commits on the remote this line lacks (behind, diverged)
}

// String renders the state as `tree` prints it.
func (s RemoteState) String() string {
	switch s.Kind {
	case "ahead":
		return fmt.Sprintf("ahead %d", s.Ahead)
	case "behind":
		return fmt.Sprintf("behind %d", s.Behind)
	case "diverged":
		return fmt.Sprintf("diverged +%d/-%d", s.Ahead, s.Behind)
	}
	return s.Kind
}

// RemoteStates classifies every open line against remoteName's tracking
// refs. A line with no tracking ref is "gone" if a remote has ever held its
// branch (remote_seen: set on clone/import and on every verified push) — it
// has since been deleted there, typically after its PR merged — and
// "unpushed" if it was created locally and never pushed.
// A line whose sealed tip equals the tracking ref is "pushed"; otherwise the
// divergence walk says ahead, behind or diverged.
func (e *Engine) RemoteStates(remoteName string) (map[string]RemoteState, error) {
	heads, err := e.remoteHeads(remoteName)
	if err != nil {
		return nil, fmt.Errorf("change.RemoteStates: %w", err)
	}
	rows, err := e.db.Query(`SELECT id, name, parent_line, tip_commit, base_commit, status, remote_seen FROM line WHERE status != 'abandoned'`)
	if err != nil {
		return nil, fmt.Errorf("change.RemoteStates: %w", err)
	}
	type row struct {
		line   Line
		tracks bool
	}
	var lines []row
	for rows.Next() {
		var l Line
		var parent, tracks interface{}
		if err := rows.Scan(&l.ID, &l.Name, &parent, &l.TipCommit, &l.BaseCommit, &l.Status, &tracks); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("change.RemoteStates: %w", err)
		}
		t := false
		if v, ok := tracks.(int64); ok && v != 0 {
			t = true
		}
		lines = append(lines, row{line: l, tracks: t})
	}
	_ = rows.Close()
	out := make(map[string]RemoteState, len(lines))
	for _, r := range lines {
		remote, ok := heads[r.line.Name]
		st, err := e.remoteState(r.line, r.tracks, remote, ok)
		if err != nil {
			return nil, fmt.Errorf("change.RemoteStates: %s: %w", r.line.Name, err)
		}
		out[r.line.Name] = st
	}
	return out, nil
}

// remoteState classifies one line against its tracking ref (remote, present
// only if has): see RemoteStates.
func (e *Engine) remoteState(line Line, seen bool, remote string, has bool) (RemoteState, error) {
	if !has {
		if seen {
			return RemoteState{Kind: "gone"}, nil
		}
		return RemoteState{Kind: "unpushed"}, nil
	}
	local, err := e.sealedTip(line)
	if err != nil {
		return RemoteState{}, err
	}
	if local == "" || local == remote {
		return RemoteState{Kind: "pushed"}, nil
	}
	ahead, behind, err := e.divergence(local, remote)
	if err != nil {
		return RemoteState{}, err
	}
	switch {
	case ahead > 0 && behind > 0:
		return RemoteState{Kind: "diverged", Ahead: ahead, Behind: behind}, nil
	case ahead > 0:
		return RemoteState{Kind: "ahead", Ahead: ahead}, nil
	case behind > 0:
		return RemoteState{Kind: "behind", Behind: behind}, nil
	}
	return RemoteState{Kind: "pushed"}, nil
}

// RemoteStateOf is RemoteStates for ONE line: the same classification, read
// from that line's tracking ref alone. `status` reports a single line, and on
// a repository with hundreds of remote branches and tags, listing every ref
// (a file each) and walking every line's divergence to print one of them was
// a fifth of the command. ok is false for an abandoned or unknown line, which
// RemoteStates leaves out of its map.
func (e *Engine) RemoteStateOf(remoteName, lineName string) (st RemoteState, ok bool, err error) {
	var l Line
	var parent sql.NullString
	var seen int64
	err = e.db.QueryRow(
		`SELECT id, name, parent_line, tip_commit, base_commit, status, remote_seen FROM line WHERE name=? AND status != 'abandoned'`,
		lineName).Scan(&l.ID, &l.Name, &parent, &l.TipCommit, &l.BaseCommit, &l.Status, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return RemoteState{}, false, nil
	}
	if err != nil {
		return RemoteState{}, false, fmt.Errorf("change.RemoteStateOf: %w", err)
	}
	l.ParentLine = parent.String
	// The tracking ref, under the rules remoteHeads applies to every ref: a
	// hash reference, and never the remote's HEAD pointer.
	var remote string
	has := false
	if lineName != "HEAD" {
		ref, rerr := e.git.Storer.Reference(plumbing.ReferenceName("refs/remotes/" + remoteName + "/" + lineName))
		switch {
		case rerr == nil:
			if ref.Type() == plumbing.HashReference {
				remote, has = ref.Hash().String(), true
			}
		case !errors.Is(rerr, plumbing.ErrReferenceNotFound):
			return RemoteState{}, false, fmt.Errorf("change.RemoteStateOf: %w", rerr)
		}
	}
	st, err = e.remoteState(l, seen != 0, remote, has)
	if err != nil {
		return RemoteState{}, false, fmt.Errorf("change.RemoteStateOf: %s: %w", lineName, err)
	}
	return st, true, nil
}

// AheadOfParent is `rev-list parent..line` on sealed tips: the commits this
// line has that its parent does not. 0 for the root, which has no parent.
func (e *Engine) AheadOfParent(line Line) (int, error) {
	if line.ParentLine == "" {
		return 0, nil
	}
	parent, err := e.lineByID(line.ParentLine)
	if err != nil {
		return 0, fmt.Errorf("change.AheadOfParent: %w", err)
	}
	st, err := e.sealedTip(line)
	if err != nil {
		return 0, fmt.Errorf("change.AheadOfParent: %w", err)
	}
	pt, err := e.sealedTip(parent)
	if err != nil {
		return 0, fmt.Errorf("change.AheadOfParent: %w", err)
	}
	ahead, _, err := e.divergence(st, pt)
	if err != nil {
		return 0, fmt.Errorf("change.AheadOfParent: %w", err)
	}
	return ahead, nil
}
