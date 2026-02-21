// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package project

import (
	"testing"

	"forgejo.org/models/db"
	"forgejo.org/models/unittest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsProjectTypeValid(t *testing.T) {
	const UnknownType Type = 15

	cases := []struct {
		typ   Type
		valid bool
	}{
		{TypeIndividual, true},
		{TypeRepository, true},
		{TypeOrganization, true},
		{UnknownType, false},
	}

	for _, v := range cases {
		assert.Equal(t, v.valid, IsTypeValid(v.typ))
	}
}

func TestGetProjects(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	projects, err := db.Find[Project](db.DefaultContext, SearchOptions{RepoID: 1})
	require.NoError(t, err)

	// 1 value for this repo exists in the fixtures
	assert.Len(t, projects, 1)

	projects, err = db.Find[Project](db.DefaultContext, SearchOptions{RepoID: 3})
	require.NoError(t, err)

	// 1 value for this repo exists in the fixtures
	assert.Len(t, projects, 1)
}

func TestProjectsSort(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	tests := []struct {
		sortType string
		wants    []int64
	}{
		{
			sortType: "default",
			wants:    []int64{1, 3, 2, 7, 6, 5, 4},
		},
		{
			sortType: "oldest",
			wants:    []int64{4, 5, 6, 7, 2, 3, 1},
		},
		{
			sortType: "recentupdate",
			wants:    []int64{1, 3, 2, 7, 6, 5, 4},
		},
		{
			sortType: "leastupdate",
			wants:    []int64{4, 5, 6, 7, 2, 3, 1},
		},
	}

	for _, tt := range tests {
		projects, count, err := db.FindAndCount[Project](db.DefaultContext, SearchOptions{
			OrderBy: GetSearchOrderByBySortType(tt.sortType),
		})
		require.NoError(t, err)
		assert.Equal(t, int64(7), count)
		if assert.Len(t, projects, 7) {
			for i := range projects {
				assert.Equal(t, tt.wants[i], projects[i].ID)
			}
		}
	}
}
