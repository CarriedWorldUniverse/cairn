// Copyright 2017 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	ErrBlameFileDoesNotExist   = errors.New("the blamed file does not exist")
	ErrBlameFileNotEnoughLines = errors.New("the blamed file has not enough lines")

	notEnoughLinesRe = regexp.MustCompile(`^fatal: file .+ has only \d+ lines?\n$`)
)

// LineBlame returns the latest commit at the given line
func (repo *Repository) LineBlame(revision, file string, line uint64) (*Commit, uint64, error) {
	res, _, gitErr := NewCommand(repo.Ctx, "blame").
		AddOptionFormat("-L %d,%d", line, line).
		AddOptionValues("-p", revision).
		AddDashesAndList(file).RunStdString(&RunOpts{Dir: repo.Path})
	if gitErr != nil {
		stdErr := gitErr.Stderr()

		if stdErr == fmt.Sprintf("fatal: no such path %s in %s\n", file, revision) {
			return nil, 0, ErrBlameFileDoesNotExist
		}
		if notEnoughLinesRe.MatchString(stdErr) {
			return nil, 0, ErrBlameFileNotEnoughLines
		}

		return nil, 0, gitErr
	}

	objectFormat, err := repo.GetObjectFormat()
	if err != nil {
		return nil, 0, err
	}

	objectIDLen := objectFormat.FullLength()

	if len(res) < objectIDLen+1 {
		return nil, 0, fmt.Errorf("output of blame is invalid, cannot contain commit ID: %s", res)
	}

	commit, err := repo.GetCommit(res[:objectIDLen])
	if err != nil {
		return nil, 0, fmt.Errorf("GetCommit: %w", err)
	}

	endIdxOriginalLineNo := strings.IndexRune(res[objectIDLen+1:], ' ')
	if endIdxOriginalLineNo == -1 {
		return nil, 0, fmt.Errorf("output of blame is invalid, cannot contain original line number: %s", res)
	}

	originalLineNo, err := strconv.ParseUint(res[objectIDLen+1:objectIDLen+1+endIdxOriginalLineNo], 10, 64)
	if err != nil {
		return nil, 0, fmt.Errorf("strconv.ParseUint: %w", err)
	}

	return commit, originalLineNo, nil
}
