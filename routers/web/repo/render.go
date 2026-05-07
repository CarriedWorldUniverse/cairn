// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package repo

import (
	"bytes"
	"io"
	"net/http"
	"path"

	"github.com/CarriedWorldUniverse/cairn/modules/charset"
	"github.com/CarriedWorldUniverse/cairn/modules/git"
	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/modules/markup"
	"github.com/CarriedWorldUniverse/cairn/modules/typesniffer"
	"github.com/CarriedWorldUniverse/cairn/modules/util"
	"github.com/CarriedWorldUniverse/cairn/services/context"
)

// RenderFile renders a file by repos path
func RenderFile(ctx *context.Context) {
	blob, err := ctx.Repo.Commit.GetBlobByPath(ctx.Repo.TreePath)
	if err != nil {
		if git.IsErrNotExist(err) {
			ctx.NotFound("GetBlobByPath", err)
		} else {
			ctx.ServerError("GetBlobByPath", err)
		}
		return
	}

	dataRc, err := blob.DataAsync()
	if err != nil {
		ctx.ServerError("DataAsync", err)
		return
	}
	defer dataRc.Close()

	buf := make([]byte, 1024)
	n, _ := util.ReadAtMost(dataRc, buf)
	buf = buf[:n]

	st := typesniffer.DetectContentType(buf, blob.Name())
	isTextFile := st.IsText()

	rd := charset.ToUTF8WithFallbackReader(io.MultiReader(bytes.NewReader(buf), dataRc), charset.ConvertOpts{})
	ctx.Resp.Header().Add("Content-Security-Policy", "frame-src 'self'; sandbox allow-scripts")

	if markupType := markup.Type(blob.Name()); markupType == "" {
		if isTextFile {
			_, _ = io.Copy(ctx.Resp, rd)
		} else {
			http.Error(ctx.Resp, "Unsupported file type render", http.StatusInternalServerError)
		}
		return
	}

	err = markup.Render(&markup.RenderContext{
		Ctx:          ctx,
		RelativePath: ctx.Repo.TreePath,
		Links: markup.Links{
			Base:       ctx.Repo.RepoLink,
			BranchPath: ctx.Repo.BranchNameSubURL(),
			TreePath:   path.Dir(ctx.Repo.TreePath),
		},
		Metas:            ctx.Repo.Repository.ComposeDocumentMetas(ctx),
		GitRepo:          ctx.Repo.GitRepo,
		InStandalonePage: true,
	}, rd, ctx.Resp)
	if err != nil {
		log.Error("Failed to render file %q: %v", ctx.Repo.TreePath, err)
		http.Error(ctx.Resp, "Failed to render file", http.StatusInternalServerError)
		return
	}
}
