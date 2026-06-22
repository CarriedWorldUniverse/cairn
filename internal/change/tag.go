package change

import (
	"fmt"
	"time"
)

// Tag is a row in the tag catalogue: a stable, human-readable name pointing at
// a single commit. Tags are immutable labels; they do not affect any line's
// ref-map, so they carry no operation-log entry.
type Tag struct {
	Name   string
	Commit string
	Tagger string
}

// Tag records a tag named name at commitSha, attributed to tagger. The tag name
// is a PRIMARY KEY, so re-tagging an existing name returns a wrapped error.
func (e *Engine) Tag(name, commitSha, tagger string) error {
	at := e.now().UTC().Format(time.RFC3339Nano)
	if _, err := e.db.Exec(
		`INSERT INTO tag(name, commit_sha, tagger, at) VALUES(?,?,?,?)`,
		name, commitSha, tagger, at); err != nil {
		return fmt.Errorf("change.Tag: %w", err)
	}
	return nil
}

// ListTags lists all tags, ordered by name.
func (e *Engine) ListTags() ([]Tag, error) {
	rows, err := e.db.Query(
		`SELECT name, commit_sha, tagger FROM tag ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("change.ListTags: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.Name, &t.Commit, &t.Tagger); err != nil {
			return nil, fmt.Errorf("change.ListTags: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change.ListTags: %w", err)
	}
	return out, nil
}
