// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package auth_test

import (
	"testing"
	"time"

	auth_model "forgejo.org/models/auth"
	"forgejo.org/models/db"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeAuthorizedIntegration(t *testing.T) *auth_model.AuthorizedIntegration {
	t.Helper()
	ai := &auth_model.AuthorizedIntegration{
		UserID:           2,
		Scope:            auth_model.AccessTokenScopeAll,
		ResourceAllRepos: true,
		Issuer:           "https://example.org/",
		ClaimRules:       &auth_model.ClaimRules{},
	}
	require.NoError(t, auth_model.InsertAuthorizedIntegration(t.Context(), ai))
	return ai
}

func TestGetAuthorizedIntegration(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	ai := makeAuthorizedIntegration(t)

	get, err := auth_model.GetAuthorizedIntegration(t.Context(), "abc", "123")
	require.ErrorIs(t, err, util.ErrNotExist)
	assert.Nil(t, get)

	get, err = auth_model.GetAuthorizedIntegration(t.Context(), ai.Issuer, ai.Audience)
	require.NoError(t, err)
	require.NotNil(t, get)
	assert.Equal(t, ai.ID, get.ID)
}

func TestAuthorizedIntegrationUpdateLastUsed(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	ai := makeAuthorizedIntegration(t)
	ai.UpdatedUnix = 0
	cnt, err := db.GetEngine(t.Context()).ID(ai.ID).Cols("updated_unix").NoAutoTime().Update(ai)
	require.NoError(t, err)
	assert.EqualValues(t, 1, cnt)

	timeutil.MockSet(time.Unix(1777130023, 0))
	defer timeutil.MockUnset()

	assert.EqualValues(t, 0, ai.UpdatedUnix)
	require.NoError(t, ai.UpdateLastUsed(t.Context()))
	assert.EqualValues(t, 1777130023, ai.UpdatedUnix) // object field updated
	assert.EqualValues(t, 1777130023, unittest.AssertExistsAndLoadBean(t, &auth_model.AuthorizedIntegration{ID: ai.ID}).UpdatedUnix)

	// nearly immediate redo should have same timestamp due to the 1 minute deduplication:
	timeutil.MockSet(time.Unix(1777130025, 0))
	require.NoError(t, ai.UpdateLastUsed(t.Context()))
	assert.EqualValues(t, 1777130023, ai.UpdatedUnix)                                                                                // object field not updated
	assert.EqualValues(t, 1777130023, unittest.AssertExistsAndLoadBean(t, &auth_model.AuthorizedIntegration{ID: ai.ID}).UpdatedUnix) // database field not updated

	// but if it's a little while later..
	timeutil.MockSet(time.Unix(1777131139, 0))
	require.NoError(t, ai.UpdateLastUsed(t.Context()))
	assert.EqualValues(t, 1777131139, ai.UpdatedUnix)                                                                                // object field updated
	assert.EqualValues(t, 1777131139, unittest.AssertExistsAndLoadBean(t, &auth_model.AuthorizedIntegration{ID: ai.ID}).UpdatedUnix) // database field updated
}

func TestNewAuthorizedIntegration(t *testing.T) {
	ai := &auth_model.AuthorizedIntegration{
		UserID:           2,
		Scope:            auth_model.AccessTokenScopeAll,
		ResourceAllRepos: true,
		Issuer:           "https://example.org/",
		ClaimRules:       &auth_model.ClaimRules{},
	}
	require.NoError(t, auth_model.InsertAuthorizedIntegration(t.Context(), ai))
	assert.Contains(t, ai.Audience, "u:2:")

	ai = &auth_model.AuthorizedIntegration{
		UserID:           2,
		Scope:            auth_model.AccessTokenScopeAll,
		ResourceAllRepos: true,
		Issuer:           "https://example.org/",
		Audience:         "I made my own audience",
		ClaimRules:       &auth_model.ClaimRules{},
	}
	require.ErrorContains(t, auth_model.InsertAuthorizedIntegration(t.Context(), ai), "audience cannot be provided")

	ai = &auth_model.AuthorizedIntegration{
		// Forgot to set UserID
		Scope:            auth_model.AccessTokenScopeAll,
		ResourceAllRepos: true,
		Issuer:           "https://example.org/",
		ClaimRules:       &auth_model.ClaimRules{},
	}
	require.ErrorContains(t, auth_model.InsertAuthorizedIntegration(t.Context(), ai), "UserID must be initialized")
}
