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

	writeAgent(w, http.StatusCreated, agent, in.ProposedOwner)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, code int, errorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(ErrorJSON{Error: errorCode, Message: message})
}

// writeAgent writes an agent as JSON with the given HTTP status code.
func writeAgent(w http.ResponseWriter, code int, a *cairn.Agent, ownerName string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	out := AgentJSON{
		Fingerprint:  a.Fingerprint,
		OwnerName:    ownerName,
		Slug:         a.Slug,
		Domain:       a.Domain,
		PublicKeyHex: hex.EncodeToString(a.PublicKey),
		Status:       string(a.Status),
		CreatedAt:    a.CreatedAt.UTC().Format(time.RFC3339),
	}
	if a.ActivatedAt != nil {
		out.ActivatedAt = a.ActivatedAt.UTC().Format(time.RFC3339)
	}
	_ = json.NewEncoder(w).Encode(out)
}
