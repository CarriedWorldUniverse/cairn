package cairn

import (
	"testing"
	"time"
)

func TestAgent_TableName(t *testing.T) {
	var a Agent
	if got, want := a.TableName(), "cairn_agent"; got != want {
		t.Errorf("TableName() = %q, want %q", got, want)
	}
}

func TestAgent_StatusValues(t *testing.T) {
	if AgentStatusPending != "pending" {
		t.Errorf("AgentStatusPending = %q, want %q", AgentStatusPending, "pending")
	}
	if AgentStatusActive != "active" {
		t.Errorf("AgentStatusActive = %q, want %q", AgentStatusActive, "active")
	}
}

func TestAgent_IsActive(t *testing.T) {
	cases := []struct {
		name   string
		status AgentStatus
		want   bool
	}{
		{"pending", AgentStatusPending, false},
		{"active", AgentStatusActive, true},
		{"unknown", AgentStatus("unknown"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := Agent{Status: tc.status}
			if got := a.IsActive(); got != tc.want {
				t.Errorf("IsActive() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAgent_RequiredFields(t *testing.T) {
	now := time.Now()
	a := Agent{
		Fingerprint: "cairn:abc123",
		UserID:      42,
		Slug:        "plumb",
		Domain:      "darksoft.co.nz",
		PublicKey:   []byte{1, 2, 3, 4},
		Status:      AgentStatusActive,
		CreatedAt:   now,
		ActivatedAt: &now,
	}
	// Sanity: every required field is set without compile error.
	if a.Fingerprint == "" || a.UserID == 0 || a.Slug == "" {
		t.Error("required fields zero")
	}
}
