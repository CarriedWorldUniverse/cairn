// Package httpd is cairn's Smart-HTTPv2 git ingress, reached THROUGH
// interchange-gateway. It trusts the gateway-injected X-CWB-* identity (the
// gateway already ran herald verification) over the reverse-proxy hop and does
// NOT re-verify. The Smart-HTTP byte protocol is served by the system
// `git http-backend` CGI; cairn owns auth + routing and delegates the pack
// streaming.
//
// The repo-admin JSON API (create-repo, pulls, org-purge) moved to gRPC in
// Phase 3 — see internal/grpcapi. This package now serves ONLY git + /healthz,
// because git cannot be gRPC and must stay on its byte transport.
package httpd

import (
	"fmt"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CarriedWorldUniverse/cairn/internal/repo"
)

// Config configures the git ingress.
type Config struct {
	Core    *repo.Service
	GitPath string // path to the git binary; defaults to "git" on PATH
}

// Server is cairn's HTTP git host.
type Server struct {
	cfg     Config
	gitPath string
}

// New builds a Server. The git path is resolved to an absolute location:
// net/http/cgi's fork/exec does not consult PATH, so a bare "git" must be
// looked up here (falling back to the literal value if lookup fails).
func New(cfg Config) *Server {
	gp := cfg.GitPath
	if gp == "" {
		gp = "git"
	}
	if !filepath.IsAbs(gp) {
		if resolved, err := exec.LookPath(gp); err == nil {
			gp = resolved
		}
	}
	return &Server{cfg: cfg, gitPath: gp}
}

// pathRe matches /{org}/{slug}.git/<rest> capturing org, slug, rest.
var pathRe = regexp.MustCompile(`^/([^/]+)/([^/]+)\.git(/.*)?$`)

// Handler returns the HTTP mux: /healthz plus Smart-HTTP git for any .git path.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"cairn"}`))
	})
	// Everything else: Smart-HTTP git, matched by the .git path shape.
	mux.HandleFunc("/", s.handleGit)
	return mux
}

// handleGit routes a Smart-HTTP request: enforce scope from the trusted
// identity, then delegate the byte protocol to `git http-backend`.
func (s *Server) handleGit(w http.ResponseWriter, r *http.Request) {
	m := pathRe.FindStringSubmatch(r.URL.Path)
	if m == nil {
		httpErr(w, http.StatusNotFound, "not a git path")
		return
	}
	org, slug, rest := m[1], m[2], m[3]

	id, ok := identityFromHeaders(r)
	if !ok {
		httpErr(w, http.StatusUnauthorized, "missing identity")
		return
	}
	if id.Org != org {
		httpErr(w, http.StatusForbidden, "org mismatch")
		return
	}

	// Determine read vs write from the requested service/endpoint.
	write := isWriteRequest(rest, r.URL.RawQuery)
	need := "repo:read"
	if write {
		need = "repo:write"
	}
	if !id.HasScope(need) {
		httpErr(w, http.StatusForbidden, "missing scope "+need)
		return
	}

	rp, err := s.cfg.Core.GetRepo(r.Context(), org, slug)
	if err != nil {
		httpErr(w, http.StatusNotFound, "repo not found")
		return
	}

	// Embargo gate: an authorized recipient fetching (upload-pack) is served the
	// embargo bare (real content); everyone else gets the public bare (frozen).
	// A write (receive-pack) always targets the public bare. No-op without an
	// embargo bare.
	verb := "git-upload-pack"
	if write {
		verb = "git-receive-pack"
	}
	bareDir := s.cfg.Core.BareForServe(r.Context(), rp.ID, id.Subject, verb)
	s.serveBackend(w, r, bareDir)
}

// isWriteRequest reports whether a Smart-HTTP request mutates the repo
// (git-receive-pack advertisement or POST).
func isWriteRequest(rest, rawQuery string) bool {
	if strings.HasSuffix(rest, "/git-receive-pack") {
		return true
	}
	if strings.HasSuffix(rest, "/info/refs") && strings.Contains(rawQuery, "service=git-receive-pack") {
		return true
	}
	return false
}

// serveBackend runs `git http-backend` as a CGI handler over bareDir (the public
// or, for an authorized embargo fetch, the embargo bare). GIT_PROJECT_ROOT points
// at the repo's parent dir; PATH_INFO is /<bare>/<rest>.
func (s *Server) serveBackend(w http.ResponseWriter, r *http.Request, bareDir string) {
	root := filepath.Dir(bareDir)  // repoRoot
	base := filepath.Base(bareDir) // <id>.git or <id>.embargo.git
	m := pathRe.FindStringSubmatch(r.URL.Path)
	rest := ""
	if m != nil {
		rest = m[3]
	}
	// -c http.receivepack=true enables push over Smart-HTTP for every repo
	// (cairn has already enforced repo:write scope before reaching here; the
	// receive-pack toggle in git is otherwise off by default).
	h := &cgi.Handler{
		Path: s.gitPath,
		Args: []string{"-c", "http.receivepack=true", "http-backend"},
		Dir:  root,
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
			"PATH_INFO=/" + base + rest,
		},
	}
	h.ServeHTTP(w, r)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"error":%q}`, msg)))
}
