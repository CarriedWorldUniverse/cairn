package cairn

import "testing"

func TestIsAgentAuthor(t *testing.T) {
	cases := []struct {
		email string
		want  bool
	}{
		{"nexus-plumb@darksoft.co.nz", true},
		{"nexus@darksoft.co.nz", false},
		{"nexus-@darksoft.co.nz", false},
		{"", false},
		{"NEXUS-plumb@x.y", false},
	}
	for _, tc := range cases {
		t.Run(tc.email, func(t *testing.T) {
			if got := IsAgentAuthor(tc.email); got != tc.want {
				t.Errorf("IsAgentAuthor(%q) = %v, want %v", tc.email, got, tc.want)
			}
		})
	}
}

func TestAgentAuthorSlug(t *testing.T) {
	cases := []struct {
		email string
		want  string
	}{
		{"nexus-plumb@darksoft.co.nz", "plumb"},
		{"nexus-anvil@example.com", "anvil"},
		{"nexus@darksoft.co.nz", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.email, func(t *testing.T) {
			if got := AgentAuthorSlug(tc.email); got != tc.want {
				t.Errorf("AgentAuthorSlug(%q) = %q, want %q", tc.email, got, tc.want)
			}
		})
	}
}

func TestAgentAuthorBadge(t *testing.T) {
	cases := []struct {
		email string
		want  string
	}{
		{"nexus-plumb@darksoft.co.nz", "agent:plumb"},
		{"nexus@darksoft.co.nz", ""},
	}
	for _, tc := range cases {
		t.Run(tc.email, func(t *testing.T) {
			if got := AgentAuthorBadge(tc.email); got != tc.want {
				t.Errorf("AgentAuthorBadge(%q) = %q, want %q", tc.email, got, tc.want)
			}
		})
	}
}
