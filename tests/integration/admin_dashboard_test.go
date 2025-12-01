// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package integration

import (
	"fmt"
	"net/http"
	"testing"

	"forgejo.org/modules/setting"
	"forgejo.org/modules/test"
	"forgejo.org/modules/translation"
	"forgejo.org/tests"
)

var commonEntries = []string{
	"delete_inactive_accounts",
	"delete_repo_archives",
	"delete_missing_repos",
	"git_gc_repos",
	"resync_all_hooks",
	"reinit_missing_repos",
	"sync_external_users",
	"repo_health_check",
	"delete_generated_repository_avatars",
	"sync_repo_branches",
	"sync_repo_tags",
}

var sshEntries = []string{
	"resync_all_sshkeys",
	"resync_all_sshprincipals",
}

func testAssertAdminDashboardEntries(t *testing.T, page *HTMLDoc, locale translation.Locale, expectSSH bool) {
	for _, entry := range commonEntries {
		page.AssertSelection(t, page.FindByText("table tr td", locale.TrString(fmt.Sprintf("admin.dashboard.%s", entry))), true)
		page.AssertSelection(t, page.Find(fmt.Sprintf("table tr td button[value='%s']", entry)), true)
	}
	for _, entry := range sshEntries {
		page.AssertSelection(t, page.FindByText("table tr td", locale.TrString(fmt.Sprintf("admin.dashboard.%s", entry))), expectSSH)
		page.AssertSelection(t, page.Find(fmt.Sprintf("table tr td button[value='%s']", entry)), expectSSH)
	}
}

func TestAdminDashboard(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	session := loginUser(t, "user1")
	locale := translation.NewLocale("en-US")
	url := "/admin"

	t.Run("SSH disabled", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()
		defer test.MockVariableValue(&setting.SSH.Disabled, true)()

		page := NewHTMLParser(t, session.MakeRequest(t, NewRequest(t, "GET", url), http.StatusOK).Body)
		testAssertAdminDashboardEntries(t, page, locale, false)
	})

	t.Run("SSH enabled, but built-in", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()
		defer test.MockVariableValue(&setting.SSH.Disabled, false)()
		defer test.MockVariableValue(&setting.SSH.StartBuiltinServer, true)()

		page := NewHTMLParser(t, session.MakeRequest(t, NewRequest(t, "GET", url), http.StatusOK).Body)
		testAssertAdminDashboardEntries(t, page, locale, false)
	})

	t.Run("SSH enabled and external", func(t *testing.T) {
		defer tests.PrintCurrentTest(t)()
		defer test.MockVariableValue(&setting.SSH.Disabled, false)()
		defer test.MockVariableValue(&setting.SSH.StartBuiltinServer, false)()

		page := NewHTMLParser(t, session.MakeRequest(t, NewRequest(t, "GET", url), http.StatusOK).Body)
		testAssertAdminDashboardEntries(t, page, locale, true)
	})
}
