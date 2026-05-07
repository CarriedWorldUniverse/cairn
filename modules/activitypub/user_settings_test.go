// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package activitypub_test

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/db"
	"github.com/CarriedWorldUniverse/cairn/models/unittest"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/activitypub"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserSettings(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	user1 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	pub, priv, err := activitypub.GetKeyPair(db.DefaultContext, user1)
	require.NoError(t, err)
	pub1, err := activitypub.GetPublicKey(db.DefaultContext, user1)
	require.NoError(t, err)
	assert.Equal(t, pub, pub1)
	priv1, err := activitypub.GetPrivateKey(db.DefaultContext, user1)
	require.NoError(t, err)
	assert.Equal(t, priv, priv1)
}
