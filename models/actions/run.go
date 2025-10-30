// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"forgejo.org/models/db"
	repo_model "forgejo.org/models/repo"
	user_model "forgejo.org/models/user"
	"forgejo.org/modules/cache"
	"forgejo.org/modules/git"
	"forgejo.org/modules/json"
	"forgejo.org/modules/log"
	api "forgejo.org/modules/structs"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/util"
	webhook_module "forgejo.org/modules/webhook"

	"code.forgejo.org/forgejo/runner/v11/act/jobparser"
	"xorm.io/builder"
)

type ConcurrencyMode int

const (
	// Don't enforce concurrency control.  Note that you won't find `UnlimitedConcurrency` implemented directly in the
	// code; setting it on an `ActionRun` prevents the other limiting behaviors.
	UnlimitedConcurrency ConcurrencyMode = iota
	// Queue behind other jobs with the same concurrency group
	QueueBehind
	// Cancel other jobs with the same concurrency group
	CancelInProgress
)

// ActionRun represents a run of a workflow file
type ActionRun struct {
	ID            int64
	Title         string
	RepoID        int64                  `xorm:"index unique(repo_index) index(concurrency)"`
	Repo          *repo_model.Repository `xorm:"-"`
	OwnerID       int64                  `xorm:"index"`
	WorkflowID    string                 `xorm:"index"`                    // the name of workflow file
	Index         int64                  `xorm:"index unique(repo_index)"` // a unique number for each run of a repository
	TriggerUserID int64                  `xorm:"index"`
	TriggerUser   *user_model.User       `xorm:"-"`
	ScheduleID    int64
	Ref           string `xorm:"index"` // the commit/tag/… that caused the run
	IsRefDeleted  bool   `xorm:"-"`
	CommitSHA     string
	Event         webhook_module.HookEventType // the webhook event that causes the workflow to run
	EventPayload  string                       `xorm:"LONGTEXT"`
	TriggerEvent  string                       // the trigger event defined in the `on` configuration of the triggered workflow
	Status        Status                       `xorm:"index"`
	Version       int                          `xorm:"version default 0"` // Status could be updated concomitantly, so an optimistic lock is needed
	// Started and Stopped is used for recording last run time, if rerun happened, they will be reset to 0
	Started timeutil.TimeStamp
	Stopped timeutil.TimeStamp
	// PreviousDuration is used for recording previous duration
	PreviousDuration time.Duration
	Created          timeutil.TimeStamp `xorm:"created"`
	Updated          timeutil.TimeStamp `xorm:"updated"`
	NotifyEmail      bool

	// pull request trust
	IsForkPullRequest   bool
	PullRequestPosterID int64
	PullRequestID       int64 `xorm:"index"`
	NeedApproval        bool
	ApprovedBy          int64 `xorm:"index"`

	ConcurrencyGroup string `xorm:"'concurrency_group' index(concurrency)"`
	ConcurrencyType  ConcurrencyMode

	PreExecutionError string `xorm:"LONGTEXT"` // used to report errors that blocked execution of a workflow
}

func init() {
	db.RegisterModel(new(ActionRun))
	db.RegisterModel(new(ActionRunIndex))
}

func (run *ActionRun) HTMLURL() string {
	if run.Repo == nil {
		return ""
	}
	return fmt.Sprintf("%s/actions/runs/%d", run.Repo.HTMLURL(), run.Index)
}

func (run *ActionRun) Link() string {
	if run.Repo == nil {
		return ""
	}
	return fmt.Sprintf("%s/actions/runs/%d", run.Repo.Link(), run.Index)
}

// RefLink return the url of run's ref
func (run *ActionRun) RefLink() string {
	refName := git.RefName(run.Ref)
	if refName.IsPull() {
		return run.Repo.Link() + "/pulls/" + refName.ShortName()
	}
	return git.RefURL(run.Repo.Link(), run.Ref)
}

// PrettyRef return #id for pull ref or ShortName for others
func (run *ActionRun) PrettyRef() string {
	refName := git.RefName(run.Ref)
	if refName.IsPull() {
		return "#" + strings.TrimSuffix(strings.TrimPrefix(run.Ref, git.PullPrefix), "/head")
	}
	return refName.ShortName()
}

// LoadAttributes load Repo TriggerUser if not loaded
func (run *ActionRun) LoadAttributes(ctx context.Context) error {
	if run == nil {
		return nil
	}

	if err := run.LoadRepo(ctx); err != nil {
		return err
	}

	if err := run.Repo.LoadAttributes(ctx); err != nil {
		return err
	}

	if run.TriggerUser == nil {
		u, err := user_model.GetPossibleUserByID(ctx, run.TriggerUserID)
		if err != nil {
			return err
		}
		run.TriggerUser = u
	}

	return nil
}

func (run *ActionRun) LoadRepo(ctx context.Context) error {
	if run == nil || run.Repo != nil {
		return nil
	}

	repo, err := repo_model.GetRepositoryByID(ctx, run.RepoID)
	if err != nil {
		return err
	}
	run.Repo = repo
	return nil
}

func (run *ActionRun) Duration() time.Duration {
	return calculateDuration(run.Started, run.Stopped, run.Status) + run.PreviousDuration
}

func (run *ActionRun) GetPushEventPayload() (*api.PushPayload, error) {
	if run.Event == webhook_module.HookEventPush {
		var payload api.PushPayload
		if err := json.Unmarshal([]byte(run.EventPayload), &payload); err != nil {
			return nil, err
		}
		return &payload, nil
	}
	return nil, fmt.Errorf("event %s is not a push event", run.Event)
}

func (run *ActionRun) GetPullRequestEventPayload() (*api.PullRequestPayload, error) {
	if run.Event == webhook_module.HookEventPullRequest ||
		run.Event == webhook_module.HookEventPullRequestSync ||
		run.Event == webhook_module.HookEventPullRequestAssign ||
		run.Event == webhook_module.HookEventPullRequestMilestone ||
		run.Event == webhook_module.HookEventPullRequestLabel {
		var payload api.PullRequestPayload
		if err := json.Unmarshal([]byte(run.EventPayload), &payload); err != nil {
			return nil, err
		}
		return &payload, nil
	}
	return nil, fmt.Errorf("event %s is not a pull request event", run.Event)
}

func (run *ActionRun) SetConcurrencyGroup(concurrencyGroup string) {
	// Concurrency groups are case insensitive identifiers, implemented by collapsing case here.  Unfortunately the
	// `ConcurrencyGroup` field can't be made a private field because xorm doesn't map those fields -- using
	// `SetConcurrencyGroup` is required for consistency but not enforced at compile-time.
	run.ConcurrencyGroup = strings.ToLower(concurrencyGroup)
}

func (run *ActionRun) SetDefaultConcurrencyGroup() {
	// Before ConcurrencyGroups were supported, Forgejo would automatically cancel runs with matching git refs, workflow
	// IDs, and trigger events.  For backwards compatibility we emulate that behavior:
	run.SetConcurrencyGroup(fmt.Sprintf(
		"%s_%s_%s__auto",
		run.Ref,
		run.WorkflowID,
		run.TriggerEvent,
	))
}

func actionsCountOpenCacheKey(repoID int64) string {
	return fmt.Sprintf("Actions:CountOpenActionRuns:%d", repoID)
}

func RepoNumOpenActions(ctx context.Context, repoID int64) int {
	num, err := cache.GetInt(actionsCountOpenCacheKey(repoID), func() (int, error) {
		count, err := db.GetEngine(ctx).
			Table("action_run").
			Where(
				builder.Eq{"repo_id": repoID}.And(
					builder.In("status", PendingStatuses()))).
			Count()
		if err != nil {
			return 0, fmt.Errorf("query error: %v", err)
		}
		return int(count), nil
	})
	if err != nil {
		log.Error("failed to retrieve NumIssues: %v", err)
		return 0
	}
	return num
}

func clearRepoRunCountCache(repo *repo_model.Repository) {
	cache.Remove(actionsCountOpenCacheKey(repo.ID))
}

func condRunsThatNeedApproval(repoID, pullRequestID int64) builder.Cond {
	// performance relies indexes on repo_id and pull_request_id
	return builder.Eq{"repo_id": repoID, "pull_request_id": pullRequestID, "need_approval": true}
}

func GetRunsThatNeedApprovalByRepoIDAndPullRequestID(ctx context.Context, repoID, pullRequestID int64) ([]*ActionRun, error) {
	var runs []*ActionRun
	if err := db.GetEngine(ctx).Where(condRunsThatNeedApproval(repoID, pullRequestID)).Find(&runs); err != nil {
		return nil, err
	}
	return runs, nil
}

func HasRunThatNeedApproval(ctx context.Context, repoID, pullRequestID int64) (bool, error) {
	return db.GetEngine(ctx).Where(condRunsThatNeedApproval(repoID, pullRequestID)).Exist(&ActionRun{})
}

type ApprovalType bool

const (
	NeedApproval        = ApprovalType(true)
	DoesNotNeedApproval = ApprovalType(false)
	UndefinedApproval   = ApprovalType(false)
)

func UpdateRunApprovalByID(ctx context.Context, id int64, approval ApprovalType, approvedBy int64) error {
	_, err := db.GetEngine(ctx).Exec("UPDATE action_run SET need_approval=?, approved_by=? WHERE id=?", bool(approval), approvedBy, id)
	return err
}

func GetRunsNotDoneByRepoIDAndPullRequestPosterID(ctx context.Context, repoID, pullRequestPosterID int64) ([]*ActionRun, error) {
	var runs []*ActionRun
	// performance relies on indexes on repo_id and status
	if err := db.GetEngine(ctx).Where("repo_id=? AND pull_request_poster_id=?", repoID, pullRequestPosterID).And(builder.In("status", []Status{StatusUnknown, StatusWaiting, StatusRunning, StatusBlocked})).Find(&runs); err != nil {
		return nil, err
	}
	return runs, nil
}

func GetRunsNotDoneByRepoIDAndPullRequestID(ctx context.Context, repoID, pullRequestID int64) ([]*ActionRun, error) {
	var runs []*ActionRun
	// performance relies on indexes on repo_id and status
	if err := db.GetEngine(ctx).Where("repo_id=? AND pull_request_id=?", repoID, pullRequestID).And(builder.In("status", []Status{StatusUnknown, StatusWaiting, StatusRunning, StatusBlocked})).Find(&runs); err != nil {
		return nil, err
	}
	return runs, nil
}

// InsertRun inserts a run
// The title will be cut off at 255 characters if it's longer than 255 characters.
// We don't have to send the ActionRunNowDone notification here because there are no runs that start in a not done status.
func InsertRun(ctx context.Context, run *ActionRun, jobs []*jobparser.SingleWorkflow) error {
	ctx, commiter, err := db.TxContext(ctx)
	if err != nil {
		return err
	}
	defer commiter.Close()

	index, err := db.GetNextResourceIndex(ctx, "action_run_index", run.RepoID)
	if err != nil {
		return err
	}
	run.Index = index
	run.Title, _ = util.SplitStringAtByteN(run.Title, 255)

	if err := db.Insert(ctx, run); err != nil {
		return err
	}

	if run.Repo == nil {
		repo, err := repo_model.GetRepositoryByID(ctx, run.RepoID)
		if err != nil {
			return err
		}
		run.Repo = repo
	}

	clearRepoRunCountCache(run.Repo)

	runJobs := make([]*ActionRunJob, 0, len(jobs))
	var hasWaiting bool
	for _, v := range jobs {
		id, job := v.Job()
		status := StatusFailure
		payload := []byte{}
		needs := []string{}
		name := run.Title
		runsOn := []string{}
		if job != nil {
			needs = job.Needs()
			if err := v.SetJob(id, job.EraseNeeds()); err != nil {
				return err
			}
			payload, _ = v.Marshal()

			if len(needs) > 0 || run.NeedApproval {
				status = StatusBlocked
			} else {
				status = StatusWaiting
				hasWaiting = true
			}
			name, _ = util.SplitStringAtByteN(job.Name, 255)
			runsOn = job.RunsOn()
		}
		runJobs = append(runJobs, &ActionRunJob{
			RunID:             run.ID,
			RepoID:            run.RepoID,
			OwnerID:           run.OwnerID,
			CommitSHA:         run.CommitSHA,
			IsForkPullRequest: run.IsForkPullRequest,
			Name:              name,
			WorkflowPayload:   payload,
			JobID:             id,
			Needs:             needs,
			RunsOn:            runsOn,
			Status:            status,
		})
	}
	if err := db.Insert(ctx, runJobs); err != nil {
		return err
	}

	// if there is a job in the waiting status, increase tasks version.
	if hasWaiting {
		if err := IncreaseTaskVersion(ctx, run.OwnerID, run.RepoID); err != nil {
			return err
		}
	}

	return commiter.Commit()
}

func GetLatestRun(ctx context.Context, repoID int64) (*ActionRun, error) {
	var run ActionRun
	has, err := db.GetEngine(ctx).Where("repo_id=?", repoID).OrderBy("id DESC").Limit(1).Get(&run)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, fmt.Errorf("latest run: %w", util.ErrNotExist)
	}
	return &run, nil
}

func GetRunBefore(ctx context.Context, _ *ActionRun) (*ActionRun, error) {
	// TODO return the most recent run related to the run given in argument
	// see https://codeberg.org/forgejo/user-research/issues/63 for context
	return nil, nil
}

func GetLatestRunForBranchAndWorkflow(ctx context.Context, repoID int64, branch, workflowFile, event string) (*ActionRun, error) {
	var run ActionRun
	q := db.GetEngine(ctx).Where("repo_id=?", repoID).And("workflow_id=?", workflowFile)
	if event != "" {
		q = q.And("event=?", event)
	}
	if branch != "" {
		q = q.And("ref=?", branch)
	}
	has, err := q.Desc("id").Get(&run)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, util.NewNotExistErrorf("run with repo_id %d, ref %s, event %s, workflow_id %s", repoID, branch, event, workflowFile)
	}
	return &run, nil
}

func GetRunByID(ctx context.Context, id int64) (*ActionRun, error) {
	run, has, err := GetRunByIDWithHas(ctx, id)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, fmt.Errorf("run with id %d: %w", id, util.ErrNotExist)
	}

	return run, nil
}

func GetRunByIDWithHas(ctx context.Context, id int64) (*ActionRun, bool, error) {
	var run ActionRun
	has, err := db.GetEngine(ctx).Where("id=?", id).Get(&run)
	if err != nil {
		return nil, false, err
	} else if !has {
		return nil, false, nil
	}

	return &run, true, nil
}

func GetRunByIndex(ctx context.Context, repoID, index int64) (*ActionRun, error) {
	run := &ActionRun{
		RepoID: repoID,
		Index:  index,
	}
	has, err := db.GetEngine(ctx).Get(run)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, fmt.Errorf("run with index %d %d: %w", repoID, index, util.ErrNotExist)
	}

	return run, nil
}

// UpdateRun updates a run.
// It requires the inputted run has Version set.
// It will return error if the version is not matched (it means the run has been changed after loaded).
// All calls to UpdateRunWithoutNotification that change run.Status from a not done status to a done status must call the ActionRunNowDone notification channel.
// Use the wrapper function UpdateRun instead.
func UpdateRunWithoutNotification(ctx context.Context, run *ActionRun, cols ...string) error {
	sess := db.GetEngine(ctx).ID(run.ID)
	if len(cols) > 0 {
		sess.Cols(cols...)
	}
	run.Title, _ = util.SplitStringAtByteN(run.Title, 255)
	affected, err := sess.Update(run)
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("run has changed")
		// It's impossible that the run is not found, since Gitea never deletes runs.
	}

	if run.Status != 0 || slices.Contains(cols, "status") {
		if run.RepoID == 0 {
			run, err = GetRunByID(ctx, run.ID)
			if err != nil {
				return err
			}
		}
		if run.Repo == nil {
			repo, err := repo_model.GetRepositoryByID(ctx, run.RepoID)
			if err != nil {
				return err
			}
			run.Repo = repo
		}
		clearRepoRunCountCache(run.Repo)
	}

	return nil
}

type ActionRunIndex db.ResourceIndex
