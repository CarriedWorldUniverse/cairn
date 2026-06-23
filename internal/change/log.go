package change

import (
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

// stripChangeID removes the trailing "\n\nChange-Id: ...\n" trailer.
func stripChangeID(m string) string {
	if i := strings.Index(m, "\n\nChange-Id:"); i >= 0 {
		return m[:i]
	}
	return m
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
