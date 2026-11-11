package change

import (
	"strings"
	"testing"
)

func TestSafeName(t *testing.T) {
	if SafeName("feat/x") != "feat/x" {
		t.Fatal("clean name altered")
	}
	got := SafeName("evil\x1b[2J\x07")
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("SafeName left control bytes: %q", got)
	}
}
