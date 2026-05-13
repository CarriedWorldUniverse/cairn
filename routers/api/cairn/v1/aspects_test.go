// Cairn-specific code; AGPLv3. See LICENSING.md.

package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// fakeProvisioner records MintAspect calls and lets tests dial in the
// response (success, name-taken, internal-error).
type fakeProvisioner struct {
	mu       sync.Mutex
	calls    []fakeMintCall
	nextErr  error
	nextResp MintedAspect
}

type fakeMintCall struct {
	parent      AspectParent
	username    string
	fullName    string
	description string
	scopes      string
}

func (f *fakeProvisioner) MintAspect(_ context.Context, parent AspectParent, username, fullName, description, scopes string) (MintedAspect, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeMintCall{
		parent:      parent,
		username:    username,
		fullName:    fullName,
		description: description,
		scopes:      scopes,
	})
	if f.nextErr != nil {
		return MintedAspect{}, f.nextErr
	}
	resp := f.nextResp
	if resp.UserID == 0 {
		// Deterministic default — UserID derived from call count so
		// successive mints in one test don't collide.
		resp.UserID = int64(100 + len(f.calls))
	}
	if resp.Username == "" {
		resp.Username = username
	}
	if resp.APIToken == "" {
		resp.APIToken = "tok-" + username
	}
	return resp, nil
}

// doMint sends a POST to the aspect-mint endpoint and returns the
// recorded ResponseRecorder. body is JSON-marshalled by the caller;
// caller==nil for anonymous.
func doMint(t *testing.T, h *Handler, body []byte, caller *cairnidentity.Caller) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/cairn/v1/users/me/aspects", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if caller != nil {
		req = req.WithContext(WithCaller(req.Context(), caller))
	}
	w := httptest.NewRecorder()
	h.PostMintAspect(w, req)
	return w
}

func TestPostMintAspect_HappyPath(t *testing.T) {
	prov := &fakeProvisioner{}
	h := newTestHandler(t).WithAspectProvisioner(prov)
	body, _ := json.Marshal(MintAspectRequestJSON{Name: "keel", Description: "Frame agent"})

	w := doMint(t, h, body, &cairnidentity.Caller{UserID: 1, Username: "alice"})
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var got MintAspectResponseJSON
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Username != "alice-keel" {
		t.Errorf("Username = %q, want alice-keel", got.Username)
	}
	if got.FullName != "keel" {
		t.Errorf("FullName = %q, want keel", got.FullName)
	}
	if got.ParentUserID != 1 || got.ParentUsername != "alice" {
		t.Errorf("parent linkage missing: %+v", got)
	}
	if got.APIToken == "" {
		t.Error("APIToken should be returned on mint")
	}
	if !strings.Contains(got.TokenScopes, "write:issue") {
		t.Errorf("TokenScopes missing write:issue: %q", got.TokenScopes)
	}

	if len(prov.calls) != 1 {
		t.Fatalf("expected 1 mint call, got %d", len(prov.calls))
	}
	c := prov.calls[0]
	if c.parent.ID != 1 || c.parent.Username != "alice" {
		t.Errorf("provisioner saw wrong parent: %+v", c.parent)
	}
	if c.username != "alice-keel" {
		t.Errorf("provisioner saw wrong username: %q", c.username)
	}
	if c.fullName != "keel" {
		t.Errorf("provisioner saw wrong fullname: %q", c.fullName)
	}
	if c.description != "Frame agent" {
		t.Errorf("description not passed through: %q", c.description)
	}
}

func TestPostMintAspect_UnauthenticatedRejected(t *testing.T) {
	prov := &fakeProvisioner{}
	h := newTestHandler(t).WithAspectProvisioner(prov)
	body, _ := json.Marshal(MintAspectRequestJSON{Name: "keel"})

	w := doMint(t, h, body, nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if len(prov.calls) != 0 {
		t.Error("provisioner must not be called for anonymous requests")
	}
}

func TestPostMintAspect_ProvisionerNotWiredYields503(t *testing.T) {
	// Default Handler (NewHandler) has no provisioner — surface should
	// fail closed.
	h := newTestHandler(t)
	body, _ := json.Marshal(MintAspectRequestJSON{Name: "keel"})

	w := doMint(t, h, body, &cairnidentity.Caller{UserID: 1, Username: "alice"})
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

func TestPostMintAspect_NameValidation(t *testing.T) {
	prov := &fakeProvisioner{}
	h := newTestHandler(t).WithAspectProvisioner(prov)
	caller := &cairnidentity.Caller{UserID: 1, Username: "alice"}

	bad := []string{
		"",            // empty
		"Keel",        // uppercase
		"123keel",     // leading digit
		"-keel",       // leading hyphen
		"keel_cli",    // underscore
		"keel keel",   // space
		strings.Repeat("x", 33), // too long
	}
	for _, name := range bad {
		body, _ := json.Marshal(MintAspectRequestJSON{Name: name})
		w := doMint(t, h, body, caller)
		if w.Code != http.StatusBadRequest {
			t.Errorf("name %q: status = %d, want 400", name, w.Code)
		}
	}
	if len(prov.calls) != 0 {
		t.Error("validation must reject before reaching provisioner")
	}
}

func TestPostMintAspect_DescriptionTooLongRejected(t *testing.T) {
	prov := &fakeProvisioner{}
	h := newTestHandler(t).WithAspectProvisioner(prov)
	body, _ := json.Marshal(MintAspectRequestJSON{
		Name:        "keel",
		Description: strings.Repeat("x", 256),
	})

	w := doMint(t, h, body, &cairnidentity.Caller{UserID: 1, Username: "alice"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if len(prov.calls) != 0 {
		t.Error("validation must reject before reaching provisioner")
	}
}

func TestPostMintAspect_NameTakenReturns409(t *testing.T) {
	prov := &fakeProvisioner{nextErr: ErrAspectNameTaken}
	h := newTestHandler(t).WithAspectProvisioner(prov)
	body, _ := json.Marshal(MintAspectRequestJSON{Name: "keel"})

	w := doMint(t, h, body, &cairnidentity.Caller{UserID: 1, Username: "alice"})
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
}

func TestPostMintAspect_InternalErrorReturns500(t *testing.T) {
	prov := &fakeProvisioner{nextErr: errors.New("DB exploded")}
	h := newTestHandler(t).WithAspectProvisioner(prov)
	body, _ := json.Marshal(MintAspectRequestJSON{Name: "keel"})

	w := doMint(t, h, body, &cairnidentity.Caller{UserID: 1, Username: "alice"})
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	// Don't leak the underlying error message to the caller.
	if strings.Contains(w.Body.String(), "DB exploded") {
		t.Error("internal error message leaked to API response")
	}
}

func TestPostMintAspect_InvalidJSONReturns400(t *testing.T) {
	prov := &fakeProvisioner{}
	h := newTestHandler(t).WithAspectProvisioner(prov)

	w := doMint(t, h, []byte("{not-json"), &cairnidentity.Caller{UserID: 1, Username: "alice"})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
