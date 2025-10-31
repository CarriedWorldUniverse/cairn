// Copyright 2024 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	unit_model "forgejo.org/models/unit"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/git"
	"forgejo.org/modules/indexer/stats"
	"forgejo.org/modules/optional"
	"forgejo.org/modules/timeutil"
	issue_service "forgejo.org/services/issue"
	files_service "forgejo.org/services/repository/files"
	"forgejo.org/services/wiki"
	"forgejo.org/tests"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"xorm.io/xorm/convert"
)

// first entry represents filename
// the following entries define the full file content over time
type FileChanges struct {
	Filename  string
	CommitMsg string
	Versions  []string
}

// performs additional repo setup as needed
type SetupRepo func(*user_model.User, *repo_model.Repository)

// put your Git repo declarations in here
// feel free to amend the helper function below or use the raw variant directly
func DeclareGitRepos(t *testing.T) func() {
	now := timeutil.TimeStampNow()
	postIssue := func(repo *repo_model.Repository, user *user_model.User, age int64, title, content string) {
		issue := &issues_model.Issue{
			RepoID:      repo.ID,
			PosterID:    user.ID,
			Title:       title,
			Content:     content,
			CreatedUnix: now.Add(-age),
		}
		require.NoError(t, issue_service.NewIssue(db.DefaultContext, repo, issue, nil, nil, nil))
	}

	cleanupFunctions := []func(){
		newRepo(t, 2, "diff-test", nil, []FileChanges{{
			Filename: "testfile",
			Versions: []string{"hello", "hallo", "hola", "native", "ubuntu-latest", "- runs-on: ubuntu-latest", "- runs-on: debian-latest"},
		}}, nil),
		newRepo(t, 2, "language-stats-test", nil, []FileChanges{{
			Filename: "main.rs",
			Versions: []string{"fn main() {", "println!(\"Hello World!\");", "}"},
		}}, nil),
		newRepo(t, 2, "mentions-highlighted", nil, []FileChanges{
			{
				Filename:  "history1.md",
				Versions:  []string{""},
				CommitMsg: "A commit message which mentions @user2 in the title\nand has some additional text which mentions @user1",
			},
			{
				Filename:  "history2.md",
				Versions:  []string{""},
				CommitMsg: "Another commit which mentions @user1 in the title\nand @user2 in the text",
			},
		}, nil),
		newRepo(t, 2, "unicode-escaping", &tests.DeclarativeRepoOptions{
			EnabledUnits: optional.Some([]unit_model.Type{unit_model.TypeCode, unit_model.TypeWiki}),
		}, []FileChanges{{
			Filename: "a-file",
			Versions: []string{"{a}{а}"},
		}}, func(user *user_model.User, repo *repo_model.Repository) {
			wiki.InitWiki(db.DefaultContext, repo)
			wiki.AddWikiPage(db.DefaultContext, user, repo, "Home", "{a}{а}", "{a}{а}")
			wiki.AddWikiPage(db.DefaultContext, user, repo, "_Sidebar", "{a}{а}", "{a}{а}")
			wiki.AddWikiPage(db.DefaultContext, user, repo, "_Footer", "{a}{а}", "{a}{а}")
		}),
		newRepo(t, 2, "multiple-combo-boxes", nil, []FileChanges{{
			Filename: ".forgejo/issue_template/multi-combo-boxes.yaml",
			Versions: []string{`
name: "Multiple combo-boxes"
description: "To show something"
body:
- type: textarea
  id: textarea-one
  attributes:
    label: one
- type: textarea
  id: textarea-two
  attributes:
    label: two
`},
		}}, nil),
		newRepo(t, 11, "dependency-test", &tests.DeclarativeRepoOptions{
			UnitConfig: optional.Some(map[unit_model.Type]convert.Conversion{
				unit_model.TypeIssues: &repo_model.IssuesConfig{
					EnableDependencies: true,
				},
			}),
		}, []FileChanges{}, func(user *user_model.User, repo *repo_model.Repository) {
			postIssue(repo, user, 500, "first issue here", "an issue created earlier")
			postIssue(repo, user, 400, "second issue here (not 1)", "not the right issue, but in the right repo")
			postIssue(repo, user, 300, "third issue here", "depends on things")
			postIssue(repo, user, 200, "unrelated issue", "shrug emoji")
			postIssue(repo, user, 100, "newest issue", "very new")
		}),
		newRepo(t, 11, "dependency-test-2", &tests.DeclarativeRepoOptions{
			UnitConfig: optional.Some(map[unit_model.Type]convert.Conversion{
				unit_model.TypeIssues: &repo_model.IssuesConfig{
					EnableDependencies: true,
				},
			}),
		}, []FileChanges{}, func(user *user_model.User, repo *repo_model.Repository) {
			postIssue(repo, user, 450, "right issue", "an issue containing word right")
			postIssue(repo, user, 150, "left issue", "an issue containing word left")
		}),
		newRepo(t, 2, "long-diff-test", nil, []FileChanges{{
			Filename: "test-README.md",
			Versions: []string{
				readStringFile(t, "tests/e2e/declarative-repo/long-diff-test/0-README.md"),
			},
		}}, func(user *user_model.User, repo *repo_model.Repository) {
			commit1Sha := addCommitToBranch(t, user, repo, "main", "test-branch", "test-README.md", "",
				readStringFile(t, "tests/e2e/declarative-repo/long-diff-test/1-README.md"))
			commit2Sha := addCommitToBranch(t, user, repo, "test-branch", "test-branch", "test-README.md", commit1Sha,
				readStringFile(t, "tests/e2e/declarative-repo/long-diff-test/2-README.md"))
			addCommitToBranch(t, user, repo, "test-branch", "test-branch", "test-README.md", commit2Sha,
				readStringFile(t, "tests/e2e/declarative-repo/long-diff-test/3-README.md"))
		}),
		// add your repo declarations here
	}

	return func() {
		for _, cleanup := range cleanupFunctions {
			cleanup()
		}
	}
}

func readStringFile(t *testing.T, fn string) string {
	c, err := os.ReadFile(fn)
	require.NoError(t, err)
	return string(c)
}

func newRepo(t *testing.T, userID int64, repoName string, initOpts *tests.DeclarativeRepoOptions, fileChanges []FileChanges, setup SetupRepo) func() {
	user := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: userID})

	opts := tests.DeclarativeRepoOptions{}
	if initOpts != nil {
		opts = *initOpts
	}
	opts.Name = optional.Some(repoName)
	if !opts.EnabledUnits.Has() {
		opts.EnabledUnits = optional.Some([]unit_model.Type{unit_model.TypeCode, unit_model.TypeIssues})
	}
	somerepo, _, cleanupFunc := tests.CreateDeclarativeRepoWithOptions(t, user, opts)

	var lastCommitID string
	for _, file := range fileChanges {
		for i, version := range file.Versions {
			operation := "update"
			if i == 0 {
				operation = "create"
			}

			// default to unique commit messages
			commitMsg := file.CommitMsg
			if commitMsg == "" {
				commitMsg = fmt.Sprintf("Patch: %s-%d", file.Filename, i+1)
			}

			resp, err := files_service.ChangeRepoFiles(git.DefaultContext, somerepo, user, &files_service.ChangeRepoFilesOptions{
				Files: []*files_service.ChangeRepoFile{{
					Operation:     operation,
					TreePath:      file.Filename,
					ContentReader: strings.NewReader(version),
				}},
				Message:   commitMsg,
				OldBranch: "main",
				NewBranch: "main",
				Author: &files_service.IdentityOptions{
					Name:  user.Name,
					Email: user.Email,
				},
				Committer: &files_service.IdentityOptions{
					Name:  user.Name,
					Email: user.Email,
				},
				Dates: &files_service.CommitDateOptions{
					Author:    time.Now(),
					Committer: time.Now(),
				},
				LastCommitID: lastCommitID,
			})
			require.NoError(t, err)
			assert.NotEmpty(t, resp)

			lastCommitID = resp.Commit.SHA
		}
	}

	if setup != nil {
		setup(user, somerepo)
	}

	err := stats.UpdateRepoIndexer(somerepo)
	require.NoError(t, err)

	return cleanupFunc
}

func addCommitToBranch(t *testing.T, user *user_model.User, repo *repo_model.Repository, oldBranch, newBranch, filename, lastSha, content string) string {
	resp, err := files_service.ChangeRepoFiles(git.DefaultContext, repo, user, &files_service.ChangeRepoFilesOptions{
		Files: []*files_service.ChangeRepoFile{{
			Operation:     "update",
			TreePath:      filename,
			ContentReader: strings.NewReader(content),
		}},
		Message:   "add commit to branch",
		OldBranch: oldBranch,
		NewBranch: newBranch,
		Author: &files_service.IdentityOptions{
			Name:  user.Name,
			Email: user.Email,
		},
		Committer: &files_service.IdentityOptions{
			Name:  user.Name,
			Email: user.Email,
		},
		Dates: &files_service.CommitDateOptions{
			Author:    time.Now(),
			Committer: time.Now(),
		},
		LastCommitID: lastSha,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, resp)
	return resp.Commit.SHA
}
