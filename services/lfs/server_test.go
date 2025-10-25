// Copyright 2025 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package lfs

import (
	"fmt"
	"strings"
	"testing"
	"time"

	perm_model "forgejo.org/models/perm"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	"forgejo.org/modules/setting"
	"forgejo.org/services/contexttest"

	_ "forgejo.org/models/forgefed"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}

type authTokenOptions struct {
	Op     string
	UserID int64
	RepoID int64
}

func getLFSAuthTokenWithBearer(opts authTokenOptions) (string, error) {
	now := time.Now()
	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(setting.LFS.HTTPAuthExpiry)),
			NotBefore: jwt.NewNumericDate(now),
		},
		RepoID: opts.RepoID,
		Op:     opts.Op,
		UserID: opts.UserID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// Sign and get the complete encoded token as a string using the secret
	tokenString, err := token.SignedString(setting.LFS.JWTSecretBytes)
	if err != nil {
		return "", fmt.Errorf("failed to sign LFS JWT token: %w", err)
	}
	return "Bearer " + tokenString, nil
}

func TestAuthenticate(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())
	repo1 := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 1})

	token2, _ := getLFSAuthTokenWithBearer(authTokenOptions{Op: "download", UserID: 2, RepoID: 1})
	_, token2, _ = strings.Cut(token2, " ")
	ctx, _ := contexttest.MockContext(t, "/")

	t.Run("handleLFSToken", func(t *testing.T) {
		u, err := handleLFSToken(ctx, "", repo1, perm_model.AccessModeRead)
		require.Error(t, err)
		assert.Nil(t, u)

		u, err = handleLFSToken(ctx, "invalid", repo1, perm_model.AccessModeRead)
		require.Error(t, err)
		assert.Nil(t, u)

		u, err = handleLFSToken(ctx, token2, repo1, perm_model.AccessModeRead)
		require.NoError(t, err)
		assert.EqualValues(t, 2, u.ID)
	})

	t.Run("authenticate", func(t *testing.T) {
		const prefixBearer = "Bearer "
		assert.False(t, authenticate(ctx, repo1, "", true, false))
		assert.False(t, authenticate(ctx, repo1, prefixBearer+"invalid", true, false))
		assert.True(t, authenticate(ctx, repo1, prefixBearer+token2, true, false))
	})
}
