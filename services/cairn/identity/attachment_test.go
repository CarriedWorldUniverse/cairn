// Cairn-specific code; AGPLv3. See LICENSING.md.

package identity

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"golang.org/x/crypto/ssh"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
)

// TestFingerprint_BothPathsAgree pins the invariant that the
// raw-ed25519 path (Fingerprint) and the OpenSSH-text path
// (FingerprintFromContent) produce identical fingerprints for the same
// logical key. If they ever diverge, cross-path lookups (e.g. signature
// verification finding an agent registered via attachment-request) fail
// silently.
func TestFingerprint_BothPathsAgree(t *testing.T) {
	hmacKey := bytes.Repeat([]byte{0xab}, 32)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	fpA := Fingerprint(hmacKey, pub)

	sshKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	authorizedKey := ssh.MarshalAuthorizedKey(sshKey)
	fpB, err := FingerprintFromContent(hmacKey, string(authorizedKey))
	if err != nil {
		t.Fatal(err)
	}

	if fpA != fpB {
		t.Errorf("paths disagree:\n  raw-bytes path:    %s\n  openssh-text path: %s", fpA, fpB)
	}
}

func marshalPubForTest(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(sshPub))
}

func TestCreateAttachmentRequest_Pending(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	content := marshalPubForTest(t, pub)

	req, err := svc.CreateAttachmentRequest(ctx, "alice", "plumb", "darksoft.co.nz", content)
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != cairn.AttachmentRequestPending {
		t.Errorf("status = %q, want pending", req.Status)
	}
	if req.Fingerprint == "" {
		t.Error("fingerprint not computed")
	}
}

func TestCreateAttachmentRequest_RejectsUnknownOwner(t *testing.T) {
	svc, _, _ := newTestService(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err := svc.CreateAttachmentRequest(context.Background(),
		"no-such-user", "plumb", "darksoft.co.nz", marshalPubForTest(t, pub))
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("err = %v, want ErrUserNotFound", err)
	}
}

func TestCreateAttachmentRequest_RejectsInvalidSlug(t *testing.T) {
	svc, _, _ := newTestService(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err := svc.CreateAttachmentRequest(context.Background(),
		"alice", "Plumb", "darksoft.co.nz", marshalPubForTest(t, pub))
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
}

func TestApproveAttachmentRequest_HappyPath(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	content := marshalPubForTest(t, pub)
	req, err := svc.CreateAttachmentRequest(ctx, "alice", "plumb", "darksoft.co.nz", content)
	if err != nil {
		t.Fatal(err)
	}

	agent, err := svc.ApproveAttachmentRequest(ctx, req.ID, 1 /*alice*/)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if agent.Status != cairn.AgentStatusActive {
		t.Errorf("status = %q, want active", agent.Status)
	}

	// Looking up by the request's fingerprint finds the agent.
	got, err := svc.LookupAgentByFingerprint(ctx, req.Fingerprint)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ID != agent.ID {
		t.Errorf("looked-up agent id = %d, want %d", got.ID, agent.ID)
	}
}

func TestApproveAttachmentRequest_AlreadyDecided(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	content := marshalPubForTest(t, pub)
	req, err := svc.CreateAttachmentRequest(ctx, "alice", "plumb", "darksoft.co.nz", content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveAttachmentRequest(ctx, req.ID, 1); err != nil {
		t.Fatal(err)
	}
	// Second approval must be rejected.
	_, err = svc.ApproveAttachmentRequest(ctx, req.ID, 1)
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("err = %v, want ErrAlreadyDecided", err)
	}
}

func TestRejectAttachmentRequest(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	content := marshalPubForTest(t, pub)
	req, err := svc.CreateAttachmentRequest(ctx, "alice", "plumb", "darksoft.co.nz", content)
	if err != nil {
		t.Fatal(err)
	}

	if err := svc.RejectAttachmentRequest(ctx, req.ID, 1); err != nil {
		t.Fatal(err)
	}
	// Approving a rejected request fails.
	_, err = svc.ApproveAttachmentRequest(ctx, req.ID, 1)
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("err = %v, want ErrAlreadyDecided", err)
	}
}

func TestListPendingForOwner(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := svc.CreateAttachmentRequest(ctx, "alice", "plumb", "darksoft.co.nz", marshalPubForTest(t, pub1)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateAttachmentRequest(ctx, "alice", "anvil", "darksoft.co.nz", marshalPubForTest(t, pub2)); err != nil {
		t.Fatal(err)
	}

	got, err := svc.ListPendingForOwner(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}

	// Other user has none.
	got, err = svc.ListPendingForOwner(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0 (bob has no pending requests)", len(got))
	}
}
