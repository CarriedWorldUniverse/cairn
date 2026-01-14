// Copyright 2026 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package secrets

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateName(t *testing.T) {
	testCases := []struct {
		name  string
		valid bool
	}{
		{"FORGEJO_", false},
		{"FORGEJO_123", false},
		{"FORGEJO_ABC", false},
		{"GITEA_", false},
		{"GITEA_123", false},
		{"GITEA_ABC", false},
		{"GITHUB_", false},
		{"GITHUB_123", false},
		{"GITHUB_ABC", false},
		{"123_TEST", false},
		{"CI", true},
		{"_CI", true},
		{"CI_", true},
		{"CI123", true},
		{"CIABC", true},
		{"FORGEJO", true},
		{"FORGEJO123", true},
		{"FORGEJOABC", true},
		{"GITEA", true},
		{"GITEA123", true},
		{"GITEAABC", true},
		{"GITHUB", true},
		{"GITHUB123", true},
		{"GITHUBABC", true},
		{"_123_TEST", true},
	}
	for _, tC := range testCases {
		t.Run(tC.name, func(t *testing.T) {
			t.Helper()
			if tC.valid {
				assert.NoError(t, ValidateName(tC.name))
				assert.NoError(t, ValidateName(strings.ToLower(tC.name)))
			} else {
				require.ErrorIs(t, ValidateName(tC.name), ErrInvalidName)
				require.ErrorIs(t, ValidateName(strings.ToLower(tC.name)), ErrInvalidName)
			}
		})
	}
}
