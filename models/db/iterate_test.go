// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package db_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"forgejo.org/models/db"
	git_model "forgejo.org/models/git"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIterate(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	xe := unittest.GetXORMEngine()
	require.NoError(t, xe.Sync(&repo_model.RepoUnit{}))
	defer test.MockVariableValue(&setting.Database.IterateBufferSize, 50)()

	cnt, err := db.GetEngine(db.DefaultContext).Count(&repo_model.RepoUnit{})
	require.NoError(t, err)

	var repoUnitCnt int
	err = db.Iterate(db.DefaultContext, nil, func(ctx context.Context, repo *repo_model.RepoUnit) error {
		repoUnitCnt++
		return nil
	})
	require.NoError(t, err)
	assert.EqualValues(t, cnt, repoUnitCnt)

	err = db.Iterate(db.DefaultContext, nil, func(ctx context.Context, repoUnit *repo_model.RepoUnit) error {
		has, err := db.ExistByID[repo_model.RepoUnit](ctx, repoUnit.ID)
		if err != nil {
			return err
		}
		if !has {
			return db.ErrNotExist{Resource: "repo_unit", ID: repoUnit.ID}
		}
		return nil
	})
	require.NoError(t, err)
}

func TestIterateMultipleFields(t *testing.T) {
	for _, bufferSize := range []int{1, 2, 3, 10} { // 8 records in fixture
		t.Run(fmt.Sprintf("No Modifications bufferSize=%d", bufferSize), func(t *testing.T) {
			require.NoError(t, unittest.PrepareTestDatabase())

			// Fetch all the commit status IDs...
			var remainingIDs []int64
			err := db.GetEngine(t.Context()).Table(&git_model.CommitStatus{}).Cols("id").Find(&remainingIDs)
			require.NoError(t, err)
			require.NotEmpty(t, remainingIDs)

			// Ensure that every repo unit ID is found when doing iterate:
			err = db.IterateByKeyset(t.Context(),
				nil,
				[]string{"repo_id", "sha", "context", "index", "id"},
				bufferSize,
				func(ctx context.Context, commit_status *git_model.CommitStatus) error {
					remainingIDs = slices.DeleteFunc(remainingIDs, func(n int64) bool {
						return commit_status.ID == n
					})
					return nil
				})
			require.NoError(t, err)
			assert.Empty(t, remainingIDs)
		})
	}
}
