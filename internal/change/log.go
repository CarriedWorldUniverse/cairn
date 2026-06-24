package change

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
)

// CommitInfo is the displayable metadata of one commit.
type CommitInfo struct {
	SHA         string
	AuthorName  string
	AuthorEmail string
	When        time.Time
	Subject     string // first line, Change-Id trailer stripped
	Message     string // full message minus the Change-Id trailer
	Working     bool   // head of an open (unsealed) change — the working commit
}

func (e *Engine) commitInfo(sha string) (CommitInfo, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(sha))
	if err != nil {
		return CommitInfo{}, fmt.Errorf("change.commitInfo: %w", err)
	}
	msg := stripChangeID(c.Message)
	subject := msg
	if i := strings.IndexByte(subject, '\n'); i >= 0 {
		subject = subject[:i]
	}
	return CommitInfo{
		SHA:         sha,
		AuthorName:  c.Author.Name,
		AuthorEmail: c.Author.Email,
		When:        c.Author.When,
		Subject:     strings.TrimSpace(subject),
		Message:     strings.TrimSpace(msg),
	}, nil
}

// isWorkingHead reports whether sha is the head_commit of some OPEN (unsealed)
// change — i.e. it is a live working commit that a snapshot may still amend.
func (e *Engine) isWorkingHead(sha string) (bool, error) {
	var one int
	err := e.db.QueryRow(
		`SELECT 1 FROM change WHERE head_commit=? AND sealed=0 LIMIT 1`, sha).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("change.isWorkingHead: %w", err)
	}
	return true, nil
}

// stripChangeID removes the trailing "\n\nChange-Id: ...\n" trailer.
func stripChangeID(m string) string {
	if i := strings.LastIndex(m, "\n\nChange-Id:"); i >= 0 {
		return m[:i]
	}
	return m
}

// parseChangeID extracts the Change-Id trailer value from a commit message, or
// "" if the message carries no trailer. It locates the trailer the same way
// stripChangeID does ("\n\nChange-Id:"), then trims the value to its first line.
func parseChangeID(m string) string {
	i := strings.LastIndex(m, "\n\nChange-Id:")
	if i < 0 {
		return ""
	}
	rest := m[i+len("\n\nChange-Id:"):]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[:nl]
	}
	return strings.TrimSpace(rest)
}

// Log returns up to limit commits along first-parent ancestry from commit
// (newest first). limit<=0 means unbounded (bounded by describeWalkCap).
func (e *Engine) Log(commit string, limit int) ([]CommitInfo, error) {
	var out []CommitInfo
	cur := commit
	for n := 0; cur != "" && n < describeWalkCap; n++ {
		if limit > 0 && len(out) >= limit {
			break
		}
		ci, err := e.commitInfo(cur)
		if err != nil {
			return nil, fmt.Errorf("change.Log: %w", err)
		}
		working, err := e.isWorkingHead(cur)
		if err != nil {
			return nil, fmt.Errorf("change.Log: %w", err)
		}
		ci.Working = working
		out = append(out, ci)
		next, err := e.firstParent(cur)
		if err != nil {
			return nil, fmt.Errorf("change.Log: %w", err)
		}
		cur = next
	}
	return out, nil
}

// Show returns a commit's metadata and the diff against its first parent.
// When the commit has no parent (root commit), the diff is against the empty tree.
func (e *Engine) Show(commit string) (CommitInfo, []FileDiff, error) {
	ci, err := e.commitInfo(commit)
	if err != nil {
		return CommitInfo{}, nil, err
	}
	parent, err := e.firstParent(commit)
	if err != nil {
		return CommitInfo{}, nil, fmt.Errorf("change.Show: %w", err)
	}
	// parent=="" → diff vs empty tree (DiffCommits handles this case)
	diffs, err := e.DiffCommits(parent, commit)
	if err != nil {
		return CommitInfo{}, nil, err
	}
	return ci, diffs, nil
}
