// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package forgejo_migrations

import (
	"xorm.io/xorm"
)

func init() {
	registerMigration(&Migration{
		Description: "add foreign keys to pull_request, base_repo_id & issue_id",
		Upgrade:     addForeignKeysPullRequest1,
	})
}

func addForeignKeysPullRequest1(x *xorm.Engine) error {
	type PullRequest struct {
		IssueID    int64 `xorm:"INDEX REFERENCES(issue, id)"`
		BaseRepoID int64 `xorm:"INDEX REFERENCES(repository, id)"`
	}
	return syncDoctorForeignKey(x, []any{
		new(PullRequest),
	})
}
