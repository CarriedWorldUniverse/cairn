package hook

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// fakeUserResolver mirrors the one in services/cairn/identity for cross-package use.
type fakeUserResolver struct {
	usernameToID map[string]int64
}

func (f *fakeUserResolver) UserIDByUsername(ctx context.Context, name string) (int64, error) {
	id, ok := f.usernameToID[name]
	if !ok {
		return 0, cairnidentity.ErrUserNotFound
	}
	return id, nil
}

func (f *fakeUserResolver) UsernameByID(ctx context.Context, id int64) (string, error) {
	for name, uid := range f.usernameToID {
		if uid == id {
			return name, nil
		}
	}
	return "", cairnidentity.ErrUserNotFound
}

// fakeRegistrar implements AgentUserRegistrar in-memory for hook tests.
// Mirrors the identity-package fake but redefined here to avoid the
// circular import.
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

func newTestSvc(t *testing.T) *cairnidentity.AgentService {
	t.Helper()
	eng := cairntest.NewEngine(t)
	store := cairnidentity.NewXormAgentStore(eng)
	pubkeys := cairnidentity.NewXormAgentPubkeyStore(eng)
	requests := cairnidentity.NewXormAttachmentRequestStore(eng)
	blocklist := cairnidentity.NewXormBlocklistStore(eng)
	users := &fakeUserResolver{usernameToID: map[string]int64{"alice": 1, "bob": 2}}
	registrar := newFakeRegistrar()
	return cairnidentity.NewAgentService(
		[]byte("0123456789abcdef0123456789abcdef"),
		store, pubkeys, requests, blocklist, users, registrar,
	)
}

const happyTrailers = "Test commit\n\nAgent-Id: plumb\nAgent-Owner: alice\nAgent-Domain: darksoft.co.nz\n"

// registerActivePlumb provisions an active "plumb" agent via the
// attachment-request flow (Plan 8). Returns the raw keypair so the
// caller can sign test commits.
func registerActivePlumb(t *testing.T, svc *cairnidentity.AgentService) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	content := string(ssh.MarshalAuthorizedKey(sshKey))
	ctx := context.Background()
	req, err := svc.CreateAttachmentRequest(ctx, "alice", "plumb", "darksoft.co.nz", content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApproveAttachmentRequest(ctx, req.ID, 1); err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestVerifyAgentCommits_AllPass(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t)

	_, priv := registerActivePlumb(t, svc)
	signer, _ := ssh.NewSignerFromKey(priv)
	commit := buildSignedCommit(t, happyTrailers, signer)

	commits := []CommitToVerify{{
		SHA:         "abc123",
		AuthorEmail: "nexus-plumb@darksoft.co.nz",
		Message:     happyTrailers,
		Raw:         commit,
	}}

	if err := VerifyAgentCommits(ctx, commits, svc, true, true); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestVerifyAgentCommits_EnforceFalseSkips(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t)

	commits := []CommitToVerify{{
		SHA:         "abc123",
		AuthorEmail: "nexus-ghost@darksoft.co.nz",
		Message:     "no trailers\n",
		Raw:         []byte("no signature here either"),
	}}

	if err := VerifyAgentCommits(ctx, commits, svc, false, true); err != nil {
		t.Errorf("expected nil with enforce=false, got %v", err)
	}
}

func TestVerifyAgentCommits_OrphanAgentAllowedWhenFlagOff(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	body := "Test commit\n"
	commit := buildSignedCommit(t, body, signer)
	commits := []CommitToVerify{
		{
			SHA:         "abc123",
			AuthorEmail: "nexus-ghost@darksoft.co.nz",
			Message:     body,
			Raw:         commit,
		},
	}
	if err := VerifyAgentCommits(ctx, commits, svc, true /*enforce*/, false /*rejectOrphan*/); err != nil {
		t.Errorf("expected nil with rejectOrphanAgents=false, got %v", err)
	}
	_ = pub
}

func TestVerifyAgentCommits_NonAgentEmailSkipped(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t)

	commits := []CommitToVerify{{
		SHA:         "abc123",
		AuthorEmail: "nexus@darksoft.co.nz",
		Message:     "human commit\n",
		Raw:         []byte("doesn't matter"),
	}}

	if err := VerifyAgentCommits(ctx, commits, svc, true, true); err != nil {
		t.Errorf("expected nil for non-agent email, got %v", err)
	}
}

func TestVerifyAgentCommits_OrphanAgent(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t)

	commits := []CommitToVerify{{
		SHA:         "abc123",
		AuthorEmail: "nexus-ghost@darksoft.co.nz",
		Message:     "ghost commit\n",
		Raw:         []byte("anything"),
	}}

	err := VerifyAgentCommits(ctx, commits, svc, true, true)
	if !errors.Is(err, ErrOrphanAgent) {
		t.Errorf("err = %v, want ErrOrphanAgent", err)
	}
}

// TestVerifyAgentCommits_PendingAgent: in the Plan 8 attachment-request
// model the cairn_agent row is only created at approval time and is
// activated atomically — a pending-status cairn_agent is no longer
// reachable through the public service surface. The verifier's
// status-gate (ErrAgentNotActive) remains in place defensively; this
// test is intentionally skipped because the reachable state space no
// longer produces it through the supported flows.
func TestVerifyAgentCommits_PendingAgent(t *testing.T) {
	t.Skip("pending-status cairn_agent unreachable post Plan 8; verifier gate retained defensively")
}

func TestVerifyAgentCommits_BlockedAgent(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t)

	pub, priv := registerActivePlumb(t, svc)
	fp := cairnidentity.Fingerprint([]byte("0123456789abcdef0123456789abcdef"), pub)
	if err := svc.Block(ctx, fp, "key compromised", &cairnidentity.Caller{UserID: 1, Username: "alice"}); err != nil {
		t.Fatal(err)
	}

	signer, _ := ssh.NewSignerFromKey(priv)
	commit := buildSignedCommit(t, happyTrailers, signer)

	commits := []CommitToVerify{{
		SHA:         "abc123",
		AuthorEmail: "nexus-plumb@darksoft.co.nz",
		Message:     happyTrailers,
		Raw:         commit,
	}}

	err := VerifyAgentCommits(ctx, commits, svc, true, true)
	if !errors.Is(err, ErrAgentBlocked) {
		t.Errorf("err = %v, want ErrAgentBlocked", err)
	}
}

func TestVerifyAgentCommits_MissingSignature(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t)

	registerActivePlumb(t, svc)

	unsigned := []byte("tree abc123\nauthor nexus-plumb <nexus-plumb@darksoft.co.nz> 1700000000 +0000\n\n" + happyTrailers)
	commits := []CommitToVerify{{
		SHA:         "abc123",
		AuthorEmail: "nexus-plumb@darksoft.co.nz",
		Message:     happyTrailers,
		Raw:         unsigned,
	}}

	err := VerifyAgentCommits(ctx, commits, svc, true, true)
	if !errors.Is(err, ErrSignatureMissing) {
		t.Errorf("err = %v, want ErrSignatureMissing", err)
	}
}

func TestVerifyAgentCommits_TamperedSignature(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t)

	_, priv := registerActivePlumb(t, svc)
	signer, _ := ssh.NewSignerFromKey(priv)
	commit := buildSignedCommit(t, happyTrailers, signer)

	for i := range commit {
		if commit[i] == 'T' && i+4 < len(commit) && string(commit[i:i+4]) == "Test" {
			commit[i] = 'E'
			commit[i+1] = 'v'
			commit[i+2] = 'i'
			commit[i+3] = 'l'
			break
		}
	}

	commits := []CommitToVerify{{
		SHA:         "abc123",
		AuthorEmail: "nexus-plumb@darksoft.co.nz",
		Message:     happyTrailers,
		Raw:         commit,
	}}

	err := VerifyAgentCommits(ctx, commits, svc, true, true)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("err = %v, want ErrInvalidSignature", err)
	}
}

func TestVerifyAgentCommits_TrailerMismatch(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t)

	_, priv := registerActivePlumb(t, svc)
	signer, _ := ssh.NewSignerFromKey(priv)
	wrongTrailers := "Test commit\n\nAgent-Id: not-plumb\nAgent-Owner: alice\nAgent-Domain: darksoft.co.nz\n"
	commit := buildSignedCommit(t, wrongTrailers, signer)

	commits := []CommitToVerify{{
		SHA:         "abc123",
		AuthorEmail: "nexus-plumb@darksoft.co.nz",
		Message:     wrongTrailers,
		Raw:         commit,
	}}

	err := VerifyAgentCommits(ctx, commits, svc, true, true)
	if !errors.Is(err, cairnidentity.ErrTrailerMismatch) {
		t.Errorf("err = %v, want ErrTrailerMismatch", err)
	}
}

func TestVerifyAgentCommits_StopsAtFirstFailure(t *testing.T) {
	ctx := context.Background()
	svc := newTestSvc(t)

	_, priv := registerActivePlumb(t, svc)
	signer, _ := ssh.NewSignerFromKey(priv)
	good := buildSignedCommit(t, happyTrailers, signer)

	commits := []CommitToVerify{
		{
			SHA:         "good1",
			AuthorEmail: "nexus-plumb@darksoft.co.nz",
			Message:     happyTrailers,
			Raw:         good,
		},
		{
			SHA:         "orphan",
			AuthorEmail: "nexus-ghost@darksoft.co.nz",
			Message:     "ghost\n",
			Raw:         []byte("anything"),
		},
		{
			SHA:         "good2",
			AuthorEmail: "nexus-plumb@darksoft.co.nz",
			Message:     happyTrailers,
			Raw:         good,
		},
	}

	err := VerifyAgentCommits(ctx, commits, svc, true, true)
	if !errors.Is(err, ErrOrphanAgent) {
		t.Errorf("err = %v, want ErrOrphanAgent (from second commit)", err)
	}
}
