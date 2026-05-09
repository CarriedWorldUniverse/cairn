// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package issues_test

import (
	"testing"

	issues_model "github.com/CarriedWorldUniverse/cairn/models/issues"
	"github.com/CarriedWorldUniverse/cairn/models/unittest"

	"github.com/stretchr/testify/require"
)

func TestFixturesAreConsistent(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	unittest.CheckConsistencyFor(t,
		&issues_model.Issue{},
		&issues_model.PullRequest{},
		&issues_model.Milestone{},
		&issues_model.Label{},
	)
}

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}
