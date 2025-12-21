// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"net/http"
	"testing"

	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
)

func TestActionRunsList(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/user5/repo4/actions")
	resp := MakeRequest(t, req, http.StatusOK)

	htmlDoc := NewHTMLParser(t, resp.Body)

	runSubLine := htmlDoc.Find(".run-list .flex-item-body").Text()
	assert.Contains(t, runSubLine, "Commit")
	assert.NotContains(t, runSubLine, "-Commit")
}
