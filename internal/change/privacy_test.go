package change

import "testing"

func TestMatchPrivacy(t *testing.T) {
	flags := []PrivateEntry{
		{Path: "secrets/prod.env", Mode: PrivacyOmit},
		{Path: "config", Mode: PrivacyShapeOnly},
		{Path: "config/keys", Mode: PrivacyOmit},
	}
	cases := []struct {
		path    string
		wantOK  bool
		wantMod string
		why     string
	}{
		{"secrets/prod.env", true, PrivacyOmit, "exact path match"},
		{"secrets/prod.env/x", true, PrivacyOmit, "under an exact-path flag (subtree)"},
		{"secrets/other.env", false, "", "sibling of a flagged path is not withheld"},
		{"secrets", false, "", "parent of a flagged path is not itself withheld"},
		{"config/app.yaml", true, PrivacyShapeOnly, "under config/ flag"},
		{"config", true, PrivacyShapeOnly, "the flagged path itself"},
		{"config/keys/id_rsa", true, PrivacyOmit, "longest (most specific) flag wins"},
		{"config/keys", true, PrivacyOmit, "the nested flag path itself"},
		{"configuration/x", false, "", "config is not a path-prefix of configuration (/ boundary)"},
		{"readme.md", false, "", "unflagged path"},
	}
	for _, c := range cases {
		mode, ok := matchPrivacy(flags, c.path)
		if ok != c.wantOK || (ok && mode != c.wantMod) {
			t.Errorf("matchPrivacy(%q) = (%q,%v), want (%q,%v) — %s", c.path, mode, ok, c.wantMod, c.wantOK, c.why)
		}
	}
}

func TestPrivacyCRUDRoundTrip(t *testing.T) {
	e := newTestEngine(t)
	if has, _ := e.HasPrivate(); has {
		t.Fatal("fresh repo should have no privacy flags")
	}
	if err := e.MarkPrivate("secrets/prod.env", PrivacyOmit); err != nil {
		t.Fatal(err)
	}
	if err := e.MarkPrivate("config", PrivacyShapeOnly); err != nil {
		t.Fatal(err)
	}
	if has, _ := e.HasPrivate(); !has {
		t.Fatal("HasPrivate should be true after marking")
	}
	got, err := e.ListPrivate()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ListPrivate len = %d, want 2", len(got))
	}
	// Re-marking updates the mode (ON CONFLICT), not a duplicate row.
	if err := e.MarkPrivate("config", PrivacyOmit); err != nil {
		t.Fatal(err)
	}
	got, _ = e.ListPrivate()
	if len(got) != 2 {
		t.Fatalf("re-mark created a duplicate row: len = %d, want 2", len(got))
	}
	if mode, ok := e.PrivacyMatch("config/x"); !ok || mode != PrivacyOmit {
		t.Fatalf("after re-mark, config/x = (%q,%v), want (omit,true)", mode, ok)
	}
	// Unmark is idempotent.
	if err := e.UnmarkPrivate("config"); err != nil {
		t.Fatal(err)
	}
	if err := e.UnmarkPrivate("config"); err != nil {
		t.Fatal("second unmark should be a no-op, got error:", err)
	}
	got, _ = e.ListPrivate()
	if len(got) != 1 {
		t.Fatalf("after unmark, len = %d, want 1", len(got))
	}
}

func TestMarkPrivateRejectsBadInput(t *testing.T) {
	e := newTestEngine(t)
	if err := e.MarkPrivate("x", "bogus"); err == nil {
		t.Error("bad mode should error")
	}
	if err := e.MarkPrivate("", PrivacyOmit); err == nil {
		t.Error("empty path should error")
	}
}
