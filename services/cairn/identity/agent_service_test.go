package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
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

const testHMACKey = "0123456789abcdef0123456789abcdef" // 32 bytes

func newTestService(t *testing.T) (*AgentService, *fakeUserResolver) {
	t.Helper()
	eng := cairntest.NewEngine(t)
	store := NewXormAgentStore(eng)
	blocklist := NewXormBlocklistStore(eng)
	users := &fakeUserResolver{
		usernameToID: map[string]int64{
			"alice": 1,
			"bob":     2,
		},
	}
	svc := NewAgentService([]byte(testHMACKey), store, blocklist, users)
	return svc, users
}

func TestAgentService_Register_AutoApproveWhenProposerIsOwner(t *testing.T) {
	svc, _ := newTestService(t)
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
	if got.Fingerprint == "" {
		t.Error("Fingerprint not populated")
	}
}

func TestAgentService_Register_PendingWhenProposerIsNotOwner(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Bob proposes a Plumb-as-alice-agent — not auto-approved.
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
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// No caller (anonymous proposal — agent on a remote machine).
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
	svc, _ := newTestService(t)
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
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Caller with UserID 0 (should never happen in production but
	// defends against a future auth middleware bug).
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

func TestAgentService_Register_RejectsDuplicate(t *testing.T) {
	svc, _ := newTestService(t)
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
	_, err := svc.Register(ctx, req, caller)
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	if !errors.Is(err, ErrAgentExists) {
		t.Errorf("err = %v, want ErrAgentExists", err)
	}
}

func TestAgentService_Register_FingerprintIsDeterministic(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	got1, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, &Caller{UserID: 1, Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}

	// Compute the expected fingerprint independently and verify match.
	want := Fingerprint([]byte(testHMACKey), pub)
	if got1.Fingerprint != want {
		t.Errorf("Fingerprint = %q, want %q", got1.Fingerprint, want)
	}
}

func TestAgentService_Approve_TransitionsPendingToActive(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Anonymous proposal -> pending.
	pending, err := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Owner approves.
	if err := svc.Approve(ctx, pending.Fingerprint, &Caller{UserID: 1, Username: "alice"}); err != nil {
		t.Fatal(err)
	}

	got, err := svc.GetByFingerprint(ctx, pending.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != cairn.AgentStatusActive {
		t.Errorf("status = %q, want active", got.Status)
	}
}

func TestAgentService_Approve_RejectsNonOwner(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	pending, _ := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz", PublicKey: pub,
	}, nil)

	err := svc.Approve(ctx, pending.Fingerprint, &Caller{UserID: 2, Username: "bob"})
	if err == nil {
		t.Fatal("expected error when non-owner approves")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

func TestAgentService_Block_RejectsNonOwner(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	a, _ := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz", PublicKey: pub,
	}, &Caller{UserID: 1, Username: "alice"})

	err := svc.Block(ctx, a.Fingerprint, "test", &Caller{UserID: 2, Username: "bob"})
	if err == nil {
		t.Fatal("expected error when non-owner blocks")
	}
	if !errors.Is(err, ErrForbidden) {
		t.Errorf("err = %v, want ErrForbidden", err)
	}
}

func TestAgentService_Register_RejectsInvalidSlug(t *testing.T) {
	svc, _ := newTestService(t)
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
	svc, _ := newTestService(t)
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
	svc, _ := newTestService(t)
	ctx := context.Background()

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	a, _ := svc.Register(ctx, RegisterRequest{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz", PublicKey: pub,
	}, &Caller{UserID: 1, Username: "alice"})

	if err := svc.Block(ctx, a.Fingerprint, "key compromised", &Caller{UserID: 1, Username: "alice"}); err != nil {
		t.Fatal(err)
	}

	blocked, err := svc.IsBlocked(ctx, a.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("agent not blocked after owner Block")
	}
}
