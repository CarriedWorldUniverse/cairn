package change

import "testing"

func TestTagCreateList(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	tip := seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	if err := e.Tag("v1.0.0", tip, "rel"); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	tags, err := e.ListTags()
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 1 || tags[0].Name != "v1.0.0" || tags[0].Commit != tip {
		t.Fatalf("tags = %+v", tags)
	}
}

func TestTagDuplicateRejected(t *testing.T) {
	e := newTestEngine(t)
	main, _ := e.LineByName("main")
	tip := seedLineTip(t, e, main.ID, map[string][]byte{"a.txt": []byte("a\n")})
	if err := e.Tag("v1", tip, "rel"); err != nil {
		t.Fatalf("Tag: %v", err)
	}
	if err := e.Tag("v1", tip, "rel"); err == nil {
		t.Fatal("duplicate tag name: want error (name is PRIMARY KEY), got nil")
	}
}
