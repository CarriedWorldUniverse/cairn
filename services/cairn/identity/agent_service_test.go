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
// Agent users are addressed by their nexus-{slug} login; pubkeys by
// monotonically assigned ids. Content lookups round-trip the OpenSSH
// text the test handed us.
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
	// Start ids at 1000 to avoid collision with the human user ids
	// used in fakeUserResolver (which range 1-99).
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

func TestAgentService_Register_AutoApproveWhenProposerIsOwner(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	got, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, &Caller{UserID: 1, Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != cairn.AgentStatusActive {
		t.Errorf("status = %q, want %q (auto-approve when proposer == owner)", got.Status, cairn.AgentStatusActive)
	}
	if got.ActivatedAt == nil || got.ActivatedAt.IsZero() {
		t.Error("ActivatedAt not set on auto-approved agent")
	}
	// The agent's fingerprint is recoverable via the join.
	fp := svc.FingerprintEd25519(pub)
	if fp == "" {
		t.Error("Fingerprint not computable")
	}
	lookedUp, err := svc.LookupAgentByFingerprint(ctx, fp)
	if err != nil {
		t.Fatalf("lookup by fingerprint: %v", err)
	}
	if lookedUp.ID != got.ID {
		t.Errorf("lookup ID = %d, want %d", lookedUp.ID, got.ID)
	}
}

func TestAgentService_Register_PendingWhenProposerIsNotOwner(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	got, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, &Caller{UserID: 2, Username: "bob"})
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != cairn.AgentStatusPending {
		t.Errorf("status = %q, want %q (pending when proposer != owner)", got.Status, cairn.AgentStatusPending)
	}
	if got.ActivatedAt != nil {
		t.Error("ActivatedAt should be nil for pending agents")
	}
}

func TestAgentService_Register_PendingWhenAnonymous(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	got, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != cairn.AgentStatusPending {
		t.Errorf("status = %q, want %q (anonymous proposal is pending)", got.Status, cairn.AgentStatusPending)
	}
}

func TestAgentService_Register_RejectsUnknownOwner(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	_, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "no-such-user",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, nil)
	if err == nil {
		t.Fatal("expected error for unknown owner")
	}
	if !errors.Is(err, ErrUserNotFound) {
		t.Errorf("err = %v, want ErrUserNotFound", err)
	}
}

func TestAgentService_Register_DoesNotAutoApproveZeroUserID(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	got, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, &Caller{UserID: 0, Username: "spoofed"})
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != cairn.AgentStatusPending {
		t.Errorf("status = %q, want pending (zero UserID must not auto-approve)", got.Status)
	}
}

func TestAgentService_Register_RejectsDuplicatePubkey(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	req := RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}
	caller := &Caller{UserID: 1, Username: "alice"}

	if _, err := svc.Register(ctx, req, caller); err != nil {
		t.Fatal(err)
	}
	// Re-registering the same pubkey collides on cairn_agent_pubkey
	// uniqueness (the fingerprint).
	_, err := svc.Register(ctx, req, caller)
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	if !errors.Is(err, ErrPubkeyAlreadyClaimed) {
		t.Errorf("err = %v, want ErrPubkeyAlreadyClaimed", err)
	}
}

func TestAgentService_Register_FingerprintIsDeterministic(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	_, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, &Caller{UserID: 1, Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	want := Fingerprint([]byte(testHMACKey), pub)
	got, err := svc.LookupAgentByFingerprint(ctx, want)
	if err != nil {
		t.Fatalf("lookup by computed fingerprint: %v", err)
	}
	if got.Slug != "plumb" {
		t.Errorf("got.Slug = %q, want plumb", got.Slug)
	}
}

func TestAgentService_Approve_TransitionsPendingToActive(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	pending, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != cairn.AgentStatusPending {
		t.Fatalf("setup: status = %q, want pending", pending.Status)
	}

	fp := svc.FingerprintEd25519(pub)
	if err := svc.Approve(ctx, fp, &Caller{UserID: 1, Username: "alice"}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetByFingerprint(ctx, fp)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != cairn.AgentStatusActive {
		t.Errorf("status = %q, want active", got.Status)
	}
}

func TestAgentService_Approve_RejectsNonOwner(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, _ = svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz", PublicKey: pub,
	}, nil)

	fp := svc.FingerprintEd25519(pub)
	err := svc.Approve(ctx, fp, &Caller{UserID: 2, Username: "bob"})
	if err == nil {
		t.Fatal("expected error when non-owner approves")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

func TestAgentService_Block_RejectsNonOwner(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, _ = svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz", PublicKey: pub,
	}, &Caller{UserID: 1, Username: "alice"})

	fp := svc.FingerprintEd25519(pub)
	err := svc.Block(ctx, fp, "test", &Caller{UserID: 2, Username: "bob"})
	if err == nil {
		t.Fatal("expected error when non-owner blocks")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

func TestAgentService_Register_RejectsInvalidSlug(t *testing.T) {
	svc, _, _ := newTestService(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

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
			_, err := svc.Register(context.Background(), RegisterRequest{
				ProposedOwner: "alice",
				Slug:          tc.slug,
				Domain:        "darksoft.co.nz",
				PublicKey:     pub,
			}, &Caller{UserID: 1, Username: "alice"})
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestAgentService_Register_RejectsInvalidDomain(t *testing.T) {
	svc, _, _ := newTestService(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	cases := []struct{ name, domain string }{
		{"empty", ""},
		{"too-long", strings.Repeat("a", 256)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Register(context.Background(), RegisterRequest{
				ProposedOwner: "alice",
				Slug:          "plumb",
				Domain:        tc.domain,
				PublicKey:     pub,
			}, &Caller{UserID: 1, Username: "alice"})
			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestAgentService_Block_OwnerCanBlock(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, _ = svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz", PublicKey: pub,
	}, &Caller{UserID: 1, Username: "alice"})

	fp := svc.FingerprintEd25519(pub)
	if err := svc.Block(ctx, fp, "key compromised", &Caller{UserID: 1, Username: "alice"}); err != nil {
		t.Fatal(err)
	}

	blocked, err := svc.IsBlocked(ctx, fp)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("agent not blocked after owner Block")
	}
}
