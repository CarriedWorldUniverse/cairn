// Copyright 2024 The Forgejo Authors
// SPDX-License-Identifier: MIT

package actions

import (
	"errors"
	"slices"
	"testing"

	actions_model "forgejo.org/models/actions"
	"forgejo.org/models/db"
	issues_model "forgejo.org/models/issues"
	repo_model "forgejo.org/models/repo"
	"forgejo.org/models/unittest"
	user_model "forgejo.org/models/user"
	actions_module "forgejo.org/modules/actions"
	"forgejo.org/modules/git"
	api "forgejo.org/modules/structs"
	webhook_module "forgejo.org/modules/webhook"

	"code.forgejo.org/forgejo/runner/v11/act/jobparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_SkipPullRequestEvent(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	repoID := int64(1)
	commitSHA := "1234"

	// event is not webhook_module.HookEventPullRequestSync, never skip
	assert.False(t, SkipPullRequestEvent(db.DefaultContext, webhook_module.HookEventPullRequest, repoID, commitSHA))

	// event is webhook_module.HookEventPullRequestSync but there is nothing in the ActionRun table, do not skip
	assert.False(t, SkipPullRequestEvent(db.DefaultContext, webhook_module.HookEventPullRequestSync, repoID, commitSHA))

	// there is a webhook_module.HookEventPullRequest event but the SHA is different, do not skip
	index := int64(1)
	run := &actions_model.ActionRun{
		Index:     index,
		Event:     webhook_module.HookEventPullRequest,
		RepoID:    repoID,
		CommitSHA: "othersha",
	}
	unittest.AssertSuccessfulInsert(t, run)
	assert.False(t, SkipPullRequestEvent(db.DefaultContext, webhook_module.HookEventPullRequestSync, repoID, commitSHA))

	// there already is a webhook_module.HookEventPullRequest with the same SHA, skip
	index++
	run = &actions_model.ActionRun{
		Index:     index,
		Event:     webhook_module.HookEventPullRequest,
		RepoID:    repoID,
		CommitSHA: commitSHA,
	}
	unittest.AssertSuccessfulInsert(t, run)
	assert.True(t, SkipPullRequestEvent(db.DefaultContext, webhook_module.HookEventPullRequestSync, repoID, commitSHA))
}

func Test_IssueCommentOnForkPullRequestEvent(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 10})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 3})
	require.NoError(t, pr.LoadIssue(db.DefaultContext))

	require.True(t, pr.IsFromFork())

	commit := &git.Commit{
		ID:            git.MustIDFromString("0000000000000000000000000000000000000000"),
		CommitMessage: "test",
	}
	detectedWorkflows := []*actions_module.DetectedWorkflow{
		{
			TriggerEvent: &jobparser.Event{
				Name: "issue_comment",
			},
		},
	}
	input := &notifyInput{
		Repo:        repo,
		Doer:        doer,
		Event:       webhook_module.HookEventIssueComment,
		PullRequest: pr,
		Payload:     &api.IssueCommentPayload{},
	}

	unittest.AssertSuccessfulDelete(t, &actions_model.ActionRun{RepoID: repo.ID})

	err := handleWorkflows(db.DefaultContext, detectedWorkflows, commit, input, "")
	require.NoError(t, err)

	runs, err := db.Find[actions_model.ActionRun](db.DefaultContext, actions_model.FindRunOptions{
		RepoID: repo.ID,
	})
	require.NoError(t, err)
	require.Len(t, runs, 1)

	assert.Equal(t, webhook_module.HookEventIssueComment, runs[0].Event)
	assert.False(t, runs[0].IsForkPullRequest)
}

func Test_OpenForkPullRequestEvent(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 10})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 3})
	require.NoError(t, pr.LoadIssue(db.DefaultContext))

	require.True(t, pr.IsFromFork())

	commit := &git.Commit{
		ID:            git.MustIDFromString("0000000000000000000000000000000000000000"),
		CommitMessage: "test",
	}
	detectedWorkflows := []*actions_module.DetectedWorkflow{
		{
			TriggerEvent: &jobparser.Event{
				Name: "pull_request",
			},
		},
	}
	input := &notifyInput{
		Repo:        repo,
		Doer:        doer,
		Event:       webhook_module.HookEventPullRequest,
		PullRequest: pr,
		Payload:     &api.PullRequestPayload{},
	}

	unittest.AssertSuccessfulDelete(t, &actions_model.ActionRun{RepoID: repo.ID})

	err := handleWorkflows(db.DefaultContext, detectedWorkflows, commit, input, "")
	require.NoError(t, err)

	runs, err := db.Find[actions_model.ActionRun](db.DefaultContext, actions_model.FindRunOptions{
		RepoID: repo.ID,
	})
	require.NoError(t, err)
	require.Len(t, runs, 1)

	assert.Equal(t, webhook_module.HookEventPullRequest, runs[0].Event)
	assert.True(t, runs[0].IsForkPullRequest)
}

func TestActionsNotifierConcurrencyGroup(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 10})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 3})

	commit := &git.Commit{
		ID:            git.MustIDFromString("0000000000000000000000000000000000000000"),
		CommitMessage: "test",
	}
	detectedWorkflows := []*actions_module.DetectedWorkflow{
		{
			EntryName: "test.yml",
			TriggerEvent: &jobparser.Event{
				Name: "pull_request",
			},
			Content: []byte("{ on: pull_request, jobs: { j1: {} }}"),
		},
	}
	input := &notifyInput{
		Repo:        repo,
		Doer:        doer,
		Event:       webhook_module.HookEventPullRequestSync,
		PullRequest: pr,
		Payload:     &api.PullRequestPayload{},
	}

	err := handleWorkflows(db.DefaultContext, detectedWorkflows, commit, input, "refs/head/main")
	require.NoError(t, err)

	runs, err := db.Find[actions_model.ActionRun](db.DefaultContext, actions_model.FindRunOptions{
		RepoID: repo.ID,
	})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	firstRun := runs[0]

	assert.Equal(t, "refs/head/main_test.yml_pull_request__auto", firstRun.ConcurrencyGroup)
	assert.Equal(t, actions_model.CancelInProgress, firstRun.ConcurrencyType)
	assert.Equal(t, actions_model.StatusWaiting, firstRun.Status)

	// Also... check if CancelPreviousWithConcurrencyGroup is invoked from handleWorkflows by firing off a second
	// workflow and checking that the first one gets cancelled:

	err = handleWorkflows(db.DefaultContext, detectedWorkflows, commit, input, "refs/head/main")
	require.NoError(t, err)

	runs, err = db.Find[actions_model.ActionRun](db.DefaultContext, actions_model.FindRunOptions{
		RepoID: repo.ID,
	})
	require.NoError(t, err)
	require.Len(t, runs, 2)

	firstRunIndex := slices.IndexFunc(runs, func(run *actions_model.ActionRun) bool { return run.ID == firstRun.ID })
	require.NotEqual(t, -1, firstRunIndex)
	firstRun = runs[firstRunIndex]

	assert.Equal(t, "refs/head/main_test.yml_pull_request__auto", firstRun.ConcurrencyGroup)
	assert.Equal(t, actions_model.CancelInProgress, firstRun.ConcurrencyType)
	assert.Equal(t, actions_model.StatusCancelled, firstRun.Status)
}

func TestActionsPreExecutionErrorInvalidJobs(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 10})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 3})

	commit := &git.Commit{
		ID:            git.MustIDFromString("0000000000000000000000000000000000000000"),
		CommitMessage: "test",
	}
	detectedWorkflows := []*actions_module.DetectedWorkflow{
		{
			EntryName: "test.yml",
			TriggerEvent: &jobparser.Event{
				Name: "pull_request",
			},
			Content: []byte("{ on: pull_request, jobs: 'hello, I am the jobs!' }"),
		},
	}
	input := &notifyInput{
		Repo:        repo,
		Doer:        doer,
		Event:       webhook_module.HookEventPullRequestSync,
		PullRequest: pr,
		Payload:     &api.PullRequestPayload{},
	}

	err := handleWorkflows(db.DefaultContext, detectedWorkflows, commit, input, "refs/head/main")
	require.NoError(t, err)

	runs, err := db.Find[actions_model.ActionRun](db.DefaultContext, actions_model.FindRunOptions{
		RepoID: repo.ID,
	})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	createdRun := runs[0]

	assert.Equal(t, actions_model.StatusFailure, createdRun.Status)
	assert.Contains(t, createdRun.PreExecutionError, "actions.workflow.job_parsing_error%!(EXTRA *fmt.wrapError=")
}

func TestActionsPreExecutionEventDetectionError(t *testing.T) {
	require.NoError(t, unittest.PrepareTestDatabase())

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 10})
	doer := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 1})
	pr := unittest.AssertExistsAndLoadBean(t, &issues_model.PullRequest{ID: 3})

	commit := &git.Commit{
		ID:            git.MustIDFromString("0000000000000000000000000000000000000000"),
		CommitMessage: "test",
	}
	detectedWorkflows := []*actions_module.DetectedWorkflow{
		{
			EntryName: "test.yml",
			TriggerEvent: &jobparser.Event{
				Name: "pull_request",
			},
			Content:             []byte("{ on: nothing, jobs: { j1: {} }}"),
			EventDetectionError: errors.New("nothing is not a valid event"),
		},
	}
	input := &notifyInput{
		Repo:        repo,
		Doer:        doer,
		Event:       webhook_module.HookEventPullRequestSync,
		PullRequest: pr,
		Payload:     &api.PullRequestPayload{},
	}

	err := handleWorkflows(db.DefaultContext, detectedWorkflows, commit, input, "refs/head/main")
	require.NoError(t, err)

	runs, err := db.Find[actions_model.ActionRun](db.DefaultContext, actions_model.FindRunOptions{
		RepoID: repo.ID,
	})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	createdRun := runs[0]

	assert.Equal(t, actions_model.StatusFailure, createdRun.Status)
	assert.Equal(t, "actions.workflow.event_detection_error%!(EXTRA *errors.errorString=nothing is not a valid event)", createdRun.PreExecutionError)
}
