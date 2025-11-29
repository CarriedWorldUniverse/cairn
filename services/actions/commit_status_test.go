// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"testing"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/require"
)

func TestCreateCommitStatus_IncompleteMatrix(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	job := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: 192})

	// Normally this job will attempt to create a commit status on a commit that doesn't exist in the test repo,
	// resulting in an error due to the test fixture data not matching the related repos. But it tried.
	err := createCommitStatus(t.Context(), job)
	require.ErrorContains(t, err, "object does not exist [id: 7a3858dc7f059543a8807a8b551304b7e362a7ef")

	// Transition from IsIncompleteMatrix()=false to true...
	isIncomplete, err := job.IsIncompleteMatrix()
	require.NoError(t, err)
	require.False(t, isIncomplete)
	job.WorkflowPayload = append(job.WorkflowPayload, "\nincomplete_matrix: true\n"...)
	isIncomplete, err = job.IsIncompleteMatrix()
	require.NoError(t, err)
	require.True(t, isIncomplete)

	// Now there should be no error since createCommitStatus will exit early due to the IsIncompleteMatrix() flag.
	err = createCommitStatus(t.Context(), job)
	require.NoError(t, err)
}
