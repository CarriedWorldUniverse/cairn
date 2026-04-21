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

	// Sign and get the complete encoded token as a string using the secret
	tokenString, err := setting.LFS.SigningKey.JWT(claims)
	if err != nil {
		return "", fmt.Errorf("failed to sign LFS JWT token: %w", err)
	}
	return "Bearer " + tokenString, nil
}

func testAuthenticate(t *testing.T, cfg string) {
	require.NoError(t, unittest.PrepareTestDatabase())
	var err error
	setting.CfgProvider, err = setting.NewConfigProviderFromData(cfg)
	require.NoError(t, err, "Config")
	setting.LoadCommonSettings()
	assert.True(t, setting.LFS.StartServer, "LFS_START_SERVER = true")
	assert.NotNil(t, setting.LFS.SigningKey, "SigningKey initialized")
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

type namedCfg struct {
	name, cfg string
}

var iniCommon = `[security]
INSTALL_LOCK = true
INTERNAL_TOKEN = ForgejoForgejoForgejoForgejoForgejoForgejo_	# don't use in prod
[oauth2]
JWT_SECRET = ForgejoForgejoForgejoForgejoForgejoForgejo_	# don't use in prod
[server]
LFS_START_SERVER = true
	`

var cfgVariants = []namedCfg{
	{name: "HS256_default", cfg: `LFS_JWT_SECRET = ForgejoForgejoForgejoForgejoForgejoForgejo_`},
	{name: "RS256", cfg: `LFS_JWT_SIGNING_ALGORITHM = RS256`},
}

func TestAuthenticate(t *testing.T) {
	// TODO: #11024
	setting.InstallLock = true
	for _, v := range cfgVariants {
		cfg := iniCommon + v.cfg
		t.Run(v.name, func(t *testing.T) { testAuthenticate(t, cfg) })
	}
}
