package change

import (
	"testing"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestOpenCreatesRootLine(t *testing.T) {
	e := newTestEngine(t)
	root, err := e.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName(main): %v", err)
	}
	if root.ParentLine != "" {
		t.Fatalf("root parent = %q, want empty", root.ParentLine)
	}
	if root.Status != "open" {
		t.Fatalf("root status = %q, want open", root.Status)
	}
}

func TestOpenIdempotent(t *testing.T) {
	dir := t.TempDir()
	e1, err := Open(dir)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	_ = e1.Close()
	e2, err := Open(dir)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer e2.Close()
	root, err := e2.LineByName("main")
	if err != nil {
		t.Fatalf("LineByName after reopen: %v", err)
	}
	if root.Status != "open" {
		t.Fatalf("root status = %q", root.Status)
	}
}
