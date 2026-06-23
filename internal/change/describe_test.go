package change

import "testing"

func TestDescribeVersion(t *testing.T) {
	e := newTestEngine(t)
	root, err := e.RootLine()
	if err != nil {
		t.Fatal(err)
	}
	c1 := seedLineTip(t, e, root.ID, map[string][]byte{"a.txt": []byte("1")})
	if err := e.Tag("v1.0.0", c1, "tester"); err != nil {
		t.Fatal(err)
	}
	seedLineTip(t, e, root.ID, map[string][]byte{"b.txt": []byte("2")})
	c3 := seedLineTip(t, e, root.ID, map[string][]byte{"c.txt": []byte("3")})

	tag, dist, err := e.DescribeVersion(c3)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v1.0.0" || dist != 2 {
		t.Fatalf("DescribeVersion(c3) = %q, %d; want v1.0.0, 2", tag, dist)
	}

	tag0, d0, err := e.DescribeVersion(c1)
	if err != nil {
		t.Fatal(err)
	}
	if tag0 != "v1.0.0" || d0 != 0 {
		t.Fatalf("DescribeVersion(c1) = %q, %d; want v1.0.0, 0", tag0, d0)
	}
}

func TestDescribeVersionNoTag(t *testing.T) {
	e := newTestEngine(t)
	root, err := e.RootLine()
	if err != nil {
		t.Fatal(err)
	}
	seedLineTip(t, e, root.ID, map[string][]byte{"a.txt": []byte("1")})
	c2 := seedLineTip(t, e, root.ID, map[string][]byte{"b.txt": []byte("2")})

	tag, dist, err := e.DescribeVersion(c2)
	if err != nil {
		t.Fatal(err)
	}
	if tag != "" || dist < 1 {
		t.Fatalf("no-tag = %q, %d; want empty, dist>=1", tag, dist)
	}
}
