package change

import (
	"fmt"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// BlameLine is one line's provenance.
type BlameLine struct {
	Commit   string
	ChangeID string // Change-Id trailer of Commit ("" if none)
	Author   string
	When     time.Time
	Text     string
}

// Blame returns per-line provenance for path at commit.
func (e *Engine) Blame(commit, path string) ([]BlameLine, error) {
	c, err := e.git.CommitObject(plumbing.NewHash(commit))
	if err != nil {
		return nil, fmt.Errorf("change.Blame: commit %s: %w", commit, err)
	}
	res, err := git.Blame(c, path)
	if err != nil {
		return nil, fmt.Errorf("change.Blame: %q: %w", path, err)
	}
	out := make([]BlameLine, 0, len(res.Lines))
	for _, ln := range res.Lines {
		sha := ln.Hash.String()
		cid, _ := e.ChangeIDOf(sha)
		out = append(out, BlameLine{
			Commit:   sha,
			ChangeID: cid,
			Author:   ln.AuthorName,
			When:     ln.Date,
			Text:     ln.Text,
		})
	}
	return out, nil
}

// IsWorkingHead reports whether sha is the head of an open (un-sealed) change.
func (e *Engine) IsWorkingHead(sha string) (bool, error) { return e.isWorkingHead(sha) }
