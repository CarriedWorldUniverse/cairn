package v1

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	eng := cairntest.NewEngine(t)
	store := cairnidentity.NewXormAgentStore(eng)
	blocklist := cairnidentity.NewXormBlocklistStore(eng)
	users := &fakeUserResolver{
		usernameToID: map[string]int64{
			"alice": 1,
			"bob":     2,
		},
	}
	svc := cairnidentity.NewAgentService([]byte(testHMACKey), store, blocklist, users)
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
