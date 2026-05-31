package sshd

import "testing"

func TestParseGitCommand(t *testing.T) {
	cases := []struct {
		in                          string
		wantVerb, wantOrg, wantSlug string
		wantErr                     bool
	}{
		{`git-upload-pack '/org-1/widgets.git'`, "git-upload-pack", "org-1", "widgets", false},
		{`git-receive-pack '/org-1/widgets'`, "git-receive-pack", "org-1", "widgets", false},
		{`git-upload-pack "/org-1/widgets.git"`, "git-upload-pack", "org-1", "widgets", false},
		{`scp -t /tmp`, "", "", "", true},
		{`git-upload-pack '/widgets.git'`, "", "", "", true}, // missing org segment
		{``, "", "", "", true},
	}
	for _, c := range cases {
		verb, org, slug, err := ParseGitCommand(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: want error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", c.in, err)
			continue
		}
		if verb != c.wantVerb || org != c.wantOrg || slug != c.wantSlug {
			t.Errorf("%q: got (%s,%s,%s), want (%s,%s,%s)", c.in, verb, org, slug, c.wantVerb, c.wantOrg, c.wantSlug)
		}
	}
}
