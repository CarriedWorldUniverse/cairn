// Cairn-specific code; AGPLv3. See LICENSING.md.

package v1

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// requestIDKey is the context key for the URL :id path param on
// attachment-request endpoints.
type requestIDKey struct{}

// WithRequestIDParam attaches the URL :id param (parsed int64) to the
// request context. Test injection helper; production wiring uses the
// router's path-param extraction wrapped in withReqID middleware.
func WithRequestIDParam(r *http.Request, id int64) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
}

// requestIDFromCtx returns the :id URL param. Returns 0 when absent.
func requestIDFromCtx(ctx context.Context) int64 {
	id, _ := ctx.Value(requestIDKey{}).(int64)
	return id
}

// writeAttachmentRequest writes an AttachmentRequest as JSON with the
// given HTTP status code.
func writeAttachmentRequest(w http.ResponseWriter, code int, req *cairn.AttachmentRequest) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(attachmentRequestJSON(req))
}

func attachmentRequestJSON(req *cairn.AttachmentRequest) AttachmentRequestJSON {
	out := AttachmentRequestJSON{
		ID:            req.ID,
		OwnerUsername: req.OwnerUsername,
		Slug:          req.Slug,
		Domain:        req.Domain,
		Fingerprint:   req.Fingerprint,
		Status:        string(req.Status),
		RequestedAt:   time.Unix(req.RequestedUnix, 0).UTC().Format(time.RFC3339),
	}
	if req.DecidedUnix > 0 {
		out.DecidedAt = time.Unix(req.DecidedUnix, 0).UTC().Format(time.RFC3339)
	}
	return out
}

// PostAttachmentRequest handles
// POST /api/cairn/v1/agents/attachment-requests.
//
// Anonymous-callable: an agent submits its keypair attestation along
// with the proposed (owner, slug, domain). The request lands in the
// pending state and the owner approves out-of-band.
func (h *Handler) PostAttachmentRequest(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var in AttachmentRequestCreateJSON
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not decode JSON body")
		return
	}
	if in.OwnerUsername == "" || in.Slug == "" || in.Domain == "" || in.PubkeyContent == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "owner_username, slug, domain, and pubkey_content are required")
		return
	}

	req, err := h.svc.CreateAttachmentRequest(r.Context(), in.OwnerUsername, in.Slug, in.Domain, in.PubkeyContent)
	switch {
	case err == nil:
		// fall through
	case errors.Is(err, cairnidentity.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "owner_not_found", "")
		return
	case errors.Is(err, cairnidentity.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
		return
	default:
		log.Printf("cairn api v1: PostAttachmentRequest: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	writeAttachmentRequest(w, http.StatusCreated, req)
}

// parseStatusFilter parses the ?status= query param. Empty string is
// allowed and indicates "no filter". Unknown values yield ok=false so
// the handler can respond with 400 invalid_status.
func parseStatusFilter(raw string) (cairn.AttachmentRequestStatus, bool) {
	switch raw {
	case "":
		return "", true
	case string(cairn.AttachmentRequestPending),
		string(cairn.AttachmentRequestApproved),
		string(cairn.AttachmentRequestRejected):
		return cairn.AttachmentRequestStatus(raw), true
	default:
		return "", false
	}
}

// GetAttachmentRequests handles
// GET /api/cairn/v1/agents/attachment-requests.
//
// Auth required. Non-admin callers see only requests where
// owner_username matches their username. Site admins see every request.
// Optional ?status=pending|approved|rejected filter; default is pending
// (to match the most common use case — "what needs my decision now?").
func (h *Handler) GetAttachmentRequests(w http.ResponseWriter, r *http.Request) {
	caller := callerFromCtx(r.Context())
	if caller == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	raw := r.URL.Query().Get("status")
	if raw == "" {
		// Default to pending — the most common UX query.
		raw = string(cairn.AttachmentRequestPending)
	}
	status, ok := parseStatusFilter(raw)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_status", "status must be one of: pending, approved, rejected")
		return
	}

	var (
		rows []*cairn.AttachmentRequest
		err  error
	)
	if caller.IsAdmin {
		rows, err = h.svc.ListAllAttachmentRequests(r.Context(), status)
	} else {
		rows, err = h.svc.ListForOwner(r.Context(), caller.Username, status)
	}
	if err != nil {
		log.Printf("cairn api v1: GetAttachmentRequests: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	out := make([]AttachmentRequestJSON, 0, len(rows))
	for _, req := range rows {
		out = append(out, attachmentRequestJSON(req))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// GetMyPendingAttachmentRequests handles
// GET /api/cairn/v1/users/me/pending-attachment-requests.
//
// Auth required. Convenience wrapper around ListPendingForOwner scoped
// to the caller. Always status=pending, always scoped to the caller —
// no admin special-case. Returns the same shape as
// GetAttachmentRequests.
func (h *Handler) GetMyPendingAttachmentRequests(w http.ResponseWriter, r *http.Request) {
	caller := callerFromCtx(r.Context())
	if caller == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	rows, err := h.svc.ListPendingForOwner(r.Context(), caller.Username)
	if err != nil {
		log.Printf("cairn api v1: GetMyPendingAttachmentRequests: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	out := make([]AttachmentRequestJSON, 0, len(rows))
	for _, req := range rows {
		out = append(out, attachmentRequestJSON(req))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

// authorizeRequestDecision loads the attachment request and confirms
// the caller is permitted to decide it (owner or site admin).
//
// On a 4xx outcome the handler has already written the response; the
// returned bool reports whether the handler should continue.
func (h *Handler) authorizeRequestDecision(w http.ResponseWriter, r *http.Request, caller *cairnidentity.Caller, id int64) (*cairn.AttachmentRequest, bool) {
	req, err := h.svc.GetAttachmentRequest(r.Context(), id)
	if err != nil {
		if errors.Is(err, cairnidentity.ErrAttachmentRequestNotFound) {
			writeError(w, http.StatusNotFound, "attachment_request_not_found", "")
			return nil, false
		}
		log.Printf("cairn api v1: authorizeRequestDecision: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return nil, false
	}
	if !caller.IsAdmin && caller.Username != req.OwnerUsername {
		writeError(w, http.StatusForbidden, "forbidden", "only the owner or a site admin may decide this request")
		return nil, false
	}
	return req, true
}

// PostApproveAttachmentRequest handles
// POST /api/cairn/v1/agents/attachment-requests/{id}/approve.
//
// Auth required. Caller must be the request's owner or a site admin.
// Returns the resulting AgentJSON (HTTP 200) on success.
func (h *Handler) PostApproveAttachmentRequest(w http.ResponseWriter, r *http.Request) {
	caller := callerFromCtx(r.Context())
	if caller == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}
	id := requestIDFromCtx(r.Context())
	if id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "request id must be a positive integer")
		return
	}
	req, ok := h.authorizeRequestDecision(w, r, caller, id)
	if !ok {
		return
	}

	agent, err := h.svc.ApproveAttachmentRequest(r.Context(), req.ID, caller.UserID)
	switch {
	case err == nil:
		// fall through
	case errors.Is(err, cairnidentity.ErrAlreadyDecided):
		writeError(w, http.StatusConflict, "already_decided", "")
		return
	case errors.Is(err, cairnidentity.ErrPubkeyAlreadyClaimed):
		writeError(w, http.StatusConflict, "pubkey_already_claimed", "this public key is already bound to another agent")
		return
	case errors.Is(err, cairnidentity.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "owner_not_found", "")
		return
	default:
		log.Printf("cairn api v1: PostApproveAttachmentRequest: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	blocked, _ := h.svc.IsBlocked(r.Context(), req.Fingerprint)
	ownerName, _ := h.svc.UsernameByID(r.Context(), agent.UserID)
	pubHex, _ := h.pubkeyHexForFingerprint(r.Context(), req.Fingerprint)
	writeAgent(w, http.StatusOK, agent, ownerName, blocked, req.Fingerprint, pubHex)
}

// PostRejectAttachmentRequest handles
// POST /api/cairn/v1/agents/attachment-requests/{id}/reject.
//
// Auth required. Caller must be the request's owner or a site admin.
// Returns 204 No Content on success.
func (h *Handler) PostRejectAttachmentRequest(w http.ResponseWriter, r *http.Request) {
	caller := callerFromCtx(r.Context())
	if caller == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}
	id := requestIDFromCtx(r.Context())
	if id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_id", "request id must be a positive integer")
		return
	}
	req, ok := h.authorizeRequestDecision(w, r, caller, id)
	if !ok {
		return
	}

	err := h.svc.RejectAttachmentRequest(r.Context(), req.ID, caller.UserID)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, cairnidentity.ErrAlreadyDecided):
		writeError(w, http.StatusConflict, "already_decided", "")
	default:
		log.Printf("cairn api v1: PostRejectAttachmentRequest: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
	}
}

// parseRequestIDParam parses a chi URL string into an int64. Returns
// 0 on parse failure so the handler treats it as "invalid id".
func parseRequestIDParam(s string) int64 {
	if s == "" {
		return 0
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}
