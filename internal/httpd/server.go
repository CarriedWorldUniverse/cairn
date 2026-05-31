// Package httpd is cairn's HTTP ingress: Smart-HTTPv2 git plus a small
// repo-admin API, reached THROUGH interchange-gateway. It trusts the
// gateway-injected X-CWB-* identity (the gateway already ran herald
// verification) over the mTLS hop and does NOT re-verify. The Smart-HTTP byte
// protocol is served by the system `git http-backend` CGI; cairn owns auth +
// routing and delegates the pack streaming.
package httpd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cgi"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	ledgerclient "github.com/CarriedWorldUniverse/cairn/internal/ledger"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
)

// IssueCreator is the slice of ledger cairn needs: open a tracking issue on
// behalf of a caller (identity forwarded via fwd). *ledger.Client satisfies it;
// tests use a fake.
type IssueCreator interface {
	CreateIssue(ctx context.Context, fwd http.Header, in ledgerclient.IssueInput) (ledgerclient.IssueResult, error)
	CommentIssue(ctx context.Context, fwd http.Header, key, body string) error
}

// Config configures the HTTP ingress.
type Config struct {
	Core       *repo.Service
	GitPath    string       // path to the git binary; defaults to "git" on PATH
	Ledger     IssueCreator // outbound ledger client for PR-as-issue
	PublicBase string       // optional public base URL for ExternalRef.url; "" omits it
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

// Handler returns the HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","service":"cairn"}`))
	})
	mux.HandleFunc("POST /api/orgs/{org}/repos", s.handleCreateRepo)
	mux.HandleFunc("POST /api/orgs/{org}/repos/{slug}/pulls", s.handleOpenPull)
	mux.HandleFunc("GET /api/orgs/{org}/repos/{slug}/pulls/{id}", s.handleGetPull)
	mux.HandleFunc("POST /api/orgs/{org}/repos/{slug}/pulls/{id}/merge", s.handleMergePull)
	// Everything else: Smart-HTTP git, matched by the .git path shape.
	mux.HandleFunc("/", s.handleGit)
	return mux
}

func (s *Server) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFromHeaders(r)
	if !ok {
		httpErr(w, http.StatusUnauthorized, "missing identity")
		return
	}
	org := r.PathValue("org")
	if id.Org != org {
		httpErr(w, http.StatusForbidden, "org mismatch")
		return
	}
	if !id.HasScope("repo:write") {
		httpErr(w, http.StatusForbidden, "missing scope repo:write")
		return
	}
	var body struct {
		Slug string `json:"slug"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Slug == "" {
		httpErr(w, http.StatusBadRequest, "slug required")
		return
	}
	rp, err := s.cfg.Core.CreateRepo(r.Context(), org, body.Slug)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": rp.ID, "org": rp.OrgID, "slug": rp.Slug, "default_branch": rp.DefaultBranch,
	})
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

	s.serveBackend(w, r, rp)
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

// serveBackend runs `git http-backend` as a CGI handler over the bare repo.
// GIT_PROJECT_ROOT points at the repo's parent dir; PATH_INFO is /<id>.git/<rest>.
func (s *Server) serveBackend(w http.ResponseWriter, r *http.Request, rp repo.Repo) {
	root := filepath.Dir(rp.StoragePath) // repoRoot
	base := filepath.Base(rp.StoragePath) // <id>.git
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
