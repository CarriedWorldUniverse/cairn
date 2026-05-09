// Cairn-specific code; AGPLv3. See LICENSING.md.

package v1

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// maxRequestBodyBytes bounds the size of API request bodies. Protects
// the anonymous POST /agents endpoint from DoS via huge payload.
// Cairn's largest legitimate request is registration: ~200 bytes
// (proposed_owner + slug + domain + 64-char hex pubkey).
const maxRequestBodyBytes = 4096

// Handler is the HTTP handler set for /api/cairn/v1.
type Handler struct {
	svc *cairnidentity.AgentService
}

// NewHandler returns a Handler bound to the given service.
func NewHandler(svc *cairnidentity.AgentService) *Handler {
	return &Handler{svc: svc}
}

// callerKey is the context key for the authenticated Caller.
type callerKey struct{}

// WithCaller attaches a Caller to ctx. Used by the auth middleware
// (in production) and tests (for injection).
func WithCaller(ctx context.Context, c *cairnidentity.Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// callerFromCtx returns the Caller, or nil for anonymous requests.
func callerFromCtx(ctx context.Context) *cairnidentity.Caller {
	c, _ := ctx.Value(callerKey{}).(*cairnidentity.Caller)
	return c
}

// PostAgents handles POST /api/cairn/v1/agents.
func (h *Handler) PostAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var in RegisterRequestJSON
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "could not decode JSON body")
		return
	}

	if in.ProposedOwner == "" || in.Slug == "" || in.Domain == "" || in.PublicKeyHex == "" {
		writeError(w, http.StatusBadRequest, "missing_fields", "proposed_owner, slug, domain, and public_key are required")
		return
	}

	pubBytes, err := hex.DecodeString(in.PublicKeyHex)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_public_key_hex", err.Error())
		return
	}
	if len(pubBytes) != ed25519.PublicKeySize {
		writeError(w, http.StatusBadRequest, "invalid_public_key_size", "expected 32 bytes")
		return
	}

	agent, err := h.svc.Register(r.Context(), cairnidentity.RegisterRequest{
		ProposedOwner: in.ProposedOwner,
		Slug:          in.Slug,
		Domain:        in.Domain,
		PublicKey:     ed25519.PublicKey(pubBytes),
	}, callerFromCtx(r.Context()))

	switch {
	case err == nil:
		// continue below
	case errors.Is(err, cairnidentity.ErrUserNotFound):
		writeError(w, http.StatusNotFound, "owner_not_found", "")
		return
	case errors.Is(err, cairnidentity.ErrAgentExists):
		writeError(w, http.StatusConflict, "agent_exists", "agent with this slug or fingerprint already exists")
		return
	default:
		log.Printf("cairn api v1: PostAgents: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	writeAgent(w, http.StatusCreated, agent, in.ProposedOwner, false)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, code int, errorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(ErrorJSON{Error: errorCode, Message: message})
}

// writeAgent writes an agent as JSON with the given HTTP status code.
func writeAgent(w http.ResponseWriter, code int, a *cairn.Agent, ownerName string, blocked bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	out := AgentJSON{
		Fingerprint:  a.Fingerprint,
		OwnerName:    ownerName,
		Slug:         a.Slug,
		Domain:       a.Domain,
		PublicKeyHex: hex.EncodeToString(a.PublicKey),
		Status:       string(a.Status),
		Blocked:      blocked,
		CreatedAt:    a.CreatedAt.UTC().Format(time.RFC3339),
	}
	if a.ActivatedAt != nil {
		out.ActivatedAt = a.ActivatedAt.UTC().Format(time.RFC3339)
	}
	_ = json.NewEncoder(w).Encode(out)
}

// fingerprintKey is the context key for the URL :fingerprint path param.
type fingerprintKey struct{}

// WithFingerprintParam attaches the URL :fingerprint param to the
// request context. Test injection helper; production wiring uses the
// router's path-param extraction.
func WithFingerprintParam(r *http.Request, fp string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), fingerprintKey{}, fp))
}

// fingerprintFromCtx returns the :fingerprint URL param.
func fingerprintFromCtx(ctx context.Context) string {
	fp, _ := ctx.Value(fingerprintKey{}).(string)
	return fp
}

// PostApprove handles POST /api/cairn/v1/agents/:fingerprint/approve.
func (h *Handler) PostApprove(w http.ResponseWriter, r *http.Request) {
	caller := callerFromCtx(r.Context())
	if caller == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	fp := fingerprintFromCtx(r.Context())
	if fp == "" {
		writeError(w, http.StatusBadRequest, "missing_fingerprint", "")
		return
	}

	err := h.svc.Approve(r.Context(), fp, caller)
	switch {
	case err == nil:
		// fall through to load + return updated agent
	case errors.Is(err, cairnidentity.ErrAgentNotFound):
		writeError(w, http.StatusNotFound, "agent_not_found", "")
		return
	case errors.Is(err, cairnidentity.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "only the agent's owner may approve")
		return
	default:
		log.Printf("cairn api v1: PostApprove: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	a, err := h.svc.GetByFingerprint(r.Context(), fp)
	if err != nil {
		log.Printf("cairn api v1: PostApprove readback: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}
	blocked, _ := h.svc.IsBlocked(r.Context(), fp)
	ownerName, _ := h.svc.UsernameByID(r.Context(), a.UserID)
	writeAgent(w, http.StatusOK, a, ownerName, blocked)
}

// PostBlock handles POST /api/cairn/v1/agents/:fingerprint/block.
func (h *Handler) PostBlock(w http.ResponseWriter, r *http.Request) {
	caller := callerFromCtx(r.Context())
	if caller == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	fp := fingerprintFromCtx(r.Context())
	if fp == "" {
		writeError(w, http.StatusBadRequest, "missing_fingerprint", "")
		return
	}

	var in BlockRequestJSON
	if r.Body != nil && r.ContentLength != 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "could not decode JSON body")
			return
		}
	}

	err := h.svc.Block(r.Context(), fp, in.Reason, caller)
	switch {
	case err == nil:
		a, gerr := h.svc.GetByFingerprint(r.Context(), fp)
		if gerr != nil {
			log.Printf("cairn api v1: PostBlock readback: %v", gerr)
			writeError(w, http.StatusInternalServerError, "internal_error", "")
			return
		}
		ownerName, _ := h.svc.UsernameByID(r.Context(), a.UserID)
		writeAgent(w, http.StatusOK, a, ownerName, true)
		return
	case errors.Is(err, cairnidentity.ErrAgentNotFound):
		writeError(w, http.StatusNotFound, "agent_not_found", "")
	case errors.Is(err, cairnidentity.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "only the agent's owner may block")
	default:
		log.Printf("cairn api v1: PostBlock: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
	}
}

// GetIdentity handles GET /api/cairn/v1/agents/:fingerprint/identity.
// Returns the agent's public key + metadata. Public — no auth required
// (the public key is, by definition, public).
func (h *Handler) GetIdentity(w http.ResponseWriter, r *http.Request) {
	fp := fingerprintFromCtx(r.Context())
	if fp == "" {
		writeError(w, http.StatusBadRequest, "missing_fingerprint", "")
		return
	}

	a, err := h.svc.GetByFingerprint(r.Context(), fp)
	if err != nil {
		if errors.Is(err, cairnidentity.ErrAgentNotFound) {
			writeError(w, http.StatusNotFound, "agent_not_found", "")
			return
		}
		log.Printf("cairn api v1: GetIdentity: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	blocked, _ := h.svc.IsBlocked(r.Context(), fp)
	ownerName, _ := h.svc.UsernameByID(r.Context(), a.UserID)
	writeAgent(w, http.StatusOK, a, ownerName, blocked)
}

// GetAgents handles GET /api/cairn/v1/agents — list the authed user's
// own agents. Optional ?status= filter accepts "pending" or "active".
func (h *Handler) GetAgents(w http.ResponseWriter, r *http.Request) {
	caller := callerFromCtx(r.Context())
	if caller == nil {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}

	status := cairn.AgentStatus(r.URL.Query().Get("status"))

	agents, err := h.svc.ListByUser(r.Context(), caller.UserID, status)
	if err != nil {
		log.Printf("cairn api v1: GetAgents: %v", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "")
		return
	}

	out := make([]AgentJSON, 0, len(agents))
	for _, a := range agents {
		blocked, _ := h.svc.IsBlocked(r.Context(), a.Fingerprint)
		ownerName, _ := h.svc.UsernameByID(r.Context(), a.UserID)
		j := AgentJSON{
			Fingerprint:  a.Fingerprint,
			OwnerName:    ownerName,
			Slug:         a.Slug,
			Domain:       a.Domain,
			PublicKeyHex: hex.EncodeToString(a.PublicKey),
			Status:       string(a.Status),
			Blocked:      blocked,
			CreatedAt:    a.CreatedAt.UTC().Format(time.RFC3339),
		}
		if a.ActivatedAt != nil {
			j.ActivatedAt = a.ActivatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, j)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}
