// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package mailer

import (
	"context"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/models/db"
	org_model "github.com/CarriedWorldUniverse/cairn/models/organization"
	"github.com/CarriedWorldUniverse/cairn/models/unittest"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	"github.com/CarriedWorldUniverse/cairn/modules/templates"
	"github.com/CarriedWorldUniverse/cairn/modules/test"
	"github.com/CarriedWorldUniverse/cairn/modules/translation"

	_ "github.com/CarriedWorldUniverse/cairn/modules/testimport"

	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	unittest.MainTest(m)
}

func AssertTranslatedLocale(t *testing.T, message string, prefixes ...string) {
	t.Helper()
	for _, prefix := range prefixes {
		assert.NotContains(t, message, prefix, "there is an untranslated locale prefix")
	}
}

func MockMailSettings(send func(msgs ...*Message)) func() {
	translation.InitLocales(context.Background())
	subjectTemplates, bodyTemplates = templates.Mailer(context.Background())
	mailService := setting.Mailer{
		From: "test@gitea.com",
	}
	cleanups := []func(){
		test.MockVariableValue(&setting.MailService, &mailService),
		test.MockVariableValue(&setting.Domain, "localhost"),
		test.MockVariableValue(&SendAsync, send),
	}
	return func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}
}

func CleanUpUsers(ctx context.Context, users []*user_model.User) {
	for _, u := range users {
		if u.IsOrganization() {
			org_model.DeleteOrganization(ctx, (*org_model.Organization)(u))
		} else {
			db.DeleteByID[user_model.User](ctx, u.ID)
			db.DeleteByBean(ctx, &user_model.EmailAddress{UID: u.ID})
		}
	}
}
