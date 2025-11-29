// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"fmt"
	"html/template"
	"slices"
	"strings"
	"time"

	"forgejo.org/models/db"
	"forgejo.org/modules/container"
	"forgejo.org/modules/timeutil"
	"forgejo.org/modules/translation"
	"forgejo.org/modules/util"

	"code.forgejo.org/forgejo/runner/v12/act/jobparser"
	"go.yaml.in/yaml/v3"
	"xorm.io/builder"
)

// ActionRunJob represents a job of a run
type ActionRunJob struct {
	ID                int64
	RunID             int64      `xorm:"index"`
	Run               *ActionRun `xorm:"-"`
	RepoID            int64      `xorm:"index"`
	OwnerID           int64      `xorm:"index"`
	CommitSHA         string     `xorm:"index"`
	IsForkPullRequest bool
	Name              string `xorm:"VARCHAR(255)"`
	Attempt           int64
	WorkflowPayload   []byte
	JobID             string   `xorm:"VARCHAR(255)"` // job id in workflow, not job's id
	Needs             []string `xorm:"JSON TEXT"`
	RunsOn            []string `xorm:"JSON TEXT"`
	TaskID            int64    // the latest task of the job
	Status            Status   `xorm:"index"`
	Started           timeutil.TimeStamp
	Stopped           timeutil.TimeStamp
	Created           timeutil.TimeStamp `xorm:"created"`
	Updated           timeutil.TimeStamp `xorm:"updated index"`
}

func init() {
	db.RegisterModel(new(ActionRunJob))
}

func (job *ActionRunJob) HTMLURL(ctx context.Context) (string, error) {
	if job.Run == nil || job.Run.Repo == nil {
		return "", fmt.Errorf("action_run_job: load run and repo before accessing HTMLURL")
	}

	// Find the "index" of the currently selected job... kinda ugly that the URL uses the index rather than some other
	// unique identifier of the job which could actually be stored upon it.  But hard to change that now.
	allJobs, err := GetRunJobsByRunID(ctx, job.RunID)
	if err != nil {
		return "", err
	}
	jobIndex := -1
	for i, otherJob := range allJobs {
		if job.ID == otherJob.ID {
			jobIndex = i
			break
		}
	}
	if jobIndex == -1 {
		return "", fmt.Errorf("action_run_job: unable to find job on run: %d", job.ID)
	}

	attempt := job.Attempt
	// If a job has never been fetched by a runner yet, it will have attempt 0 -- but this attempt will never have a
	// valid UI since attempt is incremented to 1 if it is picked up by a runner.
	if attempt == 0 {
		attempt = 1
	}

	return fmt.Sprintf("%s/actions/runs/%d/jobs/%d/attempt/%d", job.Run.Repo.HTMLURL(), job.Run.Index, jobIndex, attempt), nil
}

func (job *ActionRunJob) Duration() time.Duration {
	return calculateDuration(job.Started, job.Stopped, job.Status)
}

func (job *ActionRunJob) LoadRun(ctx context.Context) error {
	if job.Run == nil {
		run, err := GetRunByID(ctx, job.RunID)
		if err != nil {
			return err
		}
		job.Run = run
	}
	return nil
}

// LoadAttributes load Run if not loaded
func (job *ActionRunJob) LoadAttributes(ctx context.Context) error {
	if job == nil {
		return nil
	}

	if err := job.LoadRun(ctx); err != nil {
		return err
	}

	return job.Run.LoadAttributes(ctx)
}

func (job *ActionRunJob) ItRunsOn(labels []string) bool {
	if len(labels) == 0 || len(job.RunsOn) == 0 {
		return false
	}
	labelSet := make(container.Set[string])
	labelSet.AddMultiple(labels...)
	return labelSet.IsSubset(job.RunsOn)
}

func GetRunJobByID(ctx context.Context, id int64) (*ActionRunJob, error) {
	var job ActionRunJob
	has, err := db.GetEngine(ctx).Where("id=?", id).Get(&job)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, fmt.Errorf("run job with id %d: %w", id, util.ErrNotExist)
	}

	return &job, nil
}

func GetRunJobsByRunID(ctx context.Context, runID int64) ([]*ActionRunJob, error) {
	var jobs []*ActionRunJob
	if err := db.GetEngine(ctx).Where("run_id=?", runID).OrderBy("id").Find(&jobs); err != nil {
		return nil, err
	}
	return jobs, nil
}

// All calls to UpdateRunJobWithoutNotification that change run.Status for any run from a not done status to a done status must call the ActionRunNowDone notification channel.
// Use the wrapper function UpdateRunJob instead.
func UpdateRunJobWithoutNotification(ctx context.Context, job *ActionRunJob, cond builder.Cond, cols ...string) (int64, error) {
	e := db.GetEngine(ctx)

	sess := e.ID(job.ID)
	if len(cols) > 0 {
		sess.Cols(cols...)
	}

	if cond != nil {
		sess.Where(cond)
	}

	affected, err := sess.Update(job)
	if err != nil {
		return 0, err
	}

	if affected == 0 || (!slices.Contains(cols, "status") && job.Status == 0) {
		return affected, nil
	}

	if affected != 0 && slices.Contains(cols, "status") && job.Status.IsWaiting() {
		// if the status of job changes to waiting again, increase tasks version.
		if err := IncreaseTaskVersion(ctx, job.OwnerID, job.RepoID); err != nil {
			return 0, err
		}
	}

	if job.RunID == 0 {
		var err error
		if job, err = GetRunJobByID(ctx, job.ID); err != nil {
			return 0, err
		}
	}

	{
		// Other goroutines may aggregate the status of the run and update it too.
		// So we need load the run and its jobs before updating the run.
		run, err := GetRunByID(ctx, job.RunID)
		if err != nil {
			return 0, err
		}
		jobs, err := GetRunJobsByRunID(ctx, job.RunID)
		if err != nil {
			return 0, err
		}
		run.Status = AggregateJobStatus(jobs)
		if run.Started.IsZero() && run.Status.IsRunning() {
			run.Started = timeutil.TimeStampNow()
		}
		if run.Stopped.IsZero() && run.Status.IsDone() {
			run.Stopped = timeutil.TimeStampNow()
		}
		// As the caller has to ensure the ActionRunNowDone notification is sent we can ignore doing so here.
		if err := UpdateRunWithoutNotification(ctx, run, "status", "started", "stopped"); err != nil {
			return 0, fmt.Errorf("update run %d: %w", run.ID, err)
		}
	}

	return affected, nil
}

func AggregateJobStatus(jobs []*ActionRunJob) Status {
	allSuccessOrSkipped := len(jobs) != 0
	allSkipped := len(jobs) != 0
	var hasFailure, hasCancelled, hasWaiting, hasRunning, hasBlocked bool
	for _, job := range jobs {
		allSuccessOrSkipped = allSuccessOrSkipped && (job.Status == StatusSuccess || job.Status == StatusSkipped)
		allSkipped = allSkipped && job.Status == StatusSkipped
		hasFailure = hasFailure || job.Status == StatusFailure
		hasCancelled = hasCancelled || job.Status == StatusCancelled
		hasWaiting = hasWaiting || job.Status == StatusWaiting
		hasRunning = hasRunning || job.Status == StatusRunning
		hasBlocked = hasBlocked || job.Status == StatusBlocked
	}
	switch {
	case allSkipped:
		return StatusSkipped
	case allSuccessOrSkipped:
		return StatusSuccess
	case hasCancelled:
		return StatusCancelled
	case hasFailure:
		return StatusFailure
	case hasRunning:
		return StatusRunning
	case hasWaiting:
		return StatusWaiting
	case hasBlocked:
		return StatusBlocked
	default:
		return StatusUnknown // it shouldn't happen
	}
}

// StatusDiagnostics returns optional diagnostic information to display to the user derived from
// ActionRunJob's current status. It should help the user understand in which state the
// ActionRunJob is and why.
func (job *ActionRunJob) StatusDiagnostics(lang translation.Locale) []template.HTML {
	diagnostics := []template.HTML{}

	switch job.Status {
	case StatusWaiting:
		joinedLabels := strings.Join(job.RunsOn, ", ")
		diagnostics = append(diagnostics, lang.TrPluralString(len(job.RunsOn), "actions.status.diagnostics.waiting", joinedLabels))
	default:
		diagnostics = append(diagnostics, template.HTML(job.Status.LocaleString(lang)))
	}

	if job.Run.NeedApproval {
		diagnostics = append(diagnostics, template.HTML(lang.TrString("actions.need_approval_desc")))
	}

	return diagnostics
}

// Checks whether the target job is an `(incomplete matrix)` job that will be blocked until the matrix is complete, and
// then regenerated and deleted.
func (job *ActionRunJob) IsIncompleteMatrix() (bool, error) {
	var jobWorkflow jobparser.SingleWorkflow
	err := yaml.Unmarshal(job.WorkflowPayload, &jobWorkflow)
	if err != nil {
		return false, fmt.Errorf("failure unmarshaling WorkflowPayload to SingleWorkflow: %w", err)
	}
	return jobWorkflow.IncompleteMatrix, nil
}
