package change

import (
	"path/filepath"
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

var _ = filepath.Join
