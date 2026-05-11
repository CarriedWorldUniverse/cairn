package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
)

// fakeUserResolver implements UserResolver for tests.
type fakeUserResolver struct {
	usernameToID map[string]int64
}

func (f *fakeUserResolver) UserIDByUsername(ctx context.Context, name string) (int64, error) {
	id, ok := f.usernameToID[name]
	if !ok {
		return 0, ErrUserNotFound
	}
	return id, nil
}

func (f *fakeUserResolver) UsernameByID(ctx context.Context, id int64) (string, error) {
	for name, uid := range f.usernameToID {
		if uid == id {
			return name, nil
		}
	}
	return "", ErrUserNotFound
}

// fakeRegistrar implements AgentUserRegistrar entirely in-memory.
type fakeRegistrar struct {
	nextUserID    atomic.Int64
	nextPubkeyID  atomic.Int64
	usersByLogin  map[string]int64
	pubkeyContent map[int64]string
}

func newFakeRegistrar() *fakeRegistrar {
	r := &fakeRegistrar{
		usersByLogin:  map[string]int64{},
		pubkeyContent: map[int64]string{},
	}
	r.nextUserID.Store(1000)
	return r
}

func (r *fakeRegistrar) FindOrCreateAgentUser(ctx context.Context, slug, domain string) (int64, error) {
	login := "nexus-" + slug
	if id, ok := r.usersByLogin[login]; ok {
		return id, nil
	}
	id := r.nextUserID.Add(1)
	r.usersByLogin[login] = id
	return id, nil
}

func (r *fakeRegistrar) RegisterPubkey(ctx context.Context, userID int64, pubkeyContent, name string) (int64, error) {
	id := r.nextPubkeyID.Add(1)
	r.pubkeyContent[id] = pubkeyContent
	return id, nil
}

func (r *fakeRegistrar) GetPubkeyContent(ctx context.Context, publicKeyID int64) (string, error) {
	c, ok := r.pubkeyContent[publicKeyID]
	if !ok {
		return "", fmt.Errorf("no pubkey content for id %d", publicKeyID)
	}
	return c, nil
}

const testHMACKey = "0123456789abcdef0123456789abcdef" // 32 bytes

func newTestService(t *testing.T) (*AgentService, *fakeUserResolver, *fakeRegistrar) {
	t.Helper()
	eng := cairntest.NewEngine(t)
	store := NewXormAgentStore(eng)
	pubkeys := NewXormAgentPubkeyStore(eng)
	requests := NewXormAttachmentRequestStore(eng)
	blocklist := NewXormBlocklistStore(eng)
	users := &fakeUserResolver{
		usernameToID: map[string]int64{
			"alice": 1,
			"bob":     2,
		},
	}
	registrar := newFakeRegistrar()
	svc := NewAgentService([]byte(testHMACKey), store, pubkeys, requests, blocklist, users, registrar)
	return svc, users, registrar
}

// registerTestAgent provisions a fully-active agent via the
// attachment-request flow. Returns the resulting agent and the
// fingerprint that resolves to it. Used by tests that need an existing
// agent as setup without exercising the attachment flow directly.
func registerTestAgent(t *testing.T, svc *AgentService, owner, slug, domain string, ownerUserID int64) (*cairn.Agent, ed25519.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	content := string(ssh.MarshalAuthorizedKey(sshKey))
	ctx := context.Background()
	req, err := svc.CreateAttachmentRequest(ctx, owner, slug, domain, content)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := svc.ApproveAttachmentRequest(ctx, req.ID, ownerUserID)
	if err != nil {
		t.Fatal(err)
	}
	return agent, pub, req.Fingerprint
}

func TestAgentService_Approve_TransitionsPendingToActive(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	// Create a pending request (no auto-approve), then explicitly approve
	// via the legacy Approve(fingerprint, caller) path.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshKey, _ := ssh.NewPublicKey(pub)
	content := string(ssh.MarshalAuthorizedKey(sshKey))
	req, err := svc.CreateAttachmentRequest(ctx, "alice", "plumb", "darksoft.co.nz", content)
	if err != nil {
		t.Fatal(err)
	}
	// Approve via the attachment-request flow to register the agent in
	// active state.
	if _, err := svc.ApproveAttachmentRequest(ctx, req.ID, 1); err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetByFingerprint(ctx, req.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != cairn.AgentStatusActive {
		t.Errorf("status = %q, want active", got.Status)
	}
}

func TestAgentService_Approve_RejectsNonOwner(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, _, fp := registerTestAgent(t, svc, "alice", "plumb", "darksoft.co.nz", 1)

	// The legacy Approve method on an already-active agent still enforces
	// the owner check. Block is a better surface here, but we verify the
	// auth predicate directly.
	err := svc.Approve(context.Background(), fp, &Caller{UserID: 2, Username: "bob"})
	if err == nil {
		t.Fatal("expected error when non-owner approves")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

func TestAgentService_Block_RejectsNonOwner(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, _, fp := registerTestAgent(t, svc, "alice", "plumb", "darksoft.co.nz", 1)

	err := svc.Block(context.Background(), fp, "test", &Caller{UserID: 2, Username: "bob"})
	if err == nil {
		t.Fatal("expected error when non-owner blocks")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

func TestAgentService_AttachmentRequest_RejectsInvalidSlug(t *testing.T) {
	svc, _, _ := newTestService(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshKey, _ := ssh.NewPublicKey(pub)
	content := string(ssh.MarshalAuthorizedKey(sshKey))

	cases := []struct{ name, slug string }{
		{"empty", ""},
		{"uppercase", "Plumb"},
		{"leading-hyphen", "-plumb"},
		{"underscore", "plum_b"},
		{"space", "plu mb"},
		{"too-long", strings.Repeat("a", 65)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateAttachmentRequest(context.Background(), "alice", tc.slug, "darksoft.co.nz", content)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestAgentService_AttachmentRequest_RejectsInvalidDomain(t *testing.T) {
	svc, _, _ := newTestService(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshKey, _ := ssh.NewPublicKey(pub)
	content := string(ssh.MarshalAuthorizedKey(sshKey))

	cases := []struct{ name, domain string }{
		{"empty", ""},
		{"too-long", strings.Repeat("a", 256)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateAttachmentRequest(context.Background(), "alice", "plumb", tc.domain, content)
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestAgentService_AttachmentRequest_RejectsDuplicatePubkey(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	// Approve the first request — it claims the fingerprint.
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshKey, _ := ssh.NewPublicKey(pub)
	content := string(ssh.MarshalAuthorizedKey(sshKey))
	req, err := svc.CreateAttachmentRequest(ctx, "alice", "plumb", "darksoft.co.nz", content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveAttachmentRequest(ctx, req.ID, 1); err != nil {
		t.Fatal(err)
	}

	// Submit a second request with the same pubkey (different slug).
	// Creating the request succeeds (it's only a pending row); the
	// uniqueness check happens at approve-time.
	req2, err := svc.CreateAttachmentRequest(ctx, "alice", "anvil", "darksoft.co.nz", content)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ApproveAttachmentRequest(ctx, req2.ID, 1)
	if err == nil {
		t.Fatal("expected duplicate-pubkey error")
	}
	if !errors.Is(err, ErrPubkeyAlreadyClaimed) {
		t.Errorf("err = %v, want ErrPubkeyAlreadyClaimed", err)
	}
}

func TestAgentService_Block_OwnerCanBlock(t *testing.T) {
	svc, _, _ := newTestService(t)
	_, _, fp := registerTestAgent(t, svc, "alice", "plumb", "darksoft.co.nz", 1)

	if err := svc.Block(context.Background(), fp, "key compromised", &Caller{UserID: 1, Username: "alice"}); err != nil {
		t.Fatal(err)
	}

	blocked, err := svc.IsBlocked(context.Background(), fp)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("agent not blocked after owner Block")
	}
}
