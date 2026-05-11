// Cairn-specific code; AGPLv3. See LICENSING.md.

package v1

import (
	"context"
	"net/http"
	"sync"

	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
)

var (
	initOnce      sync.Once
	initErr       error
	globalService *cairnidentity.AgentService
	globalHandler *Handler
)

// Init is called once at server startup. Reads the instance HMAC key
// from disk (generating on first run if missing), constructs the
// AgentService, and stores it globally for the route handlers to use.
//
// Caller is responsible for ensuring this runs after the database
// engine is initialized but before the HTTP server starts accepting
// connections.
//
// Init is once-only per process: subsequent calls are no-ops returning
// the result of the first call (success or error). If the configuration
// changes at runtime (e.g. a different HMAC key path is desired),
// the process must be restarted.
func Init(
	_ context.Context,
	hmacKeyPath string,
	store cairnidentity.AgentStore,
	pubkeys cairnidentity.AgentPubkeyStore,
	requests cairnidentity.AttachmentRequestStore,
	blocklist cairnidentity.AgentBlocklistStore,
	users cairnidentity.UserResolver,
	registrar cairnidentity.AgentUserRegistrar,
) error {
	initOnce.Do(func() {
		key, err := cairnidentity.LoadInstanceHMACKey(hmacKeyPath)
		if err != nil {
			initErr = err
			return
		}
		globalService = cairnidentity.NewAgentService(key, store, pubkeys, requests, blocklist, users, registrar)
		globalHandler = NewHandler(globalService)
		// Publish to the identity-package global so consumers outside
		// the v1 API (e.g. the pre-receive hook) can reach the service
		// without importing routers/api/cairn/v1.
		cairnidentity.SetGlobal(globalService)
	})
	return initErr
}

// Handler returns the package-global handler. Returns nil if Init has
// not yet been called.
func GlobalHandler() *Handler { return globalHandler }

// RouteGroup is the minimal interface Cairn needs from Forgejo's
// router. Forgejo's *web.Route satisfies this via a thin adapter
// (see routes_forgejo.go). Keeping the interface tight here means
// the Cairn-specific route layout stays decoupled from any one
// router type — which keeps rebases against upstream sane.
type RouteGroup interface {
	Get(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
}

// MountRoutes wires the Cairn API endpoints onto the provided router
// group. Caller is expected to root the group at /api/cairn/v1 and to
// have already attached whatever auth middleware populates the
// services/context APIContext (so that extractForgejoUser can find a
// Doer when present).
//
// MountRoutes panics if Init has not been called — failing fast at
// boot is preferable to a silently broken API.
func MountRoutes(group RouteGroup) {
	if globalHandler == nil {
		panic("cairn api v1: MountRoutes called before Init")
	}

	withCaller := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			caller := extractForgejoUser(r)
			if caller != nil {
				r = r.WithContext(WithCaller(r.Context(), caller))
			}
			next(w, r)
		}
	}

	withFP := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			fp := urlParam(r, "fingerprint")
			r = WithFingerprintParam(r, fp)
			next(w, r)
		}
	}

	withReqID := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			id := parseRequestIDParam(urlParam(r, "id"))
			r = WithRequestIDParam(r, id)
			next(w, r)
		}
	}

	group.Get("/agents", withCaller(globalHandler.GetAgents))
	group.Get("/agents/{fingerprint}/identity", withFP(globalHandler.GetIdentity))
	group.Post("/agents/{fingerprint}/approve", withFP(withCaller(globalHandler.PostApprove)))
	group.Post("/agents/{fingerprint}/block", withFP(withCaller(globalHandler.PostBlock)))
	group.Post("/agents/attachment-requests", withCaller(globalHandler.PostAttachmentRequest))
	group.Get("/agents/attachment-requests", withCaller(globalHandler.GetAttachmentRequests))
	group.Post("/agents/attachment-requests/{id}/approve", withReqID(withCaller(globalHandler.PostApproveAttachmentRequest)))
	group.Post("/agents/attachment-requests/{id}/reject", withReqID(withCaller(globalHandler.PostRejectAttachmentRequest)))
	group.Get("/users/me/pending-attachment-requests", withCaller(globalHandler.GetMyPendingAttachmentRequests))
}
