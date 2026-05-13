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

	"golang.org/x/crypto/ssh"

	cairn "github.com/CarriedWorldUniverse/cairn/models/cairn"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

// maxRequestBodyBytes bounds the size of API request bodies. Protects
// anonymously-callable endpoints (attachment-request submission, block
// reason, etc.) from DoS via huge payload. An OpenSSH ed25519
// authorized_keys line is ~80 bytes; 4 KiB leaves comfortable headroom
// for JSON wrapping and slug/domain/owner fields.
const maxRequestBodyBytes = 4096

// Handler is the HTTP handler set for /api/cairn/v1.
type Handler struct {
	svc *cairnidentity.AgentService

	// aspectProvisioner mints aspect users + access tokens for the
	// Phase 1 AI-first identity layer (POST /users/me/aspects). nil
	// until WithAspectProvisioner is called; PostMintAspect returns
	// 503 when unwired so the surface fails closed by default.
	aspectProvisioner AspectProvisioner
}

// NewHandler returns a Handler bound to the given service.
func NewHandler(svc *cairnidentity.AgentService) *Handler {
	return &Handler{svc: svc}
}

// WithAspectProvisioner returns a copy of h with the given aspect
// provisioner attached. Production wiring builds the provisioner
// (backed by forgejo's user_model.CreateUser + auth_model.
// NewAccessToken) once at boot and threads it through this setter;
// tests inject fakes the same way.
func (h *Handler) WithAspectProvisioner(p AspectProvisioner) *Handler {
	cp := *h
	cp.aspectProvisioner = p
	return &cp
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

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, code int, errorCode, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(ErrorJSON{Error: errorCode, Message: message})
}

// writeAgent writes an agent as JSON with the given HTTP status code.
//
// Post-V503 the Agent struct no longer carries Fingerprint / PublicKey
// directly. The caller passes the values it derived from the join
// (cairn_agent_pubkey + Forgejo public_key) or recomputed.
func writeAgent(w http.ResponseWriter, code int, a *cairn.Agent, ownerName string, blocked bool, fingerprint, publicKeyHex string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	out := AgentJSON{
		Fingerprint:  fingerprint,
		OwnerName:    ownerName,
		Slug:         a.Slug,
		Domain:       a.Domain,
		PublicKeyHex: publicKeyHex,
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
	pubHex, _ := h.pubkeyHexForFingerprint(r.Context(), fp)
	writeAgent(w, http.StatusOK, a, ownerName, blocked, fp, pubHex)
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
		pubHex, _ := h.pubkeyHexForFingerprint(r.Context(), fp)
		writeAgent(w, http.StatusOK, a, ownerName, true, fp, pubHex)
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
	pubHex, _ := h.pubkeyHexForFingerprint(r.Context(), fp)
	writeAgent(w, http.StatusOK, a, ownerName, blocked, fp, pubHex)
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
		// Pick the first registered pubkey for the listing. Multi-host
		// agents expose all of theirs via the per-agent detail endpoint
		// (added in Task 4).
		pubkeys, _ := h.svc.ListAgentPubkeys(r.Context(), a.ID)
		var fp, pubHex string
		blocked := false
		if len(pubkeys) > 0 {
			fp = pubkeys[0].Fingerprint
			blocked, _ = h.svc.IsBlocked(r.Context(), fp)
			if content, err := h.svc.PubkeyContentForAgent(r.Context(), a.ID); err == nil {
				pubHex = pubkeyHexFromContent(content)
			}
		}
		ownerName, _ := h.svc.UsernameByID(r.Context(), a.UserID)
		j := AgentJSON{
			Fingerprint:  fp,
			OwnerName:    ownerName,
			Slug:         a.Slug,
			Domain:       a.Domain,
			PublicKeyHex: pubHex,
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

// pubkeyHexForFingerprint loads the OpenSSH-format pubkey content for
// the given fingerprint and returns its raw ed25519 bytes hex-encoded
// (matching the pre-V503 wire format for AgentJSON.PublicKeyHex).
// Returns empty string + the underlying error on failure; callers that
// don't care about the error log it and degrade gracefully.
func (h *Handler) pubkeyHexForFingerprint(ctx context.Context, fp string) (string, error) {
	content, err := h.svc.PubkeyContentForFingerprint(ctx, fp)
	if err != nil {
		return "", err
	}
	return pubkeyHexFromContent(content), nil
}

// pubkeyHexFromContent extracts the raw ed25519 bytes from an OpenSSH
// authorized_keys line (e.g. "ssh-ed25519 AAAA...") and hex-encodes
// them. Best-effort: returns empty on parse failure or non-ed25519
// key types.
func pubkeyHexFromContent(content string) string {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(content))
	if err != nil {
		return ""
	}
	cpk, ok := pub.(ssh.CryptoPublicKey)
	if !ok {
		return ""
	}
	ed, ok := cpk.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return ""
	}
	return hex.EncodeToString(ed)
}
