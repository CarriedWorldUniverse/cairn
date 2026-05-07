// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package org_test

import (
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/unittest"
	"github.com/CarriedWorldUniverse/cairn/routers/web/org"
	"github.com/CarriedWorldUniverse/cairn/services/contexttest"

	"github.com/stretchr/testify/assert"
)

func TestCheckProjectColumnChangePermissions(t *testing.T) {
	unittest.PrepareTestEnv(t)
	ctx, _ := contexttest.MockContext(t, "user2/-/projects/4/4")
	contexttest.LoadUser(t, ctx, 2)
	ctx.ContextUser = ctx.Doer // user2
	ctx.SetParams(":id", "4")
	ctx.SetParams(":columnID", "4")

	project, column := org.CheckProjectColumnChangePermissions(ctx)
	assert.NotNil(t, project)
	assert.NotNil(t, column)
	assert.False(t, ctx.Written())
}
