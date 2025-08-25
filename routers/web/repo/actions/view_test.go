// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package actions

import (
	"fmt"
	"testing"

	actions_model "forgejo.org/models/actions"
	unittest "forgejo.org/models/unittest"
	"forgejo.org/services/contexttest"

	"github.com/stretchr/testify/assert"
)

func Test_artifactsFind(t *testing.T) {
	unittest.PrepareTestEnv(t)

	for _, testCase := range []struct {
		name         string
		artifactName string
		count        int
	}{
		{
			name:         "Found",
			artifactName: "artifact-v4-download",
			count:        1,
		},
		{
			name:         "NotFound",
			artifactName: "notexist",
			count:        0,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runID := int64(792)
			ctx, _ := contexttest.MockContext(t, fmt.Sprintf("user5/repo4/actions/runs/%v/artifacts/%v", runID, testCase.artifactName))
			artifacts := artifactsFind(ctx, actions_model.FindArtifactsOptions{
				RunID:        runID,
				ArtifactName: testCase.artifactName,
			})
			assert.False(t, ctx.Written())
			assert.Len(t, artifacts, testCase.count)
		})
	}
}

func Test_artifactsFindByNameOrID(t *testing.T) {
	unittest.PrepareTestEnv(t)

	for _, testCase := range []struct {
		name     string
		nameOrID string
		err      string
	}{
		{
			name:     "NameFound",
			nameOrID: "artifact-v4-download",
		},
		{
			name:     "NameNotFound",
			nameOrID: "notexist",
			err:      "artifact name not found",
		},
		{
			name:     "IDFound",
			nameOrID: "22",
		},
		{
			name:     "IDNotFound",
			nameOrID: "666",
			err:      "artifact ID not found",
		},
		{
			name:     "IDZeroNotFound",
			nameOrID: "0",
			err:      "artifact name not found",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runID := int64(792)
			ctx, resp := contexttest.MockContext(t, fmt.Sprintf("user5/repo4/actions/runs/%v/artifacts/%v", runID, testCase.nameOrID))
			artifacts := artifactsFindByNameOrID(ctx, runID, testCase.nameOrID)
			if testCase.err == "" {
				assert.NotEmpty(t, artifacts)
				assert.False(t, ctx.Written(), resp.Body.String())
			} else {
				assert.Empty(t, artifacts)
				assert.True(t, ctx.Written())
				assert.Contains(t, resp.Body.String(), testCase.err)
			}
		})
	}
}
