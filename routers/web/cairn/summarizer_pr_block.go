// Package cairn — Cairn web UI augmentations.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/modules/log"
	"github.com/CarriedWorldUniverse/cairn/modules/markup"
	"github.com/CarriedWorldUniverse/cairn/modules/markup/markdown"
	"github.com/CarriedWorldUniverse/cairn/services/cairn/summarizer"
)

//go:embed templates/summarizer/*.tmpl
var summarizerTemplates embed.FS

var prSummaryBlockTemplate = template.Must(
	template.New("pr-summary-block.tmpl").
		Funcs(template.FuncMap{"safeHTML": func(s string) template.HTML { return template.HTML(s) }}).
		ParseFS(summarizerTemplates, "templates/summarizer/pr-summary-block.tmpl"),
)

type prSummaryView struct {
	Available     bool
	State         string
	ModelID       string
	GeneratedAt   string
	SummaryHTML   string
	RegenerateURL string
}

// RenderPRSummaryBlock returns the HTML for the per-PR Cairn summary block,
// or "" if the simplifier is not enabled. canRegenerate gates the
// regenerate button; regenURL is the API endpoint clients POST to.
func RenderPRSummaryBlock(repoID, prNumber int64, canRegenerate bool, regenURL string) string {
	svc := summarizer.Global()
	if svc == nil {
		return ""
	}
	view := prSummaryView{Available: true}

	row, err := svc.GetCachedSummary(context.Background(), repoID, prNumber)
	switch {
	case errors.Is(err, summarizer.ErrNoSummary):
		view.State = "generating"
	case err != nil:
		log.Warn("cairn: PR summary lookup repo=%d pr=%d: %v", repoID, prNumber, err)
		view.State = "unavailable"
	default:
		view.State = "ready"
		view.ModelID = row.ModelID
		view.GeneratedAt = time.Unix(row.GeneratedUnix, 0).UTC().Format("2006-01-02 15:04 UTC")
		html, rerr := markdown.RenderRawString(&markup.RenderContext{Ctx: context.Background()}, row.SummaryMD)
		if rerr != nil {
			log.Warn("cairn: PR summary markdown render repo=%d pr=%d: %v", repoID, prNumber, rerr)
			view.State = "unavailable"
		} else {
			view.SummaryHTML = html
		}
	}

	if canRegenerate && view.State == "ready" {
		view.RegenerateURL = regenURL
	}

	var buf strings.Builder
	if err := prSummaryBlockTemplate.Execute(&buf, view); err != nil {
		log.Warn("cairn: PR summary template render: %v", err)
		return ""
	}
	return buf.String()
}
