// Cairn-specific code; AGPLv3. See LICENSING.md.

package v1

import (
	"net/http"

	"github.com/CarriedWorldUniverse/cairn/modules/web"
	cairnidentity "github.com/CarriedWorldUniverse/cairn/services/cairn/identity"
	"github.com/CarriedWorldUniverse/cairn/services/context"

	"github.com/go-chi/chi/v5"
)

// extractForgejoUser pulls the authenticated Doer out of the
// services/context APIContext attached to the request by Forgejo's
// shared API middleware stack (see routers/api/shared.Middlewares —
// in particular APIContexter() + apiAuthentication()).
//
// Returns nil for anonymous requests, and nil if the APIContext is
// missing entirely (which would mean the route was mounted outside
// the shared middleware stack — e.g. in unit tests). We use a
// panic-recover here because services/context.GetAPIContext does an
// unchecked type assertion and panics when the key is absent.
func extractForgejoUser(r *http.Request) (out *cairnidentity.Caller) {
	defer func() {
		if rec := recover(); rec != nil {
			out = nil
		}
	}()
	apiCtx := context.GetAPIContext(r)
	if apiCtx == nil || apiCtx.Doer == nil {
		return nil
	}
	return &cairnidentity.Caller{
		UserID:   apiCtx.Doer.ID,
		Username: apiCtx.Doer.Name,
		IsAdmin:  apiCtx.Doer.IsAdmin,
	}
}

// urlParam pulls a URL path parameter via chi (Forgejo's underlying
// router).
func urlParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// webRouteGroup adapts a *web.Route prefix to the RouteGroup interface.
// Forgejo's Route.Get/Post accept variadic any (so middleware/handlers
// can be mixed in), which means *web.Route doesn't directly satisfy
// our stricter http.HandlerFunc-typed interface. The adapter is a
// one-line shim per method.
type webRouteGroup struct{ r *web.Route }

// NewForgejoRouteGroup wraps a *web.Route into a Cairn RouteGroup.
// Caller is expected to have already entered the desired group prefix
// via r.Group("/api/cairn/v1", ...) so that the adapter's relative
// patterns ("/agents", "/agents/{fingerprint}/...") land at the right
// absolute paths.
func NewForgejoRouteGroup(r *web.Route) RouteGroup {
	return &webRouteGroup{r: r}
}

func (g *webRouteGroup) Get(pattern string, h http.HandlerFunc) {
	g.r.Get(pattern, h)
}

func (g *webRouteGroup) Post(pattern string, h http.HandlerFunc) {
	g.r.Post(pattern, h)
}
