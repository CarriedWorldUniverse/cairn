package v1

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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
// Mirrors the implementations in services/cairn/identity and
// services/cairn/hook (cannot import either's test package, hence the
// duplication).
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
		},
	}
	registrar := newFakeRegistrar()
	svc := cairnidentity.NewAgentService([]byte(testHMACKey), store, pubkeys, requests, blocklist, users, registrar)
	return NewHandler(svc)
}

func TestPostAgents_AutoApproveWhenAuthedAsOwner(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKeyHex:  hex.EncodeToString(pub),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(WithCaller(req.Context(), &cairnidentity.Caller{UserID: 1, Username: "alice"}))

	w := httptest.NewRecorder()
	h.PostAgents(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	var got AgentJSON
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != string(cairn.AgentStatusActive) {
		t.Errorf("status = %q, want active", got.Status)
	}
	if got.Fingerprint == "" {
		t.Error("fingerprint missing")
	}
}

func TestPostAgents_PendingWhenAnonymous(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKeyHex:  hex.EncodeToString(pub),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.PostAgents(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}

	var got AgentJSON
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Status != string(cairn.AgentStatusPending) {
		t.Errorf("status = %q, want pending", got.Status)
	}
}

func TestPostAgents_RejectsUnknownOwner(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "nobody",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKeyHex:  hex.EncodeToString(pub),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.PostAgents(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestPostAgents_RejectsDuplicate(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKeyHex:  hex.EncodeToString(pub),
	})

	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(WithCaller(req.Context(), &cairnidentity.Caller{UserID: 1, Username: "alice"}))
		w := httptest.NewRecorder()
		h.PostAgents(w, req)
		return w.Code
	}

	if code := send(); code != http.StatusCreated {
		t.Fatalf("first request status = %d, want 201", code)
	}
	if code := send(); code != http.StatusConflict {
		t.Errorf("duplicate request status = %d, want 409", code)
	}
}

func TestPostAgents_RejectsMalformedHex(t *testing.T) {
	h := newTestHandler(t)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKeyHex:  "not-hex-z",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.PostAgents(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestPostAgents_RejectsWrongPubkeyLength(t *testing.T) {
	h := newTestHandler(t)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKeyHex:  hex.EncodeToString([]byte{1, 2, 3, 4}),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.PostAgents(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestPostApprove_OwnerCanApprove(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Step 1: register anonymously → pending.
	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.PostAgents(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("register status = %d", w.Code)
	}
	var pending AgentJSON
	json.Unmarshal(w.Body.Bytes(), &pending)

	// Step 2: owner approves.
	approveReq := httptest.NewRequest(http.MethodPost,
		"/api/cairn/v1/agents/"+pending.Fingerprint+"/approve", nil)
	approveReq = approveReq.WithContext(WithCaller(approveReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	approveReq = WithFingerprintParam(approveReq, pending.Fingerprint)
	approveW := httptest.NewRecorder()
	h.PostApprove(approveW, approveReq)

	if approveW.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want 200; body=%s", approveW.Code, approveW.Body.String())
	}
	var got AgentJSON
	json.Unmarshal(approveW.Body.Bytes(), &got)
	if got.Status != string(cairn.AgentStatusActive) {
		t.Errorf("status = %q, want active", got.Status)
	}
}

func TestPostApprove_NonOwnerForbidden(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.PostAgents(w, req)
	var pending AgentJSON
	json.Unmarshal(w.Body.Bytes(), &pending)

	approveReq := httptest.NewRequest(http.MethodPost, "/", nil)
	approveReq = approveReq.WithContext(WithCaller(approveReq.Context(),
		&cairnidentity.Caller{UserID: 2, Username: "bob"}))
	approveReq = WithFingerprintParam(approveReq, pending.Fingerprint)
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
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Auto-approve as owner → already active.
	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(WithCaller(req.Context(), &cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w := httptest.NewRecorder()
	h.PostAgents(w, req)
	var a AgentJSON
	json.Unmarshal(w.Body.Bytes(), &a)
	if a.Status != string(cairn.AgentStatusActive) {
		t.Fatalf("setup: expected active, got %q", a.Status)
	}

	// Re-approve. Should succeed (200) and remain active.
	approveReq := httptest.NewRequest(http.MethodPost, "/", nil)
	approveReq = approveReq.WithContext(WithCaller(approveReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	approveReq = WithFingerprintParam(approveReq, a.Fingerprint)
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
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(WithCaller(req.Context(), &cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w := httptest.NewRecorder()
	h.PostAgents(w, req)
	var a AgentJSON
	json.Unmarshal(w.Body.Bytes(), &a)

	blockBody, _ := json.Marshal(BlockRequestJSON{Reason: "key compromised"})
	blockReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(blockBody))
	blockReq.Header.Set("Content-Type", "application/json")
	blockReq = blockReq.WithContext(WithCaller(blockReq.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	blockReq = WithFingerprintParam(blockReq, a.Fingerprint)
	blockW := httptest.NewRecorder()
	h.PostBlock(blockW, blockReq)

	if blockW.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", blockW.Code, blockW.Body.String())
	}

	blocked, err := h.svc.IsBlocked(context.Background(), a.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("agent not blocked after PostBlock")
	}
}

func TestPostBlock_NonOwnerForbidden(t *testing.T) {
	h := newTestHandler(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(WithCaller(req.Context(), &cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w := httptest.NewRecorder()
	h.PostAgents(w, req)
	var a AgentJSON
	json.Unmarshal(w.Body.Bytes(), &a)

	blockReq := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"reason":"x"}`)))
	blockReq.Header.Set("Content-Type", "application/json")
	blockReq = blockReq.WithContext(WithCaller(blockReq.Context(),
		&cairnidentity.Caller{UserID: 2, Username: "bob"}))
	blockReq = WithFingerprintParam(blockReq, a.Fingerprint)
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
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(WithCaller(req.Context(), &cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w := httptest.NewRecorder()
	h.PostAgents(w, req)
	var a AgentJSON
	json.Unmarshal(w.Body.Bytes(), &a)

	idReq := httptest.NewRequest(http.MethodGet, "/", nil)
	idReq = WithFingerprintParam(idReq, a.Fingerprint)
	idW := httptest.NewRecorder()
	h.GetIdentity(idW, idReq)

	if idW.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", idW.Code)
	}

	var got AgentJSON
	json.Unmarshal(idW.Body.Bytes(), &got)
	if got.PublicKeyHex != hex.EncodeToString(pub) {
		t.Errorf("public_key = %q, want %q", got.PublicKeyHex, hex.EncodeToString(pub))
	}
	if got.Slug != "plumb" {
		t.Errorf("slug = %q, want plumb", got.Slug)
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
		pub, _, _ := ed25519.GenerateKey(rand.Reader)
		body, _ := json.Marshal(RegisterRequestJSON{
			ProposedOwner: "alice", Slug: slug, Domain: "darksoft.co.nz",
			PublicKeyHex: hex.EncodeToString(pub),
		})
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(WithCaller(req.Context(),
			&cairnidentity.Caller{UserID: 1, Username: "alice"}))
		w := httptest.NewRecorder()
		h.PostAgents(w, req)
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

	pub1, _, _ := ed25519.GenerateKey(rand.Reader)
	body1, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub1),
	})
	req1 := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	req1 = req1.WithContext(WithCaller(req1.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w1 := httptest.NewRecorder()
	h.PostAgents(w1, req1)

	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	body2, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "anvil", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub2),
	})
	req2 := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	h.PostAgents(w2, req2)

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
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Auto-approved registration.
	body, _ := json.Marshal(RegisterRequestJSON{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz",
		PublicKeyHex: hex.EncodeToString(pub),
	})
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(WithCaller(req.Context(), &cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w := httptest.NewRecorder()
	h.PostAgents(w, req)
	var a AgentJSON
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatalf("decode register response: %v; body=%s", err, w.Body.String())
	}

	// Verify the registered agent is not blocked.
	if a.Blocked {
		t.Error("freshly registered agent reported blocked")
	}

	// Block via the service (simulating a PostBlock call).
	if err := h.svc.Block(context.Background(), a.Fingerprint, "compromised", &cairnidentity.Caller{UserID: 1, Username: "alice"}); err != nil {
		t.Fatal(err)
	}

	// GET /identity should now show blocked=true.
	idReq := httptest.NewRequest(http.MethodGet, "/", nil)
	idReq = WithFingerprintParam(idReq, a.Fingerprint)
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
