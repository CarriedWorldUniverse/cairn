package identity

import (
	"errors"
	"testing"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

func testAgent() *cairn.Agent {
	return &cairn.Agent{
		ID:     1,
		Slug:   "plumb",
		Domain: "darksoft.co.nz",
	}
}

func TestVerifyTrailers_AllMatching(t *testing.T) {
	msg := `Add agent registration endpoint

Agent-Id: plumb
Agent-Owner: alice
Agent-Domain: darksoft.co.nz
`
	if err := VerifyTrailers(msg, testAgent(), "alice"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestVerifyTrailers_NoCairnTrailers(t *testing.T) {
	msg := `Just a commit

Co-Authored-By: somebody@example.com
`
	if err := VerifyTrailers(msg, testAgent(), "alice"); err != nil {
		t.Errorf("expected nil (no Cairn trailers means no check), got %v", err)
	}
}

func TestVerifyTrailers_NoTrailersAtAll(t *testing.T) {
	msg := "Single-line commit"
	if err := VerifyTrailers(msg, testAgent(), "alice"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestVerifyTrailers_AgentIdMismatch(t *testing.T) {
	msg := `Add thing

Agent-Id: anvil
Agent-Owner: alice
Agent-Domain: darksoft.co.nz
`
	err := VerifyTrailers(msg, testAgent(), "alice")
	if !errors.Is(err, ErrTrailerMismatch) {
		t.Errorf("err = %v, want ErrTrailerMismatch", err)
	}
}

func TestVerifyTrailers_AgentOwnerMismatch(t *testing.T) {
	msg := `Add thing

Agent-Id: plumb
Agent-Owner: bob
Agent-Domain: darksoft.co.nz
`
	err := VerifyTrailers(msg, testAgent(), "alice")
	if !errors.Is(err, ErrTrailerMismatch) {
		t.Errorf("err = %v, want ErrTrailerMismatch", err)
	}
}

func TestVerifyTrailers_AgentDomainMismatch(t *testing.T) {
	msg := `Add thing

Agent-Id: plumb
Agent-Owner: alice
Agent-Domain: example.com
`
	err := VerifyTrailers(msg, testAgent(), "alice")
	if !errors.Is(err, ErrTrailerMismatch) {
		t.Errorf("err = %v, want ErrTrailerMismatch", err)
	}
}

func TestVerifyTrailers_WhitespaceTolerant(t *testing.T) {
	msg := `Add thing

Agent-Id:   plumb
Agent-Owner:	alice
Agent-Domain:darksoft.co.nz
`
	if err := VerifyTrailers(msg, testAgent(), "alice"); err != nil {
		t.Errorf("whitespace-tolerant parse failed: %v", err)
	}
}

func TestVerifyTrailers_MixedTrailers(t *testing.T) {
	// Cairn trailers alongside other common trailers (Co-Authored-By etc.)
	msg := `Add agent registration endpoint

Some explanatory paragraph in the middle.

Agent-Id: plumb
Agent-Owner: alice
Agent-Domain: darksoft.co.nz
Co-Authored-By: somebody@example.com
Signed-off-by: someone@example.org
`
	if err := VerifyTrailers(msg, testAgent(), "alice"); err != nil {
		t.Errorf("expected nil with mixed trailers, got %v", err)
	}
}

func TestVerifyTrailers_PartialCairnTrailers(t *testing.T) {
	// Only Agent-Id present — should still validate it.
	msg := `Thing

Agent-Id: anvil
`
	err := VerifyTrailers(msg, testAgent(), "alice")
	if !errors.Is(err, ErrTrailerMismatch) {
		t.Errorf("err = %v, want ErrTrailerMismatch (partial trailer should still be validated)", err)
	}
}
