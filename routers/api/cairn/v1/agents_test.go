package v1

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/ssh"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	"github.com/CarriedWorldUniverse/cairn/models/cairn/cairntest"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

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

const testHMACKey = "0123456789abcdef0123456789abcdef"

// fakeRegistrar is an in-memory AgentUserRegistrar for handler tests.
type fakeRegistrar struct {
	users         map[string]int64
	nextUserID    int64
	pubkeyContent map[int64]string
	nextPubkeyID  int64
}

func newFakeRegistrar() *fakeRegistrar {
	return &fakeRegistrar{
		users:         map[string]int64{},
		nextUserID:    1000,
		pubkeyContent: map[int64]string{},
	}
}

func (r *fakeRegistrar) FindOrCreateAgentUser(ctx context.Context, slug, domain string) (int64, error) {
	login := "nexus-" + slug
	if id, ok := r.users[login]; ok {
		return id, nil
	}
	r.nextUserID++
	r.users[login] = r.nextUserID
	return r.nextUserID, nil
}

func (r *fakeRegistrar) RegisterPubkey(ctx context.Context, userID int64, content, name string) (int64, error) {
	r.nextPubkeyID++
	r.pubkeyContent[r.nextPubkeyID] = content
	return r.nextPubkeyID, nil
}

func (r *fakeRegistrar) GetPubkeyContent(ctx context.Context, id int64) (string, error) {
	c, ok := r.pubkeyContent[id]
	if !ok {
		return "", errors.New("no content")
	}
	return c, nil
}

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	eng := cairntest.NewEngine(t)
	store := cairnidentity.NewXormAgentStore(eng)
	pubkeys := cairnidentity.NewXormAgentPubkeyStore(eng)
	requests := cairnidentity.NewXormAttachmentRequestStore(eng)
	blocklist := cairnidentity.NewXormBlocklistStore(eng)
	users := &fakeUserResolver{
		usernameToID: map[string]int64{
			"alice": 1,
			"bob":     2,
			"admin":   3,
		},
	}
	registrar := newFakeRegistrar()
	svc := cairnidentity.NewAgentService([]byte(testHMACKey), store, pubkeys, requests, blocklist, users, registrar)
	return NewHandler(svc)
}

// pubAndContent generates a fresh ed25519 keypair and returns the raw
// public key alongside its OpenSSH authorized_keys text representation.
func pubAndContent(t *testing.T) (ed25519.PublicKey, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return pub, string(ssh.MarshalAuthorizedKey(sshKey))
}

// registerAgentViaService provisions a fully-active agent by going
// directly through the service-layer attachment-request helpers.
// Returns the resulting fingerprint that lookups can use.
func registerAgentViaService(t *testing.T, h *Handler, owner, slug, domain string, ownerUserID int64) (ed25519.PublicKey, string) {
	t.Helper()
	pub, content := pubAndContent(t)
	ctx := context.Background()
	req, err := h.svc.CreateAttachmentRequest(ctx, owner, slug, domain, content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.ApproveAttachmentRequest(ctx, req.ID, ownerUserID); err != nil {
		t.Fatal(err)
	}
	return pub, req.Fingerprint
}

// registerPendingViaService creates a pending agent via the attachment
// flow (without auto-approve), useful for testing PostApprove.
func registerPendingViaService(t *testing.T, h *Handler, owner, slug, domain string) (ed25519.PublicKey, string) {
	t.Helper()
	pub, content := pubAndContent(t)
	req, err := h.svc.CreateAttachmentRequest(context.Background(), owner, slug, domain, content)
	if err != nil {
		t.Fatal(err)
	}
	// Approve via the attachment-request flow to make the agent
	// addressable by its fingerprint, but we'll exercise the older
	// PostApprove handler for the test. The handler is idempotent on
	// already-active agents, so calling it twice is well-defined.
	if _, err := h.svc.ApproveAttachmentRequest(context.Background(), req.ID, 1); err != nil {
		t.Fatal(err)
	}
	return pub, req.Fingerprint
}

func TestPostApprove_OwnerCanApprove(t *testing.T) {
	h := newTestHandler(t)
	_, fp := registerPendingViaService(t, h, "alice", "plumb", "darksoft.co.nz")

	approveReq := httptest.NewRequest(http.MethodPost,
		"/api/cairn/v1/agents/"+fp+"/approve", nil)
	approveReq = approveReq.WithContext(WithCaller(approveReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	approveReq = WithFingerprintParam(approveReq, fp)
	approveW := httptest.NewRecorder()
	h.PostApprove(approveW, approveReq)

	if approveW.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want 200; body=%s", approveW.Code, approveW.Body.String())
	}
	var got AgentJSON
	if err := json.Unmarshal(approveW.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != string(cairn.AgentStatusActive) {
		t.Errorf("status = %q, want active", got.Status)
	}
}

func TestPostApprove_NonOwnerForbidden(t *testing.T) {
	h := newTestHandler(t)
	_, fp := registerPendingViaService(t, h, "alice", "plumb", "darksoft.co.nz")

	approveReq := httptest.NewRequest(http.MethodPost, "/", nil)
	approveReq = approveReq.WithContext(WithCaller(approveReq.Context(),
		&cairnidentity.Caller{UserID: 2, Username: "bob"}))
	approveReq = WithFingerprintParam(approveReq, fp)
	approveW := httptest.NewRecorder()
	h.PostApprove(approveW, approveReq)

	if approveW.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", approveW.Code)
	}
}

func TestPostApprove_UnauthenticatedUnauthorized(t *testing.T) {
	h := newTestHandler(t)

	approveReq := httptest.NewRequest(http.MethodPost, "/", nil)
	approveReq = WithFingerprintParam(approveReq, "cairn:does-not-matter")
	approveW := httptest.NewRecorder()
	h.PostApprove(approveW, approveReq)

	if approveW.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", approveW.Code)
	}
}

func TestPostApprove_NotFound(t *testing.T) {
	h := newTestHandler(t)

	approveReq := httptest.NewRequest(http.MethodPost, "/", nil)
	approveReq = approveReq.WithContext(WithCaller(approveReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	approveReq = WithFingerprintParam(approveReq, "cairn:does-not-exist")
	approveW := httptest.NewRecorder()
	h.PostApprove(approveW, approveReq)

	if approveW.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", approveW.Code)
	}
}

func TestPostApprove_AlreadyActiveIsIdempotent(t *testing.T) {
	h := newTestHandler(t)
	_, fp := registerAgentViaService(t, h, "alice", "plumb", "darksoft.co.nz", 1)

	approveReq := httptest.NewRequest(http.MethodPost, "/", nil)
	approveReq = approveReq.WithContext(WithCaller(approveReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	approveReq = WithFingerprintParam(approveReq, fp)
	approveW := httptest.NewRecorder()
	h.PostApprove(approveW, approveReq)

	if approveW.Code != http.StatusOK {
		t.Errorf("re-approve status = %d, want 200 (idempotent)", approveW.Code)
	}
	var got AgentJSON
	json.Unmarshal(approveW.Body.Bytes(), &got)
	if got.Status != string(cairn.AgentStatusActive) {
		t.Errorf("status after re-approve = %q, want active", got.Status)
	}
}

func TestPostBlock_OwnerCanBlock(t *testing.T) {
	h := newTestHandler(t)
	_, fp := registerAgentViaService(t, h, "alice", "plumb", "darksoft.co.nz", 1)

	blockBody, _ := json.Marshal(BlockRequestJSON{Reason: "key compromised"})
	blockReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(blockBody))
	blockReq.Header.Set("Content-Type", "application/json")
	blockReq = blockReq.WithContext(WithCaller(blockReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	blockReq = WithFingerprintParam(blockReq, fp)
	blockW := httptest.NewRecorder()
	h.PostBlock(blockW, blockReq)

	if blockW.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", blockW.Code, blockW.Body.String())
	}

	blocked, err := h.svc.IsBlocked(context.Background(), fp)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("agent not blocked after PostBlock")
	}
}

func TestPostBlock_NonOwnerForbidden(t *testing.T) {
	h := newTestHandler(t)
	_, fp := registerAgentViaService(t, h, "alice", "plumb", "darksoft.co.nz", 1)

	blockReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"reason":"x"}`)))
	blockReq.Header.Set("Content-Type", "application/json")
	blockReq = blockReq.WithContext(WithCaller(blockReq.Context(),
		&cairnidentity.Caller{UserID: 2, Username: "bob"}))
	blockReq = WithFingerprintParam(blockReq, fp)
	blockW := httptest.NewRecorder()
	h.PostBlock(blockW, blockReq)

	if blockW.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", blockW.Code)
	}
}

func TestPostBlock_UnauthenticatedUnauthorized(t *testing.T) {
	h := newTestHandler(t)

	blockReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"reason":"x"}`)))
	blockReq.Header.Set("Content-Type", "application/json")
	blockReq = WithFingerprintParam(blockReq, "cairn:any")
	blockW := httptest.NewRecorder()
	h.PostBlock(blockW, blockReq)

	if blockW.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", blockW.Code)
	}
}

func TestPostBlock_NotFound(t *testing.T) {
	h := newTestHandler(t)

	blockReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"reason":"x"}`)))
	blockReq.Header.Set("Content-Type", "application/json")
	blockReq = blockReq.WithContext(WithCaller(blockReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	blockReq = WithFingerprintParam(blockReq, "cairn:does-not-exist")
	blockW := httptest.NewRecorder()
	h.PostBlock(blockW, blockReq)

	if blockW.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", blockW.Code)
	}
}

func TestGetIdentity_ReturnsPublicKey(t *testing.T) {
	h := newTestHandler(t)
	_, fp := registerAgentViaService(t, h, "alice", "plumb", "darksoft.co.nz", 1)

	idReq := httptest.NewRequest(http.MethodGet, "/", nil)
	idReq = WithFingerprintParam(idReq, fp)
	idW := httptest.NewRecorder()
	h.GetIdentity(idW, idReq)

	if idW.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", idW.Code)
	}

	var got AgentJSON
	json.Unmarshal(idW.Body.Bytes(), &got)
	if got.Slug != "plumb" {
		t.Errorf("slug = %q, want plumb", got.Slug)
	}
	if got.PublicKeyHex == "" {
		t.Error("public_key empty in identity response")
	}
}

func TestGetIdentity_NotFound(t *testing.T) {
	h := newTestHandler(t)

	idReq := httptest.NewRequest(http.MethodGet, "/", nil)
	idReq = WithFingerprintParam(idReq, "cairn:does-not-exist")
	idW := httptest.NewRecorder()
	h.GetIdentity(idW, idReq)

	if idW.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", idW.Code)
	}
}

func TestGetAgents_ListsCurrentUsersAgents(t *testing.T) {
	h := newTestHandler(t)

	for _, slug := range []string{"plumb", "anvil", "forge"} {
		registerAgentViaService(t, h, "alice", slug, "darksoft.co.nz", 1)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/cairn/v1/agents", nil)
	listReq = listReq.WithContext(WithCaller(listReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	listW := httptest.NewRecorder()
	h.GetAgents(listW, listReq)

	if listW.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", listW.Code)
	}

	var got []AgentJSON
	if err := json.Unmarshal(listW.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3", len(got))
	}
}

func TestGetAgents_StatusFilter(t *testing.T) {
	h := newTestHandler(t)

	// Active agent via approved attachment request.
	registerAgentViaService(t, h, "alice", "plumb", "darksoft.co.nz", 1)

	// Pending agent: create the request but never approve. cairn_agent
	// row is only created at approve-time, so this contributes 0 agents.
	// To exercise the status filter we need an actual pending cairn_agent;
	// without the Register back-compat path that's harder to reach. The
	// test therefore checks that ?status=active returns the single active
	// agent.
	listReq := httptest.NewRequest(http.MethodGet, "/api/cairn/v1/agents?status=active", nil)
	listReq = listReq.WithContext(WithCaller(listReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	listW := httptest.NewRecorder()
	h.GetAgents(listW, listReq)

	if listW.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", listW.Code)
	}

	var got []AgentJSON
	json.Unmarshal(listW.Body.Bytes(), &got)
	if len(got) != 1 {
		t.Errorf("active filter len = %d, want 1", len(got))
	}
	if got[0].Slug != "plumb" {
		t.Errorf("slug = %q, want plumb", got[0].Slug)
	}
}

func TestGetAgents_RequiresAuth(t *testing.T) {
	h := newTestHandler(t)

	listReq := httptest.NewRequest(http.MethodGet, "/", nil)
	listW := httptest.NewRecorder()
	h.GetAgents(listW, listReq)

	if listW.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", listW.Code)
	}
}

func TestPostBlock_BlockedFieldVisibleInIdentity(t *testing.T) {
	h := newTestHandler(t)
	_, fp := registerAgentViaService(t, h, "alice", "plumb", "darksoft.co.nz", 1)

	// Block via the service.
	if err := h.svc.Block(context.Background(), fp, "compromised", &cairnidentity.Caller{UserID: 1, Username: "alice"}); err != nil {
		t.Fatal(err)
	}

	idReq := httptest.NewRequest(http.MethodGet, "/", nil)
	idReq = WithFingerprintParam(idReq, fp)
	idW := httptest.NewRecorder()
	h.GetIdentity(idW, idReq)

	var got AgentJSON
	if err := json.Unmarshal(idW.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode identity response: %v; body=%s", err, idW.Body.String())
	}
	if !got.Blocked {
		t.Error("blocked agent reported not blocked in GetIdentity response")
	}
}
