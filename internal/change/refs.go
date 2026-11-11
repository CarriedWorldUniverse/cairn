package change

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Ref-shaped operations — line rename/copy, tags (lightweight + annotated),
// remote edits — and the undo machinery that makes them reversible. Line tips
// are what an operation's before/after view captures; these operations change
// things a tip map cannot express (a name, a tag row, a remote URL), so each
// records inverse "steps" in op_undo that Undo replays.

// undoStep is one inverse action, stored as JSON in op_undo.
type undoStep struct {
	Kind string `json:"k"`
	Name string `json:"n,omitempty"`
	A    string `json:"a,omitempty"`
	B    string `json:"b,omitempty"`
	C    string `json:"c,omitempty"`
	D    string `json:"d,omitempty"`
	E    string `json:"e,omitempty"`
}

// recordRefOp appends an operation (view tips unchanged unless before/after
// differ) with a human detail and its inverse steps, atomically.
func (e *Engine) recordRefOp(opType, detail string, before, after map[string]string, steps []undoStep) error {
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return fmt.Errorf("change.recordRefOp: %w", err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return fmt.Errorf("change.recordRefOp: %w", err)
	}
	stepsJSON, err := json.Marshal(steps)
	if err != nil {
		return fmt.Errorf("change.recordRefOp: %w", err)
	}
	now := e.now().UTC()
	id := stamp(now) + "-" + newID()[:8]
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("change.recordRefOp: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(
		`INSERT INTO operation(id, op_type, actor, parent_op, view_before, view_after, detail, at, seq)
		 VALUES(?,?,?, COALESCE((SELECT id FROM operation ORDER BY seq DESC LIMIT 1),''), ?,?,?,?, COALESCE((SELECT MAX(seq) FROM operation),0)+1)`,
		id, opType, "system", string(beforeJSON), string(afterJSON), detail, stamp(now)); err != nil {
		return fmt.Errorf("change.recordRefOp: %w", err)
	}
	if len(steps) > 0 {
		if _, err := tx.Exec(`INSERT INTO op_undo(op_id, steps) VALUES(?,?)`, id, string(stepsJSON)); err != nil {
			return fmt.Errorf("change.recordRefOp: %w", err)
		}
	}
	// Stash/conflict rows changed since the last op (e.g. by Undo's
	// restoreAux) belong to this op, so undoing it restores them too.
	if err := claimAux(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (e *Engine) undoStepsOf(opID string) ([]undoStep, error) {
	var raw string
	err := e.db.QueryRow(`SELECT steps FROM op_undo WHERE op_id=?`, opID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("change.undoSteps: %w", err)
	}
	var steps []undoStep
	if err := json.Unmarshal([]byte(raw), &steps); err != nil {
		return nil, fmt.Errorf("change.undoSteps: %w", err)
	}
	return steps, nil
}

// applyUndoSteps replays the inverse steps of op opID, newest first, and
// returns the steps that would undo THAT (in application order), so the undo
// operation is itself reversible.
func (e *Engine) applyUndoSteps(opID string) ([]undoStep, error) {
	steps, err := e.undoStepsOf(opID)
	if err != nil {
		return nil, err
	}
	var inv []undoStep
	// tagInverse captures a tag row's present state as the step that restores it.
	tagInverse := func(name string) undoStep {
		old, ok, _ := e.tagRow(name)
		if !ok {
			return undoStep{Kind: "tag_drop", Name: name}
		}
		return undoStep{Kind: "tag_restore", Name: name, A: old.Commit, B: old.Tagger, C: old.At, D: old.Message, E: old.Obj}
	}
	remoteInverse := func(name string) undoStep {
		rem, err := e.GetRemote(name)
		if err != nil {
			return undoStep{Kind: "remote_drop", Name: name}
		}
		return undoStep{Kind: "remote_restore", Name: name, A: rem.URL, B: rem.Kind}
	}
	b2i := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	for i := len(steps) - 1; i >= 0; i-- {
		s := steps[i]
		switch s.Kind {
		case "tag_drop":
			inv = append(inv, tagInverse(s.Name))
			if err := e.dropTagRow(s.Name); err != nil {
				return nil, err
			}
		case "tag_restore":
			inv = append(inv, tagInverse(s.Name))
			if err := e.putTagRow(TagInfo{Name: s.Name, Commit: s.A, Tagger: s.B, At: s.C, Message: s.D, Obj: s.E}); err != nil {
				return nil, err
			}
		case "line_rename":
			// Name = the current name, A = the name to give back; B/C = the
			// remote_seen / tracks_remote flags to restore.
			l, err := e.LineByName(s.Name)
			if err != nil {
				return nil, fmt.Errorf("change.Undo: rename back %q: %w", s.Name, err)
			}
			var seen, tracks bool
			_ = e.db.QueryRow(`SELECT remote_seen, tracks_remote FROM line WHERE id=?`, l.ID).Scan(&seen, &tracks)
			inv = append(inv, undoStep{Kind: "line_rename", Name: s.A, A: s.Name, B: b2i(seen), C: b2i(tracks)})
			if err := e.renameLineRows(l.ID, s.Name, s.A); err != nil {
				return nil, err
			}
			if _, err := e.db.Exec(`UPDATE line SET remote_seen=?, tracks_remote=? WHERE id=?`, s.B == "1", s.C == "1", l.ID); err != nil {
				return nil, fmt.Errorf("change.Undo: %w", err)
			}
		case "line_restore":
			// A = id, Name = the name it held, before being set aside.
			var cur string
			_ = e.db.QueryRow(`SELECT name FROM line WHERE id=?`, s.A).Scan(&cur)
			inv = append(inv, undoStep{Kind: "line_aside", Name: cur, A: s.A})
			ts := stamp(e.now())
			if _, err := e.db.Exec(`UPDATE line SET name=?, status='open', updated_at=? WHERE id=?`, s.Name, ts, s.A); err != nil {
				return nil, fmt.Errorf("change.Undo: %w", err)
			}
			if _, err := e.db.Exec(`UPDATE change SET status='open', updated_at=? WHERE status='abandoned' AND line_id=?`, ts, s.A); err != nil {
				return nil, fmt.Errorf("change.Undo: %w", err)
			}
		case "line_aside":
			// Name = the name to park it under, A = id.
			ts := stamp(e.now())
			var cur string
			_ = e.db.QueryRow(`SELECT name FROM line WHERE id=?`, s.A).Scan(&cur)
			inv = append(inv, undoStep{Kind: "line_restore", Name: cur, A: s.A})
			if _, err := e.db.Exec(`UPDATE change SET status='abandoned', updated_at=? WHERE line_id=?`, ts, s.A); err != nil {
				return nil, fmt.Errorf("change.Undo: %w", err)
			}
			if _, err := e.db.Exec(`UPDATE line SET status='abandoned', name=?, updated_at=? WHERE id=?`, s.Name, ts, s.A); err != nil {
				return nil, fmt.Errorf("change.Undo: %w", err)
			}
		case "line_drop":
			// A copy made by `branch -c`: Name = the copy's name.
			if l, err := e.LineByName(s.Name); err == nil {
				inv = append(inv, undoStep{Kind: "line_create", Name: s.Name, A: l.ID, B: l.ParentLine, C: l.TipCommit, D: l.BaseCommit})
				_, _ = e.db.Exec(`DELETE FROM change WHERE line_id=?`, l.ID)
				_, _ = e.db.Exec(`DELETE FROM line WHERE id=?`, l.ID)
			}
		case "line_create":
			now := stamp(e.now())
			if _, err := e.db.Exec(
				`INSERT INTO line(id, name, parent_line, tip_commit, base_commit, status, created_at, updated_at)
				 VALUES(?,?,?,?,?, 'open', ?, ?)`, s.A, s.Name, s.B, s.C, s.D, now, now); err != nil {
				return nil, fmt.Errorf("change.Undo: %w", err)
			}
			inv = append(inv, undoStep{Kind: "line_drop", Name: s.Name})
		case "remote_restore":
			inv = append(inv, remoteInverse(s.Name))
			if err := e.restoreRemote(s.Name, s.A, s.B); err != nil {
				return nil, err
			}
		case "remote_drop":
			inv = append(inv, remoteInverse(s.Name))
			if err := e.dropRemoteConfig(s.Name); err != nil {
				return nil, err
			}
		case "remote_rename":
			inv = append(inv, undoStep{Kind: "remote_rename", Name: s.A, A: s.Name})
			if err := e.renameRemoteNoOp(s.Name, s.A); err != nil {
				return nil, err
			}
		case "config_set":
			if _, err := e.db.Exec(`INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, s.Name, s.A); err != nil {
				return nil, fmt.Errorf("change.Undo: %w", err)
			}
		case "config_unset":
			if _, err := e.db.Exec(`DELETE FROM config WHERE key=?`, s.Name); err != nil {
				return nil, fmt.Errorf("change.Undo: %w", err)
			}
		}
	}
	return inv, nil
}

// PendingUndoRenames lists the line renames (current name -> name to restore)
// the next Undo will reverse, so the working-copy layer can move the matching
// folders back.
func (e *Engine) PendingUndoRenames() ([][2]string, error) {
	var id string
	err := e.db.QueryRow(`SELECT id FROM operation ORDER BY seq DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	steps, err := e.undoStepsOf(id)
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Kind == "line_rename" {
			out = append(out, [2]string{steps[i].Name, steps[i].A})
		}
	}
	return out, nil
}

// ValidRefName applies git's check-ref-format rules (the subset that matters
// for a branch or tag name) so a name cairn accepts is one git accepts.
func ValidRefName(name string) error {
	switch {
	case name == "":
		return errors.New("empty name")
	case name == "@":
		return errors.New("'@' is not a valid name")
	case strings.HasPrefix(name, "-"):
		return errors.New("a name may not start with '-'")
	case strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//"):
		return errors.New("a name may not start or end with '/' or contain '//'")
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock"):
		return errors.New("a name may not end with '.' or '.lock'")
	case strings.Contains(name, "..") || strings.Contains(name, "@{"):
		return errors.New("a name may not contain '..' or '@{'")
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f || strings.ContainsRune(" ~^:?*[\\", c) {
			return fmt.Errorf("a name may not contain %q", c)
		}
	}
	for _, comp := range strings.Split(name, "/") {
		if strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".lock") {
			return errors.New("a name component may not start with '.' or end with '.lock'")
		}
	}
	return nil
}

// ---- lines ---------------------------------------------------------------

// SealedTip is the line's most recent sealed commit ("" when none) — what a
// push publishes and what ref-shaped verbs mean by "the branch tip".
func (e *Engine) SealedTipOf(l Line) (string, error) { return e.sealedTip(l) }

// CommitSubject returns the first line of a commit's message (Change-Id
// trailer stripped).
func (e *Engine) CommitSubject(sha string) (string, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return "", fmt.Errorf("change.CommitSubject: %w", err)
	}
	msg := strings.TrimSpace(stripChangeID(c.Message))
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return msg, nil
}

// IsAncestorOf reports whether a is an ancestor of (or equal to) b, by
// cairn's generation-ordered graph walk.
func (e *Engine) IsAncestorOf(a, b string) (bool, error) { return e.isAncestor(a, b) }

// ListOpenLines returns every non-abandoned line, name-ordered.
func (e *Engine) ListOpenLines() ([]Line, error) {
	nodes, err := e.lineRows()
	if err != nil {
		return nil, err
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, nil
}

func (e *Engine) lineRows() ([]Line, error) {
	rows, err := e.db.Query(`SELECT id, name, parent_line, tip_commit, base_commit, status FROM line WHERE status != 'abandoned'`)
	if err != nil {
		return nil, fmt.Errorf("change.ListOpenLines: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Line
	for rows.Next() {
		var l Line
		var parent sql.NullString
		if err := rows.Scan(&l.ID, &l.Name, &parent, &l.TipCommit, &l.BaseCommit, &l.Status); err != nil {
			return nil, fmt.Errorf("change.ListOpenLines: %w", err)
		}
		l.ParentLine = parent.String
		out = append(out, l)
	}
	return out, rows.Err()
}

// renameLineRows renames a line in the catalogue and every table that records
// a line by name (the parent links are by id and need no change).
func (e *Engine) renameLineRows(id, from, to string) error {
	ts := stamp(e.now())
	if _, err := e.db.Exec(`UPDATE line SET name=?, updated_at=? WHERE id=?`, to, ts, id); err != nil {
		return fmt.Errorf("change.RenameLine: %w", err)
	}
	_, _ = e.db.Exec(`UPDATE stash SET branch=? WHERE branch=?`, to, from)
	_, _ = e.db.Exec(`UPDATE bisect SET branch=? WHERE branch=?`, to, from)
	return nil
}

// RenameLine renames the line oldName to newName (git branch -m). Child lines
// keep pointing at it (parentage is by id). The renamed line is no longer the
// branch the remote holds under the old name, so it reads "unpushed" until
// pushed under its new name. With force (-M) an existing open, non-root line
// of that name is set aside (abandoned) first. Undoable.
func (e *Engine) RenameLine(oldName, newName string, force bool) error {
	if err := ValidRefName(newName); err != nil {
		return fmt.Errorf("change.RenameLine: %q: %w", newName, err)
	}
	l, err := e.LineByName(oldName)
	if err != nil || l.Status == "abandoned" {
		return fmt.Errorf("change.RenameLine: no line named %q: %w", oldName, ErrNotFound)
	}
	if oldName == newName {
		return nil
	}
	var steps []undoStep
	before, err := e.viewMap()
	if err != nil {
		return err
	}
	if existing, err := e.LineByName(newName); err == nil {
		switch {
		case existing.Status == "abandoned":
			return fmt.Errorf("change.RenameLine: the name %q is held by an abandoned line; pick another name", newName)
		case !force:
			return fmt.Errorf("change.RenameLine: a line named %q already exists (use -M to replace it)", newName)
		case existing.ParentLine == "":
			return fmt.Errorf("change.RenameLine: cannot replace the root line %q", newName)
		}
		ts := stamp(e.now())
		aside := newName + "@replaced-" + existing.ID[:8]
		if _, err := e.db.Exec(`UPDATE change SET status='abandoned', updated_at=? WHERE line_id=?`, ts, existing.ID); err != nil {
			return fmt.Errorf("change.RenameLine: %w", err)
		}
		if _, err := e.db.Exec(`UPDATE line SET status='abandoned', name=?, updated_at=? WHERE id=?`, aside, ts, existing.ID); err != nil {
			return fmt.Errorf("change.RenameLine: %w", err)
		}
		steps = append(steps, undoStep{Kind: "line_restore", Name: newName, A: existing.ID})
	}
	var seen, tracks bool
	if err := e.db.QueryRow(`SELECT remote_seen, tracks_remote FROM line WHERE id=?`, l.ID).Scan(&seen, &tracks); err != nil {
		return fmt.Errorf("change.RenameLine: %w", err)
	}
	if err := e.renameLineRows(l.ID, oldName, newName); err != nil {
		return err
	}
	if _, err := e.db.Exec(`UPDATE line SET remote_seen=0, tracks_remote=0 WHERE id=?`, l.ID); err != nil {
		return fmt.Errorf("change.RenameLine: %w", err)
	}
	b2i := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	steps = append(steps, undoStep{Kind: "line_rename", Name: newName, A: oldName, B: b2i(seen), C: b2i(tracks)})
	// The old projection ref would otherwise linger until the next Export.
	_ = e.git.Storer.RemoveReference(plumbing.NewBranchReferenceName(oldName))
	after, err := e.viewMap()
	if err != nil {
		return err
	}
	return e.recordRefOp("rename", "branch -m "+oldName+" "+newName, before, after, steps)
}

// CopyLine creates newName as a copy of oldName (git branch -c): same parent,
// same sealed tip, no remote association. Undoable.
func (e *Engine) CopyLine(oldName, newName string) error {
	if err := ValidRefName(newName); err != nil {
		return fmt.Errorf("change.CopyLine: %q: %w", newName, err)
	}
	src, err := e.LineByName(oldName)
	if err != nil || src.Status == "abandoned" {
		return fmt.Errorf("change.CopyLine: no line named %q: %w", oldName, ErrNotFound)
	}
	if _, err := e.LineByName(newName); err == nil {
		return fmt.Errorf("change.CopyLine: a line named %q already exists", newName)
	}
	tip, err := e.sealedTip(src)
	if err != nil {
		return err
	}
	parent := src.ParentLine
	if parent == "" {
		parent = src.ID
	}
	before, err := e.viewMap()
	if err != nil {
		return err
	}
	now := stamp(e.now())
	if _, err := e.db.Exec(
		`INSERT INTO line(id, name, parent_line, tip_commit, base_commit, status, created_at, updated_at)
		 VALUES(?,?,?,?,?, 'open', ?, ?)`,
		newID(), newName, parent, tip, tip, now, now); err != nil {
		return fmt.Errorf("change.CopyLine: %w", err)
	}
	after, err := e.viewMap()
	if err != nil {
		return err
	}
	return e.recordRefOp("branch", "branch -c "+oldName+" "+newName, before, after, []undoStep{{Kind: "line_drop", Name: newName}})
}

// ---- tags ----------------------------------------------------------------

// TagInfo describes a tag: the commit it names, and for an annotated tag its
// tag object and message.
type TagInfo struct {
	Name      string
	Commit    string
	Tagger    string
	At        string
	Message   string
	Obj       string // tag object hash; "" for a lightweight tag
	Annotated bool
	Date      time.Time // tagger date (annotated) or commit date (lightweight)
}

func (e *Engine) putTagRow(t TagInfo) error {
	if _, err := e.db.Exec(
		`INSERT INTO tag(name, commit_sha, tagger, at, message, obj) VALUES(?,?,?,?,?,?)
		 ON CONFLICT(name) DO UPDATE SET commit_sha=excluded.commit_sha, tagger=excluded.tagger,
		   at=excluded.at, message=excluded.message, obj=excluded.obj`,
		t.Name, t.Commit, t.Tagger, t.At, t.Message, t.Obj); err != nil {
		return fmt.Errorf("change.Tag: %w", err)
	}
	target := t.Commit
	if t.Obj != "" {
		target = t.Obj
	}
	return e.setRef(plumbing.NewTagReferenceName(t.Name), target)
}

func (e *Engine) dropTagRow(name string) error {
	if _, err := e.db.Exec(`DELETE FROM tag WHERE name=?`, name); err != nil {
		return fmt.Errorf("change.Tag: %w", err)
	}
	if err := e.git.Storer.RemoveReference(plumbing.NewTagReferenceName(name)); err != nil && !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return fmt.Errorf("change.Tag: %w", err)
	}
	return nil
}

func (e *Engine) tagRow(name string) (TagInfo, bool, error) {
	var t TagInfo
	err := e.db.QueryRow(`SELECT name, commit_sha, tagger, at, message, obj FROM tag WHERE name=?`, name).
		Scan(&t.Name, &t.Commit, &t.Tagger, &t.At, &t.Message, &t.Obj)
	if errors.Is(err, sql.ErrNoRows) {
		return t, false, nil
	}
	if err != nil {
		return t, false, fmt.Errorf("change.Tag: %w", err)
	}
	return t, true, nil
}

// CreateTag records a tag at commit. message != "" (or annotated) writes a real
// annotated tag object, which push publishes as such. force moves an existing
// tag. Undoable.
func (e *Engine) CreateTag(name, commit, message string, annotated, force bool, tagger string) error {
	if err := ValidRefName(name); err != nil {
		return fmt.Errorf("change.Tag: %q: %w", name, err)
	}
	if _, err := e.git.CommitObject(plumbing.NewHash(commit)); err != nil {
		return fmt.Errorf("change.Tag: %s is not a commit: %w", commit, err)
	}
	old, exists, err := e.tagRow(name)
	if err != nil {
		return err
	}
	if exists && !force {
		return fmt.Errorf("change.Tag: tag %q already exists (use -f to move it)", name)
	}
	t := TagInfo{Name: name, Commit: commit, Tagger: tagger, At: stamp(e.now())}
	if annotated {
		n, em := e.idName, e.idEmail
		if n == "" {
			n = tagger
		}
		if n == "" {
			n = "cairn"
		}
		if em == "" {
			em = "cairn@localhost"
		}
		if !strings.HasSuffix(message, "\n") {
			message += "\n"
		}
		tag := &object.Tag{
			Name:       name,
			Tagger:     object.Signature{Name: n, Email: em, When: e.now()},
			Message:    message,
			TargetType: plumbing.CommitObject,
			Target:     plumbing.NewHash(commit),
		}
		obj := e.git.Storer.NewEncodedObject()
		if err := tag.Encode(obj); err != nil {
			return fmt.Errorf("change.Tag: %w", err)
		}
		h, err := e.git.Storer.SetEncodedObject(obj)
		if err != nil {
			return fmt.Errorf("change.Tag: %w", err)
		}
		t.Obj, t.Message = h.String(), message
	}
	view, err := e.viewMap()
	if err != nil {
		return err
	}
	step := undoStep{Kind: "tag_drop", Name: name}
	if exists {
		step = undoStep{Kind: "tag_restore", Name: name, A: old.Commit, B: old.Tagger, C: old.At, D: old.Message, E: old.Obj}
	}
	if err := e.putTagRow(t); err != nil {
		return err
	}
	return e.recordRefOp("tag", "tag "+name+" "+commit[:7], view, view, []undoStep{step})
}

// RemoveTag deletes a tag (and its ref). Undoable; a missing tag is an error.
func (e *Engine) RemoveTag(name string) (string, error) {
	old, exists, err := e.tagRow(name)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("change.Tag: tag %q not found: %w", name, ErrNotFound)
	}
	view, err := e.viewMap()
	if err != nil {
		return "", err
	}
	if err := e.dropTagRow(name); err != nil {
		return "", err
	}
	step := undoStep{Kind: "tag_restore", Name: name, A: old.Commit, B: old.Tagger, C: old.At, D: old.Message, E: old.Obj}
	return old.Commit, e.recordRefOp("tag", "tag -d "+name, view, view, []undoStep{step})
}

// ListTagInfos returns every tag with its annotation and date. A tag imported
// from a git remote whose ref pointed at a tag object is reported annotated,
// peeled to its commit.
func (e *Engine) ListTagInfos() ([]TagInfo, error) {
	rows, err := e.db.Query(`SELECT name, commit_sha, tagger, at, message, obj FROM tag ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("change.ListTagInfos: %w", err)
	}
	var out []TagInfo
	for rows.Next() {
		var t TagInfo
		if err := rows.Scan(&t.Name, &t.Commit, &t.Tagger, &t.At, &t.Message, &t.Obj); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("change.ListTagInfos: %w", err)
		}
		out = append(out, t)
	}
	_ = rows.Close()
	for i := range out {
		t := &out[i]
		if t.Obj == "" {
			if tg, err := e.git.TagObject(plumbing.NewHash(t.Commit)); err == nil {
				t.Obj = t.Commit
				if peeled, ok := e.peelToCommit(t.Commit); ok {
					t.Commit = peeled
				}
				t.Message = tg.Message
				t.Tagger = tg.Tagger.Name
			}
		}
		t.Annotated = t.Obj != ""
		if t.Annotated {
			if tg, err := e.git.TagObject(plumbing.NewHash(t.Obj)); err == nil {
				t.Date = tg.Tagger.When
			}
		}
		if c, err := e.git.CommitObject(plumbing.NewHash(t.Commit)); err == nil {
			if t.Date.IsZero() {
				t.Date = c.Committer.When
			}
		}
	}
	return out, nil
}

// ---- remotes -------------------------------------------------------------

// GetRemote returns the named remote, or ErrNotFound.
func (e *Engine) GetRemote(name string) (RemoteInfo, error) {
	rem, err := e.git.Remote(name)
	if errors.Is(err, git.ErrRemoteNotFound) {
		return RemoteInfo{}, fmt.Errorf("change.GetRemote: no such remote %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return RemoteInfo{}, fmt.Errorf("change.GetRemote: %w", err)
	}
	url := ""
	if u := rem.Config().URLs; len(u) > 0 {
		url = u[0]
	}
	return RemoteInfo{Name: name, URL: url, Kind: e.remoteKind(name)}, nil
}

func (e *Engine) dropRemoteConfig(name string) error {
	if err := e.git.DeleteRemote(name); err != nil && !errors.Is(err, git.ErrRemoteNotFound) {
		return fmt.Errorf("change.Remote: %w", err)
	}
	_, _ = e.db.Exec(`DELETE FROM remote_kind WHERE name=?`, name)
	return nil
}

func (e *Engine) restoreRemote(name, url, kind string) error {
	_ = e.git.DeleteRemote(name)
	if _, err := e.git.CreateRemote(&config.RemoteConfig{Name: name, URLs: []string{url}}); err != nil {
		return fmt.Errorf("change.Remote: %w", err)
	}
	if kind == "" {
		kind = "git"
	}
	_, err := e.db.Exec(`INSERT INTO remote_kind(name, kind) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET kind=excluded.kind`, name, kind)
	return err
}

// moveTrackingRefs re-homes refs/remotes/<from>/* under <to>.
func (e *Engine) moveTrackingRefs(from, to string) error {
	prefix := "refs/remotes/" + from + "/"
	iter, err := e.git.Storer.IterReferences()
	if err != nil {
		return err
	}
	var moves []*plumbing.Reference
	_ = iter.ForEach(func(r *plumbing.Reference) error {
		if strings.HasPrefix(r.Name().String(), prefix) {
			moves = append(moves, r)
		}
		return nil
	})
	iter.Close()
	for _, r := range moves {
		if to != "" && r.Type() == plumbing.HashReference {
			nn := plumbing.ReferenceName("refs/remotes/" + to + "/" + strings.TrimPrefix(r.Name().String(), prefix))
			if err := e.git.Storer.SetReference(plumbing.NewHashReference(nn, r.Hash())); err != nil {
				return err
			}
		}
		if err := e.git.Storer.RemoveReference(r.Name()); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) renameRemoteNoOp(from, to string) error {
	rem, err := e.GetRemote(from)
	if err != nil {
		return err
	}
	if _, err := e.GetRemote(to); err == nil {
		return fmt.Errorf("change.RenameRemote: remote %q already exists", to)
	}
	cfg := rem.Kind
	if err := e.restoreRemote(to, rem.URL, cfg); err != nil {
		return err
	}
	if err := e.dropRemoteConfig(from); err != nil {
		return err
	}
	return e.moveTrackingRefs(from, to)
}

// RemoveRemote deletes a remote and its tracking refs. The remote's config is
// restored by undo (tracking refs are re-created by the next fetch).
func (e *Engine) RemoveRemote(name string) error {
	rem, err := e.GetRemote(name)
	if err != nil {
		return err
	}
	view, err := e.viewMap()
	if err != nil {
		return err
	}
	if err := e.dropRemoteConfig(name); err != nil {
		return err
	}
	if err := e.moveTrackingRefs(name, ""); err != nil {
		return fmt.Errorf("change.RemoveRemote: %w", err)
	}
	return e.recordRefOp("remote", "remote remove "+name, view, view,
		[]undoStep{{Kind: "remote_restore", Name: name, A: rem.URL, B: rem.Kind}})
}

// RenameRemote renames a remote, moving its tracking refs. Undoable.
func (e *Engine) RenameRemote(from, to string) error {
	if err := ValidRefName(to); err != nil {
		return fmt.Errorf("change.RenameRemote: %q: %w", to, err)
	}
	view, err := e.viewMap()
	if err != nil {
		return err
	}
	if err := e.renameRemoteNoOp(from, to); err != nil {
		return err
	}
	return e.recordRefOp("remote", "remote rename "+from+" "+to, view, view,
		[]undoStep{{Kind: "remote_rename", Name: to, A: from}})
}

// SetRemoteURL re-points a remote at url (credentials are stripped into the
// credstore, like add). Undoable.
func (e *Engine) SetRemoteURL(name, url string) error {
	rem, err := e.GetRemote(name)
	if err != nil {
		return err
	}
	view, err := e.viewMap()
	if err != nil {
		return err
	}
	if err := e.AddRemote(name, url, rem.Kind); err != nil {
		return err
	}
	return e.recordRefOp("remote", "remote set-url "+name, view, view,
		[]undoStep{{Kind: "remote_restore", Name: name, A: rem.URL, B: rem.Kind}})
}

// ListRemoteRefs asks the remote what it advertises (hash refs only).
func (e *Engine) ListRemoteRefs(name string) (map[string]string, string, error) {
	rem, err := e.git.Remote(name)
	if err != nil {
		return nil, "", fmt.Errorf("change.ListRemoteRefs: no such remote %q", name)
	}
	auth, err := e.authForRemote(rem)
	if err != nil {
		return nil, "", fmt.Errorf("change.ListRemoteRefs: %w", err)
	}
	refs, err := listAdvertisedRefs(rem, auth)
	if err != nil && !strings.Contains(err.Error(), "empty") {
		return nil, "", fmt.Errorf("change.ListRemoteRefs: %w", err)
	}
	out := map[string]string{}
	head := ""
	for _, r := range refs {
		if r.Type() == plumbing.SymbolicReference && r.Name() == plumbing.HEAD {
			head = r.Target().Short()
			continue
		}
		if r.Type() == plumbing.HashReference {
			out[r.Name().String()] = r.Hash().String()
		}
	}
	if head == "" {
		// A remote that does not advertise the symref: guess by hash.
		if h, ok := out["refs/heads/main"]; ok && out["HEAD"] == h {
			head = "main"
		}
	}
	return out, head, nil
}

// DeleteRemoteRef deletes ref (a full ref name such as refs/heads/x) on the
// remote and drops the matching tracking ref.
func (e *Engine) DeleteRemoteRef(remoteName, ref string) error {
	rem, err := e.git.Remote(remoteName)
	if err != nil {
		return fmt.Errorf("change.DeleteRemoteRef: no such remote %q", remoteName)
	}
	auth, err := e.authForRemote(rem)
	if err != nil {
		return fmt.Errorf("change.DeleteRemoteRef: %w", err)
	}
	err = rem.Push(&git.PushOptions{RemoteName: remoteName, RefSpecs: []config.RefSpec{config.RefSpec(":" + ref)}, Auth: auth})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("change.DeleteRemoteRef: %w", err)
	}
	refs, _, lerr := e.ListRemoteRefs(remoteName)
	if lerr != nil {
		return lerr
	}
	if _, still := refs[ref]; still {
		return fmt.Errorf("change.DeleteRemoteRef: %s is still present on %s after the delete (the remote may refuse deletions)", ref, remoteName)
	}
	if strings.HasPrefix(ref, "refs/heads/") {
		_ = e.git.Storer.RemoveReference(plumbing.ReferenceName("refs/remotes/" + remoteName + "/" + strings.TrimPrefix(ref, "refs/heads/")))
	}
	return nil
}

// LeaseCheck implements --force-with-lease against the remote-tracking ref:
// the remote's live value of branch must equal what the last fetch/push
// recorded, or someone else has pushed since.
func (e *Engine) LeaseCheck(remoteName, branch string) error {
	live, _, err := e.ListRemoteRefs(remoteName)
	if err != nil {
		return err
	}
	want := ""
	if ref, err := e.git.Storer.Reference(plumbing.ReferenceName("refs/remotes/" + remoteName + "/" + branch)); err == nil {
		want = ref.Hash().String()
	}
	got := live["refs/heads/"+branch]
	if got != want {
		return fmt.Errorf("stale info: %s/%s is %s on the remote but your last fetch saw %s — fetch first, or use --force",
			remoteName, branch, shortOrNone(got), shortOrNone(want))
	}
	return nil
}

func shortOrNone(h string) string {
	if h == "" {
		return "(absent)"
	}
	return h[:7]
}

// TrackingHeads lists the short names under refs/remotes/<remote>/ (HEAD
// excluded) — the remote-tracking branches `branch -r` shows.
func (e *Engine) TrackingHeads(remoteName string) (map[string]string, error) {
	return e.remoteHeads(remoteName)
}

// DisableGlobalForce makes the prepared push honour only each refspec's own
// '+' prefix: --force-with-lease forces branches (after its lease check)
// but must not let the global force option move remote tags.
func (pp *PreparedPush) DisableGlobalForce() { pp.force = false }
