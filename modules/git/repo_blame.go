// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"fmt"
	"regexp"
)

var (
	ErrBlameFileDoesNotExist   = errors.New("the blamed file does not exist")
	ErrBlameFileNotEnoughLines = errors.New("the blamed file has not enough lines")

	notEnoughLinesRe = regexp.MustCompile(`^fatal: file .+ has only \d+ lines?\n$`)
)

// LineBlame returns the latest commit at the given line
func (repo *Repository) LineBlame(revision, file string, line uint64) (*Commit, error) {
	res, _, gitErr := NewCommand(repo.Ctx, "blame").
		AddOptionFormat("-L %d,%d", line, line).
		AddOptionValues("-p", revision).
		AddDashesAndList(file).RunStdString(&RunOpts{Dir: repo.Path})
	if gitErr != nil {
		stdErr := gitErr.Stderr()

		if stdErr == fmt.Sprintf("fatal: no such path %s in %s\n", file, revision) {
			return nil, ErrBlameFileDoesNotExist
		}
		if notEnoughLinesRe.MatchString(stdErr) {
			return nil, ErrBlameFileNotEnoughLines
		}

		return nil, gitErr
	}

	objectFormat, err := repo.GetObjectFormat()
	if err != nil {
		return nil, err
	}

	objectIDLen := objectFormat.FullLength()
	if len(res) < objectIDLen {
		return nil, fmt.Errorf("output of blame is invalid, cannot contain commit ID: %s", res)
	}

	return repo.GetCommit(res[:objectIDLen])
}
