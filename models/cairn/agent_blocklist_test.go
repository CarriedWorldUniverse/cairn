package cairn

import (
	"testing"
	"time"
)

func TestAgentBlocklist_TableName(t *testing.T) {
	var b AgentBlocklist
	if got, want := b.TableName(), "cairn_agent_blocklist"; got != want {
		t.Errorf("TableName() = %q, want %q", got, want)
	}
}

func TestAgentBlocklist_RequiredFields(t *testing.T) {
	b := AgentBlocklist{
		AgentID:   42,
		BlockedAt: time.Now(),
		Reason:    "key compromised",
	}
	if b.AgentID == 0 || b.BlockedAt.IsZero() {
		t.Error("required fields zero")
	}
}
