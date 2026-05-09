// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package incoming

import (
	"bytes"
	"context"
	"fmt"

	issues_model "github.com/CarriedWorldUniverse/cairn/models/issues"
	access_model "github.com/CarriedWorldUniverse/cairn/models/perm/access"
	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	user_model "github.com/CarriedWorldUniverse/cairn/models/user"
	"github.com/CarriedWorldUniverse/cairn/modules/gitrepo"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/modules/setting"
	"github.com/CarriedWorldUniverse/cairn/modules/util"
	attachment_service "github.com/CarriedWorldUniverse/cairn/services/attachment"
	"github.com/CarriedWorldUniverse/cairn/services/context/upload"
	issue_service "github.com/CarriedWorldUniverse/cairn/services/issue"
	incoming_payload "github.com/CarriedWorldUniverse/cairn/services/mailer/incoming/payload"
	"github.com/CarriedWorldUniverse/cairn/services/mailer/token"
	pull_service "github.com/CarriedWorldUniverse/cairn/services/pull"
)

type MailHandler interface {
	Handle(ctx context.Context, content *MailContent, doer *user_model.User, payload []byte) error
}

var handlers = map[token.HandlerType]MailHandler{
	token.ReplyHandlerType:       &ReplyHandler{},
	token.UnsubscribeHandlerType: &UnsubscribeHandler{},
}

// ReplyHandler handles incoming emails to create a reply from them
type ReplyHandler struct{}

func (h *ReplyHandler) Handle(ctx context.Context, content *MailContent, doer *user_model.User, payload []byte) error {
	if doer == nil {
		return util.NewInvalidArgumentErrorf("doer can't be nil")
	}

	ref, err := incoming_payload.GetReferenceFromPayload(ctx, payload)
	if err != nil {
		return err
	}

	var issue *issues_model.Issue

	switch r := ref.(type) {
	case *issues_model.Issue:
		issue = r
	case *issues_model.Comment:
		comment := r

		if err := comment.LoadIssue(ctx); err != nil {
			return err
		}

		issue = comment.Issue
	default:
		return util.NewInvalidArgumentErrorf("unsupported reply reference: %v", ref)
	}

	if err := issue.LoadRepo(ctx); err != nil {
		return err
	}

	perm, err := access_model.GetUserRepoPermission(ctx, issue.Repo, doer)
	if err != nil {
		return err
	}

	// Locked issues require write permissions
	if issue.IsLocked && !perm.CanWriteIssuesOrPulls(issue.IsPull) && !doer.IsAdmin {
		log.Debug("can't write issue or pull")
		return nil
	}

	if !perm.CanReadIssuesOrPulls(issue.IsPull) {
		log.Debug("can't read issue or pull")
		return nil
	}

	log.Trace("incoming mail related to %T", ref)

	attachmentIDs := make([]string, 0, len(content.Attachments))
	if setting.Attachment.Enabled {
		for _, attachment := range content.Attachments {
			a, err := attachment_service.UploadAttachment(ctx, bytes.NewReader(attachment.Content), setting.Attachment.AllowedTypes, int64(len(attachment.Content)), &repo_model.Attachment{
				Name:       attachment.Name,
				UploaderID: doer.ID,
				RepoID:     issue.Repo.ID,
			})
			if err != nil {
				if upload.IsErrFileTypeForbidden(err) {
					log.Info("Skipping disallowed attachment type: %s", attachment.Name)
					continue
				}
				return err
			}
			attachmentIDs = append(attachmentIDs, a.UUID)
		}
	}

	if content.Content == "" && len(attachmentIDs) == 0 {
		log.Trace("incoming mail has no content and no attachment", ref)
		return nil
	}

	switch r := ref.(type) {
	case *issues_model.Issue:
		_, err = issue_service.CreateIssueComment(ctx, doer, issue.Repo, issue, content.Content, attachmentIDs)
		if err != nil {
			return fmt.Errorf("CreateIssueComment failed: %w", err)
		}
	case *issues_model.Comment:
		comment := r

		switch comment.Type {
		case issues_model.CommentTypeComment, issues_model.CommentTypeReview:
			_, err = issue_service.CreateIssueComment(ctx, doer, issue.Repo, issue, content.Content, attachmentIDs)
			if err != nil {
				return fmt.Errorf("CreateIssueComment failed: %w", err)
			}
		case issues_model.CommentTypeCode:
			err := issue.LoadRepo(ctx)
			if err != nil {
				return fmt.Errorf("LoadRepo failed: %w", err)
			}
			err = issue.LoadPullRequest(ctx)
			if err != nil {
				return fmt.Errorf("LoadPullRequest failed: %w", err)
			}
			repo, err := gitrepo.OpenRepository(ctx, issue.Repo)
			defer repo.Close()
			if err != nil {
				return fmt.Errorf("OpenRepository failed: %w", err)
			}
			afterCommitID, err := repo.GetRefCommitID(issue.PullRequest.GetGitRefName())
			if err != nil {
				return fmt.Errorf("GetRefCommitID failed: %w", err)
			}
			_, err = pull_service.CreateCodeComment(
				ctx,
				doer,
				nil,
				issue,
				comment.Line,
				content.Content,
				comment.TreePath,
				false, // not pending review but a single review
				comment.ReviewID,
				issue.PullRequest.MergeBase,
				afterCommitID,
				attachmentIDs,
			)
			if err != nil {
				return fmt.Errorf("CreateCodeComment failed: %w", err)
			}
		default:
			log.Trace("incoming mail related to comment of type %v is ignored", comment.Type)
		}
	default:
		log.Trace("incoming mail related to %T is ignored", ref)
	}
	return nil
}

// UnsubscribeHandler handles unwatching issues/pulls
type UnsubscribeHandler struct{}

func (h *UnsubscribeHandler) Handle(ctx context.Context, _ *MailContent, doer *user_model.User, payload []byte) error {
	if doer == nil {
		return util.NewInvalidArgumentErrorf("doer can't be nil")
	}

	ref, err := incoming_payload.GetReferenceFromPayload(ctx, payload)
	if err != nil {
		return err
	}

	switch r := ref.(type) {
	case *issues_model.Issue:
		issue := r

		if err := issue.LoadRepo(ctx); err != nil {
			return err
		}

		perm, err := access_model.GetUserRepoPermission(ctx, issue.Repo, doer)
		if err != nil {
			return err
		}

		if !perm.CanReadIssuesOrPulls(issue.IsPull) {
			log.Debug("can't read issue or pull")
			return nil
		}

		return issues_model.CreateOrUpdateIssueWatch(ctx, doer.ID, issue.ID, false)
	default:
		return fmt.Errorf("unsupported unsubscribe reference: %v", ref)
	}
}
