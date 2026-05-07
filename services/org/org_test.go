// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package org

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models"
	"github.com/CarriedWorldUniverse/cairn/models/actions"
	"github.com/CarriedWorldUniverse/cairn/models/db"
	"github.com/CarriedWorldUniverse/cairn/models/organization"
	"github.com/CarriedWorldUniverse/cairn/models/unittest"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/optional"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}

func TestDeleteOrganization(t *testing.T) {
	defer unittest.OverrideFixtures("services/org/TestDeleteOrganization")()
	require.NoError(t, unittest.PrepareTestDatabase())

	org := unittest.AssertExistsAndLoadBean(t, &organization.Organization{ID: 6})
	require.NoError(t, DeleteOrganization(db.DefaultContext, org, false))
	unittest.AssertNotExistsBean(t, &organization.Organization{ID: 6})
	unittest.AssertNotExistsBean(t, &organization.OrgUser{OrgID: 6})
	unittest.AssertNotExistsBean(t, &organization.Team{OrgID: 6})
	unittest.AssertNotExistsBean(t, &actions.ActionRunnerToken{OwnerID: optional.Some[int64](6)})
	unittest.AssertNotExistsBean(t, &user_model.Follow{FollowID: 6})
	unittest.AssertNotExistsBean(t, &user_model.BlockedUser{UserID: 6})

	org = unittest.AssertExistsAndLoadBean(t, &organization.Organization{ID: 3})
	err := DeleteOrganization(db.DefaultContext, org, false)
	require.Error(t, err)
	assert.True(t, models.IsErrUserOwnRepos(err))

	user := unittest.AssertExistsAndLoadBean(t, &organization.Organization{ID: 5})
	require.Error(t, DeleteOrganization(db.DefaultContext, user, false))
	unittest.CheckConsistencyFor(t, &user_model.User{}, &organization.Team{})

	assert.Zero(t, unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1001}).NumFollowing)
}
