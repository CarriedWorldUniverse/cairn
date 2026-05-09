// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

import (
	"strings"
	"testing"

	cairnmodels "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
	"github.com/CarriedWorldUniverse/cairn/services/cairn/summarizer"
)

func TestRenderPRSummaryBlock_NilService(t *testing.T) {
	summarizer.SetGlobal(nil)
	out := RenderPRSummaryBlock(1, 1, false, "")
	if out != "" {
		t.Fatalf("expected empty string when service nil, got %q", out)
	}
}

func TestRenderPRSummaryBlock_GeneratingState(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := summarizer.NewService(eng, nil)
	summarizer.SetGlobal(svc)
	t.Cleanup(func() { summarizer.SetGlobal(nil) })

	out := RenderPRSummaryBlock(42, 7, false, "")
	if !strings.Contains(out, "summary generating") {
		t.Fatalf("expected generating placeholder, got %q", out)
	}
	if !strings.Contains(out, "by cairn") {
		t.Fatalf("expected 'by cairn' header, got %q", out)
	}
}

func TestRenderPRSummaryBlock_ReadyState(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := summarizer.NewService(eng, nil)
	summarizer.SetGlobal(svc)
	t.Cleanup(func() { summarizer.SetGlobal(nil) })

	row := &cairnmodels.PRSummary{
		RepoID:      42,
		PRNumber:    7,
		ContentHash: "abc",
		SummaryMD:   "# test summary\n\nbody text",
		ModelID:     "test-model",
	}
	if _, err := eng.Insert(row); err != nil {
		t.Fatalf("insert: %v", err)
	}

	out := RenderPRSummaryBlock(42, 7, false, "/regen-url")
	if !strings.Contains(out, "test summary") {
		t.Fatalf("expected rendered markdown body, got %q", out)
	}
	if !strings.Contains(out, "test-model") {
		t.Fatalf("expected ModelID, got %q", out)
	}
	if !strings.Contains(out, "by cairn") {
		t.Fatalf("expected 'by cairn' header, got %q", out)
	}
}

func TestRenderPRSummaryBlock_RegenerateURLOnlyForAdmin(t *testing.T) {
	eng := cairntest.NewEngine(t)
	svc := summarizer.NewService(eng, nil)
	summarizer.SetGlobal(svc)
	t.Cleanup(func() { summarizer.SetGlobal(nil) })

	row := &cairnmodels.PRSummary{
		RepoID: 42, PRNumber: 7, ContentHash: "abc",
		SummaryMD: "summary", ModelID: "m",
	}
	if _, err := eng.Insert(row); err != nil {
		t.Fatalf("insert: %v", err)
	}

	noBtn := RenderPRSummaryBlock(42, 7, false, "/regen-url")
	if strings.Contains(noBtn, "data-url") {
		t.Fatalf("non-admin should not see regenerate button: %q", noBtn)
	}
	if strings.Contains(noBtn, "cairn-summary-regen") {
		t.Fatalf("non-admin should not see regenerate button class: %q", noBtn)
	}

	withBtn := RenderPRSummaryBlock(42, 7, true, "/regen-url")
	if !strings.Contains(withBtn, `data-url="/regen-url"`) {
		t.Fatalf("admin should see regenerate button with URL: %q", withBtn)
	}
	if !strings.Contains(withBtn, "cairn-summary-regen") {
		t.Fatalf("admin should see regenerate button class: %q", withBtn)
	}
}
