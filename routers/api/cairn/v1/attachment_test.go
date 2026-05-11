// Cairn-specific code; AGPLv3. See LICENSING.md.

package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

func doCreate(t *testing.T, h *Handler, body []byte, caller *cairnidentity.Caller) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/agents/attachment-requests", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if caller != nil {
		req = req.WithContext(WithCaller(req.Context(), caller))
	}
	w := httptest.NewRecorder()
	h.PostAttachmentRequest(w, req)
	return w
}

func TestPostAttachmentRequest_HappyPath(t *testing.T) {
	h := newTestHandler(t)
	_, content := pubAndContent(t)

	body, _ := json.Marshal(AttachmentRequestCreateJSON{
		OwnerUsername: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PubkeyContent: content,
	})
	w := doCreate(t, h, body, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var got AttachmentRequestJSON
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID == 0 {
		t.Error("missing id in response")
	}
	if got.Status != string(cairn.AttachmentRequestPending) {
		t.Errorf("status = %q, want pending", got.Status)
	}
	if got.Fingerprint == "" {
		t.Error("missing fingerprint in response")
	}
	if got.OwnerUsername != "alice" || got.Slug != "plumb" || got.Domain != "darksoft.co.nz" {
		t.Errorf("response fields mismatch: %+v", got)
	}
	if got.RequestedAt == "" {
		t.Error("requested_at empty")
	}
}

func TestPostAttachmentRequest_MissingFields(t *testing.T) {
	h := newTestHandler(t)

	body, _ := json.Marshal(AttachmentRequestCreateJSON{
		OwnerUsername: "alice",
		// missing slug/domain/pubkey
	})
	w := doCreate(t, h, body, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestPostAttachmentRequest_UnknownOwner(t *testing.T) {
	h := newTestHandler(t)
	_, content := pubAndContent(t)

	body, _ := json.Marshal(AttachmentRequestCreateJSON{
		OwnerUsername: "no-such-user",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PubkeyContent: content,
	})
	w := doCreate(t, h, body, nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body=%s", w.Code, w.Body.String())
	}
}

func TestPostAttachmentRequest_MalformedPubkey(t *testing.T) {
	h := newTestHandler(t)

	body, _ := json.Marshal(AttachmentRequestCreateJSON{
		OwnerUsername: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PubkeyContent: "not-a-real-ssh-key",
	})
	w := doCreate(t, h, body, nil)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	var er ErrorJSON
	json.Unmarshal(w.Body.Bytes(), &er)
	if er.Error != "invalid_input" {
		t.Errorf("error code = %q, want invalid_input", er.Error)
	}
}

// createReqForTest creates an attachment request directly via the
// service. Useful when a test needs an existing pending request to
// approve/reject.
func createReqForTest(t *testing.T, h *Handler, owner, slug, domain string) *cairn.AttachmentRequest {
	t.Helper()
	_, content := pubAndContent(t)
	req, err := h.svc.CreateAttachmentRequest(context.Background(), owner, slug, domain, content)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestGetAttachmentRequests_OwnerScoped(t *testing.T) {
	h := newTestHandler(t)
	// Two pending for alice, one for bob.
	createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")
	createReqForTest(t, h, "alice", "anvil", "darksoft.co.nz")
	createReqForTest(t, h, "bob", "forge", "darksoft.co.nz")

	req := httptest.NewRequest(http.MethodGet, "/api/cairn/v1/agents/attachment-requests", nil)
	req = req.WithContext(WithCaller(req.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w := httptest.NewRecorder()
	h.GetAttachmentRequests(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got []AttachmentRequestJSON
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 2 {
		t.Errorf("len = %d, want 2 (alice's pending)", len(got))
	}
	for _, r := range got {
		if r.OwnerUsername != "alice" {
			t.Errorf("unexpected owner %q in alice's list", r.OwnerUsername)
		}
	}
}

func TestGetAttachmentRequests_AdminSeesAll(t *testing.T) {
	h := newTestHandler(t)
	createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")
	createReqForTest(t, h, "bob", "forge", "darksoft.co.nz")

	req := httptest.NewRequest(http.MethodGet, "/api/cairn/v1/agents/attachment-requests", nil)
	req = req.WithContext(WithCaller(req.Context(),
		&cairnidentity.Caller{UserID: 3, Username: "admin", IsAdmin: true}))
	w := httptest.NewRecorder()
	h.GetAttachmentRequests(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []AttachmentRequestJSON
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 2 {
		t.Errorf("admin list len = %d, want 2", len(got))
	}
}

func TestGetAttachmentRequests_StatusFilter(t *testing.T) {
	h := newTestHandler(t)
	ctx := context.Background()

	r1 := createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")
	if _, err := h.svc.ApproveAttachmentRequest(ctx, r1.ID, 1); err != nil {
		t.Fatal(err)
	}
	createReqForTest(t, h, "alice", "anvil", "darksoft.co.nz")

	req := httptest.NewRequest(http.MethodGet, "/api/cairn/v1/agents/attachment-requests?status=approved", nil)
	req = req.WithContext(WithCaller(req.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w := httptest.NewRecorder()
	h.GetAttachmentRequests(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []AttachmentRequestJSON
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 1 || got[0].Status != string(cairn.AttachmentRequestApproved) {
		t.Errorf("approved filter got = %+v, want 1 approved", got)
	}
}

func TestGetAttachmentRequests_InvalidStatus(t *testing.T) {
	h := newTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/cairn/v1/agents/attachment-requests?status=nonsense", nil)
	req = req.WithContext(WithCaller(req.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w := httptest.NewRecorder()
	h.GetAttachmentRequests(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestGetAttachmentRequests_RequiresAuth(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/cairn/v1/agents/attachment-requests", nil)
	w := httptest.NewRecorder()
	h.GetAttachmentRequests(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestGetMyPendingAttachmentRequests(t *testing.T) {
	h := newTestHandler(t)
	createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")
	createReqForTest(t, h, "bob", "forge", "darksoft.co.nz")

	req := httptest.NewRequest(http.MethodGet, "/api/cairn/v1/users/me/pending-attachment-requests", nil)
	req = req.WithContext(WithCaller(req.Context(),
		&cairnidentity.Caller{UserID: 1, Username: "alice"}))
	w := httptest.NewRecorder()
	h.GetMyPendingAttachmentRequests(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []AttachmentRequestJSON
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got) != 1 {
		t.Errorf("len = %d, want 1", len(got))
	}
}

func doApprove(t *testing.T, h *Handler, id int64, caller *cairnidentity.Caller) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/cairn/v1/agents/attachment-requests/"+strconv.FormatInt(id, 10)+"/approve", nil)
	if caller != nil {
		req = req.WithContext(WithCaller(req.Context(), caller))
	}
	req = WithRequestIDParam(req, id)
	w := httptest.NewRecorder()
	h.PostApproveAttachmentRequest(w, req)
	return w
}

func TestPostApproveAttachmentRequest_OwnerApproves(t *testing.T) {
	h := newTestHandler(t)
	r := createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")

	w := doApprove(t, h, r.ID, &cairnidentity.Caller{UserID: 1, Username: "alice"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var got AgentJSON
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.Status != string(cairn.AgentStatusActive) {
		t.Errorf("agent status = %q, want active", got.Status)
	}
	if got.Fingerprint != r.Fingerprint {
		t.Errorf("fingerprint = %q, want %q", got.Fingerprint, r.Fingerprint)
	}
}

func TestPostApproveAttachmentRequest_AdminApproves(t *testing.T) {
	h := newTestHandler(t)
	r := createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")

	w := doApprove(t, h, r.ID, &cairnidentity.Caller{UserID: 3, Username: "admin", IsAdmin: true})
	if w.Code != http.StatusOK {
		t.Fatalf("admin approve status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestPostApproveAttachmentRequest_NonOwnerForbidden(t *testing.T) {
	h := newTestHandler(t)
	r := createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")

	w := doApprove(t, h, r.ID, &cairnidentity.Caller{UserID: 2, Username: "bob"})
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestPostApproveAttachmentRequest_Unauthenticated(t *testing.T) {
	h := newTestHandler(t)
	r := createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")
	w := doApprove(t, h, r.ID, nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestPostApproveAttachmentRequest_AlreadyDecided(t *testing.T) {
	h := newTestHandler(t)
	r := createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")

	caller := &cairnidentity.Caller{UserID: 1, Username: "alice"}
	if w := doApprove(t, h, r.ID, caller); w.Code != http.StatusOK {
		t.Fatalf("first approve status = %d", w.Code)
	}
	w := doApprove(t, h, r.ID, caller)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (already decided)", w.Code)
	}
}

func TestPostApproveAttachmentRequest_NotFound(t *testing.T) {
	h := newTestHandler(t)
	w := doApprove(t, h, 99999, &cairnidentity.Caller{UserID: 1, Username: "alice"})
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func doReject(t *testing.T, h *Handler, id int64, caller *cairnidentity.Caller) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/cairn/v1/agents/attachment-requests/"+strconv.FormatInt(id, 10)+"/reject", nil)
	if caller != nil {
		req = req.WithContext(WithCaller(req.Context(), caller))
	}
	req = WithRequestIDParam(req, id)
	w := httptest.NewRecorder()
	h.PostRejectAttachmentRequest(w, req)
	return w
}

func TestPostRejectAttachmentRequest_OwnerRejects(t *testing.T) {
	h := newTestHandler(t)
	r := createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")

	w := doReject(t, h, r.ID, &cairnidentity.Caller{UserID: 1, Username: "alice"})
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
}

func TestPostRejectAttachmentRequest_AlreadyDecided(t *testing.T) {
	h := newTestHandler(t)
	r := createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")
	caller := &cairnidentity.Caller{UserID: 1, Username: "alice"}

	if w := doReject(t, h, r.ID, caller); w.Code != http.StatusNoContent {
		t.Fatalf("first reject status = %d", w.Code)
	}
	w := doReject(t, h, r.ID, caller)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 (already decided)", w.Code)
	}
}

func TestPostRejectAttachmentRequest_NonOwnerForbidden(t *testing.T) {
	h := newTestHandler(t)
	r := createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")

	w := doReject(t, h, r.ID, &cairnidentity.Caller{UserID: 2, Username: "bob"})
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

func TestPostRejectAttachmentRequest_Unauthenticated(t *testing.T) {
	h := newTestHandler(t)
	r := createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")
	w := doReject(t, h, r.ID, nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestPostRejectAttachmentRequest_AdminRejects(t *testing.T) {
	h := newTestHandler(t)
	r := createReqForTest(t, h, "alice", "plumb", "darksoft.co.nz")

	w := doReject(t, h, r.ID, &cairnidentity.Caller{UserID: 3, Username: "admin", IsAdmin: true})
	if w.Code != http.StatusNoContent {
		t.Fatalf("admin reject status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
	got, err := h.svc.GetAttachmentRequest(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("GetAttachmentRequest after admin reject: %v", err)
	}
	if got.Status != cairn.AttachmentRequestRejected {
		t.Errorf("status = %q, want rejected", got.Status)
	}
}

func TestParseRequestIDParam(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"abc", 0},
		{"-1", 0},
		{"0", 0},
		{"1", 1},
		{"42", 42},
	}
	for _, tc := range cases {
		if got := parseRequestIDParam(tc.in); got != tc.want {
			t.Errorf("parseRequestIDParam(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
