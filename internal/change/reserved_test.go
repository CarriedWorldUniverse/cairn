package change

import "testing"

func TestReservedMetaName(t *testing.T) {
	bad := []string{
		".git", ".GIT", ".Git", ".cairn", ".CAIRN", ".Cairn",
		".git.", ".git ", ".git. .", ".cairn.", ".cairn  ",
		"git~1", "GIT~1", "git~2", "git~1.", "cairn~1", "CAIRN~1", "cairn~12 ",
		".g‌it", ".‍git"[0:0] + ".git‌", "\ufeff.git", ".ca‎irn", ".c‪airn",
	}
	for _, n := range bad {
		if err := validTreeEntryName(n); err == nil {
			t.Errorf("validTreeEntryName(%q) = nil, want refusal", n)
		}
	}
	good := []string{".gitignore", ".gitattributes", ".gitmodules", ".cairnignore", "cairn", "git", "gitx~1", "git~0", "git~a", ".github", "a.git", "cairn.db", ".cairn2"}
	for _, n := range good {
		if err := validTreeEntryName(n); err != nil {
			t.Errorf("validTreeEntryName(%q) = %v, want ok", n, err)
		}
	}
}

func TestValidTreePathRefusesMetaComponents(t *testing.T) {
	for _, p := range []string{".cairn/cairn.db", "sub/.git/config", "a/.CAIRN./wc.json", "GIT~1/hooks/x"} {
		if err := validTreePath(p); err == nil {
			t.Errorf("validTreePath(%q) = nil, want refusal", p)
		}
	}
	if err := validTreePath("src/.gitignore"); err != nil {
		t.Errorf("validTreePath(.gitignore) = %v", err)
	}
}
