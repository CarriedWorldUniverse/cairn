package change

import "testing"

// seedLine commits c1→c2→c3 on main and returns their sealed shas.
func seedLine(t *testing.T, e *Engine) (c1, c2, c3 string) {
	t.Helper()
	e.SetIdentity("Dev", "dev@x.io")
	main, _ := e.LineByName("main")
	mk := func(msg, body string) string {
		ch, err := e.CreateChange(main.ID, "dev")
		if err != nil {
			t.Fatal(err)
		}
		r, err := e.Commit(ch.ID, map[string][]byte{"a.txt": []byte(body)}, nil, msg)
		if err != nil {
			t.Fatal(err)
		}
		return r.HeadCommit
	}
	return mk("c1", "1\n"), mk("c2", "2\n"), mk("c3", "3\n")
}

func TestEmbargoPublicTip(t *testing.T) {
	e := newTestEngine(t)
	c1, c2, c3 := seedLine(t, e)

	// No embargo → public tip is the real tip.
	if pt, err := e.PublicTip(c3); err != nil || pt != c3 {
		t.Fatalf("no-embargo PublicTip = (%s,%v), want %s", pt, err, c3)
	}
	// Embargo c2 → everything at/after c2 is held; public tip = c1.
	if err := e.MarkEmbargo(c2); err != nil {
		t.Fatal(err)
	}
	if pt, _ := e.PublicTip(c3); pt != c1 {
		t.Fatalf("embargo c2: PublicTip(c3) = %s, want c1 %s", pt, c1)
	}
	// Embargo the root commit too → nothing public.
	if err := e.MarkEmbargo(c1); err != nil {
		t.Fatal(err)
	}
	if pt, _ := e.PublicTip(c3); pt != "" {
		t.Fatalf("root embargoed: PublicTip(c3) = %s, want empty", pt)
	}
	// Disclose c2 — but c1 still embargoed → still nothing public.
	if err := e.UnmarkEmbargo(c2); err != nil {
		t.Fatal(err)
	}
	if pt, _ := e.PublicTip(c3); pt != "" {
		t.Fatalf("c1 still embargoed: PublicTip(c3) = %s, want empty", pt)
	}
	// Disclose c1 → fully public.
	if err := e.UnmarkEmbargo(c1); err != nil {
		t.Fatal(err)
	}
	if pt, _ := e.PublicTip(c3); pt != c3 {
		t.Fatalf("all disclosed: PublicTip(c3) = %s, want c3", pt)
	}
}

func TestEmbargoCRUD(t *testing.T) {
	e := newTestEngine(t)
	c1, c2, _ := seedLine(t, e)
	if has, _ := e.HasEmbargo(); has {
		t.Fatal("fresh repo has embargo")
	}
	if err := e.MarkEmbargo(c2); err != nil {
		t.Fatal(err)
	}
	if err := e.MarkEmbargo(c2); err != nil { // idempotent
		t.Fatal(err)
	}
	if has, _ := e.HasEmbargo(); !has {
		t.Fatal("HasEmbargo false after mark")
	}
	if emb, _ := e.IsEmbargoed(c2); !emb {
		t.Fatal("c2 not embargoed")
	}
	if emb, _ := e.IsEmbargoed(c1); emb {
		t.Fatal("c1 wrongly embargoed")
	}
	list, _ := e.ListEmbargo()
	if len(list) != 1 || list[0] != c2 {
		t.Fatalf("ListEmbargo = %v, want [c2]", list)
	}
}

// TestEmbargoTravelsInMeta proves a server can read embargo flags: they round-trip
// through refs/cairn/meta into a fresh OpenBare engine.
func TestEmbargoTravelsInMeta(t *testing.T) {
	e := newTestEngine(t)
	_, c2, _ := seedLine(t, e)
	if err := e.MarkEmbargo(c2); err != nil {
		t.Fatal(err)
	}
	setMetaRef(t, e) // export meta (now carrying embargo) + set refs/cairn/meta

	se, err := OpenBare(bareStoreDir(e))
	if err != nil {
		t.Fatal(err)
	}
	defer se.Close()
	if _, err := se.LoadFromMeta(); err != nil {
		t.Fatal(err)
	}
	list, err := se.ListEmbargo()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0] != c2 {
		t.Fatalf("server-side ListEmbargo = %v, want [c2] — embargo did not travel in meta", list)
	}
}
