// Copyright 2025 The Forgejo Authors. All rights reserved.
// SPDX-License-Identifier: GPL-3.0-or-later

package issues

import (
	"context"
	"strconv"

	"forgejo.org/models/moderation"
	"forgejo.org/modules/json"
	"forgejo.org/modules/timeutil"
)

// IssueData represents a trimmed down issue that is used for preserving
// only the fields needed for abusive content reports (mainly string fields).
type IssueData struct {
	RepoID         int64
	Index          int64
	PosterID       int64
	Title          string
	Content        string
	ContentVersion int
	CreatedUnix    timeutil.TimeStamp
	UpdatedUnix    timeutil.TimeStamp
}

// Implements GetFieldsMap() from ShadowCopyData interface, returning a list of <key, value> pairs
// to be used when rendering the shadow copy for admins reviewing the corresponding abuse report(s).
func (cd IssueData) GetFieldsMap() []moderation.ShadowCopyField {
	return []moderation.ShadowCopyField{
		{Key: "RepoID", Value: strconv.FormatInt(cd.RepoID, 10)},
		{Key: "Index", Value: strconv.FormatInt(cd.Index, 10)},
		{Key: "PosterID", Value: strconv.FormatInt(cd.PosterID, 10)},
		{Key: "Title", Value: cd.Title},
		{Key: "Content", Value: cd.Content},
		{Key: "ContentVersion", Value: strconv.Itoa(cd.ContentVersion)},
		{Key: "CreatedUnix", Value: cd.CreatedUnix.AsLocalTime().String()},
		{Key: "UpdatedUnix", Value: cd.UpdatedUnix.AsLocalTime().String()},
	}
}

// newIssueData creates a trimmed down issue to be used just to create a JSON structure
// (keeping only the fields relevant for moderation purposes)
func newIssueData(issue *Issue) IssueData {
	return IssueData{
		RepoID:         issue.RepoID,
		Index:          issue.Index,
		PosterID:       issue.PosterID,
		Title:          issue.Title,
		Content:        issue.Content,
		ContentVersion: issue.ContentVersion,
		CreatedUnix:    issue.CreatedUnix,
		UpdatedUnix:    issue.UpdatedUnix,
	}
}

// CommentData represents a trimmed down comment that is used for preserving
// only the fields needed for abusive content reports (mainly string fields).
type CommentData struct {
	PosterID       int64
	IssueID        int64
	Content        string
	ContentVersion int
	CreatedUnix    timeutil.TimeStamp
	UpdatedUnix    timeutil.TimeStamp
}

// Implements GetFieldsMap() from ShadowCopyData interface, returning a list of <key, value> pairs
// to be used when rendering the shadow copy for admins reviewing the corresponding abuse report(s).
func (cd CommentData) GetFieldsMap() []moderation.ShadowCopyField {
	return []moderation.ShadowCopyField{
		{Key: "PosterID", Value: strconv.FormatInt(cd.PosterID, 10)},
		{Key: "IssueID", Value: strconv.FormatInt(cd.IssueID, 10)},
		{Key: "Content", Value: cd.Content},
		{Key: "ContentVersion", Value: strconv.Itoa(cd.ContentVersion)},
		{Key: "CreatedUnix", Value: cd.CreatedUnix.AsLocalTime().String()},
		{Key: "UpdatedUnix", Value: cd.UpdatedUnix.AsLocalTime().String()},
	}
}

// newCommentData creates a trimmed down comment to be used just to create a JSON structure
// (keeping only the fields relevant for moderation purposes)
func newCommentData(comment *Comment) CommentData {
	return CommentData{
		PosterID:       comment.PosterID,
		IssueID:        comment.IssueID,
		Content:        comment.Content,
		ContentVersion: comment.ContentVersion,
		CreatedUnix:    comment.CreatedUnix,
		UpdatedUnix:    comment.UpdatedUnix,
	}
}

// IfNeededCreateShadowCopyForIssue checks if for the given issue there are any reports of abusive content submitted
// and if found a shadow copy of relevant issue fields will be stored into DB and linked to the above report(s).
// This function should be called before a issue is deleted or updated.
func IfNeededCreateShadowCopyForIssue(ctx context.Context, issue *Issue) error {
	shadowCopyNeeded, err := moderation.IsShadowCopyNeeded(ctx, moderation.ReportedContentTypeIssue, issue.ID)
	if err != nil {
		return err
	}

	if shadowCopyNeeded {
		issueData := newIssueData(issue)
		content, err := json.Marshal(issueData)
		if err != nil {
			return err
		}
		return moderation.CreateShadowCopyForIssue(ctx, issue.ID, string(content))
	}

	return nil
}

// IfNeededCreateShadowCopyForComment checks if for the given comment there are any reports of abusive content submitted
// and if found a shadow copy of relevant comment fields will be stored into DB and linked to the above report(s).
// This function should be called before a comment is deleted or updated.
func IfNeededCreateShadowCopyForComment(ctx context.Context, comment *Comment, forUpdates bool) error {
	shadowCopyNeeded, err := moderation.IsShadowCopyNeeded(ctx, moderation.ReportedContentTypeComment, comment.ID)
	if err != nil {
		return err
	}

	if shadowCopyNeeded {
		if forUpdates {
			// get the unaltered comment fields (for updates the provided variable is already altered but not yet saved)
			if comment, err = GetCommentByID(ctx, comment.ID); err != nil {
				return err
			}
		}
		commentData := newCommentData(comment)
		content, err := json.Marshal(commentData)
		if err != nil {
			return err
		}
		return moderation.CreateShadowCopyForComment(ctx, comment.ID, string(content))
	}

	return nil
}
