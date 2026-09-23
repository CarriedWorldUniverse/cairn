package change

import "fmt"

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
		if !ok {
			if r.tracks {
				out[r.line.Name] = RemoteState{Kind: "gone"}
			} else {
				out[r.line.Name] = RemoteState{Kind: "unpushed"}
			}
			continue
		}
		local, err := e.sealedTip(r.line)
		if err != nil {
			return nil, fmt.Errorf("change.RemoteStates: %s: %w", r.line.Name, err)
		}
		if local == "" || local == remote {
			out[r.line.Name] = RemoteState{Kind: "pushed"}
			continue
		}
		ahead, behind, err := e.divergence(local, remote)
		if err != nil {
			return nil, fmt.Errorf("change.RemoteStates: %s: %w", r.line.Name, err)
		}
		switch {
		case ahead > 0 && behind > 0:
			out[r.line.Name] = RemoteState{Kind: "diverged", Ahead: ahead, Behind: behind}
		case ahead > 0:
			out[r.line.Name] = RemoteState{Kind: "ahead", Ahead: ahead}
		case behind > 0:
			out[r.line.Name] = RemoteState{Kind: "behind", Behind: behind}
		default:
			out[r.line.Name] = RemoteState{Kind: "pushed"}
		}
	}
	return out, nil
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
