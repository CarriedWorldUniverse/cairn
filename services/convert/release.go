// Copyright 2020 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package convert

import (
	"context"

	repo_model "github.com/CarriedWorldUniverse/cairn/models/repo"
	api "github.com/CarriedWorldUniverse/cairn/modules/structs"
)

// ToAPIRelease convert a repo_model.Release to api.Release
func ToAPIRelease(ctx context.Context, repo *repo_model.Repository, r *repo_model.Release, githubFormat bool) *api.Release {
	return &api.Release{
		ID:                   r.ID,
		TagName:              r.TagName,
		Target:               r.Target,
		Title:                r.Title,
		Note:                 r.Note,
		URL:                  r.APIURL(),
		HTMLURL:              r.HTMLURL(),
		TarURL:               r.TarURL(),
		ZipURL:               r.ZipURL(),
		HideArchiveLinks:     r.HideArchiveLinks,
		UploadURL:            r.APIUploadURL(githubFormat),
		IsDraft:              r.IsDraft,
		IsPrerelease:         r.IsPrerelease,
		CreatedAt:            r.CreatedUnix.AsTime(),
		PublishedAt:          r.CreatedUnix.AsTime(),
		Publisher:            ToUser(ctx, r.Publisher, nil),
		Attachments:          ToAPIAttachments(repo, r.Attachments),
		ArchiveDownloadCount: r.ArchiveDownloadCount,
	}
}
