// Package grpcapi is cairn's gRPC JSON-API surface — repos, pull requests, and
// the cross-pillar org purge — served behind interchange over mTLS. It is the
// Phase 3 successor to the HTTP API handlers in internal/httpd; identity now
// arrives as cwb-* gRPC metadata (injected by the gateway after herald
// verification), which cairn TRUSTS and does NOT re-verify.
//
// cairn's git transports are unaffected: Smart-HTTP (internal/httpd handleGit)
// and SSH (internal/sshd) keep their own ingresses — git cannot be gRPC.
package grpcapi

import (
	"context"
	"net/http"
	"strings"

	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
	cairnv1 "github.com/CarriedWorldUniverse/cwb-proto/gen/go/cwb/cairn/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// IssueCreator is the slice of the ledger client cairn's PR flow needs. The
// real *ledger.Client satisfies it; tests use a fake.
type IssueCreator interface {
	CreateIssue(ctx context.Context, fwd http.Header, in ledgerclient.IssueInput) (ledgerclient.IssueResult, error)
	CommentIssue(ctx context.Context, fwd http.Header, key, body string) error
}

// identity is the gateway-verified caller, read from cwb-* gRPC metadata.
type identity struct {
	Subject          string
	Org              string
	Kind             string
	ResponsibleHuman string
	Scopes           []string
}

func (i identity) hasScope(s string) bool {
	for _, have := range i.Scopes {
		if have == s {
			return true
		}
	}
	return false
}

// identityFromCtx reads the trusted cwb-* metadata. ok is false when Subject or
// Org is absent — the gateway always sets both for an authed request, so their
// absence means the call did not arrive via the authed gateway path.
func identityFromCtx(ctx context.Context) (identity, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return identity{}, false
	}
	get := func(k string) string {
		if v := md.Get(k); len(v) > 0 {
			return v[0]
		}
		return ""
	}
	sub, org := get("cwb-subject"), get("cwb-org")
	if sub == "" || org == "" {
		return identity{}, false
	}
	return identity{
		Subject:          sub,
		Org:              org,
		Kind:             get("cwb-kind"),
		ResponsibleHuman: get("cwb-responsible-human"),
		Scopes:           strings.Fields(get("cwb-scopes")),
	}, true
}

// forwardHeader rebuilds the X-CWB-* headers from the verified identity so the
// ledger client (which maps X-CWB-* -> cwb-* metadata) acts on-behalf-of the
// caller when cairn opens/comments the linked ledger issue.
func (i identity) forwardHeader() http.Header {
	h := http.Header{}
	h.Set("X-CWB-Subject", i.Subject)
	h.Set("X-CWB-Org", i.Org)
	if i.Kind != "" {
		h.Set("X-CWB-Kind", i.Kind)
	}
	if i.ResponsibleHuman != "" {
		h.Set("X-CWB-Responsible-Human", i.ResponsibleHuman)
	}
	h.Set("X-CWB-Scopes", strings.Join(i.Scopes, " "))
	return h
}

// authed extracts the identity, enforces that the request's org (a path param)
// equals the caller's verified org, and that the caller holds scope. reqOrg ""
// skips the org-match check (the org-bound PurgeOrg targets the verified org).
func authed(ctx context.Context, reqOrg, scope string) (identity, error) {
	id, ok := identityFromCtx(ctx)
	if !ok {
		return identity{}, status.Error(codes.Unauthenticated, "missing identity")
	}
	if reqOrg != "" && id.Org != reqOrg {
		return identity{}, status.Error(codes.PermissionDenied, "org mismatch")
	}
	if !id.hasScope(scope) {
		return identity{}, status.Error(codes.PermissionDenied, "missing scope "+scope)
	}
	return id, nil
}

// toProtoPull mirrors the REST toPullResponse: url is set only when a public
// base is configured.
func toProtoPull(p repo.Pull, slug, publicBase, org string) *cairnv1.Pull {
	url := ""
	if publicBase != "" {
		url = publicBase + "/" + org + "/" + slug
	}
	return &cairnv1.Pull{
		Id:             p.ID,
		Repo:           slug,
		Source:         p.Source,
		Target:         p.Target,
		Title:          p.Title,
		State:          p.State,
		LedgerIssueKey: p.LedgerIssueKey,
		Url:            url,
	}
}

// Servers holds the cairn gRPC service implementations over the repo core +
// ledger client.
type Servers struct {
	core       *repo.Service
	ledger     IssueCreator
	publicBase string // optional; "" omits ExternalRef/PR urls
}

// New builds the cairn gRPC service implementations.
func New(core *repo.Service, ledger IssueCreator, publicBase string) *Servers {
	return &Servers{core: core, ledger: ledger, publicBase: publicBase}
}

// Register registers RepoService, PullService, and OrgService on g.
func (s *Servers) Register(g grpc.ServiceRegistrar) {
	cairnv1.RegisterRepoServiceServer(g, &repoServer{s: s})
	cairnv1.RegisterPullServiceServer(g, &pullServer{s: s})
	cairnv1.RegisterOrgServiceServer(g, &orgServer{s: s})
}
