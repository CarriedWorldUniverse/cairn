# Cairn Registration CLI Implementation Plan (Plan 2b)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cairn binary gains operator-side CLI subcommands so a human (or an agent on a machine with the owner's seed) can register, list, approve, block agents, and sign git commits. Builds on Plan 2a's API. After this plan: end-to-end agent registration is achievable from a CLI without curl, and `git commit -S` produces verifiable agent signatures.

**Architecture:** The same `cairn` binary that runs `cairn web` and `cairn migrate` gains new subcommands under `cairn agent ...`, `cairn agents ...`, `cairn auth ...`, and `cairn commit-sign-helper`. Subcommands live under `cmd/cairn/` (created in Plan 1, populated here). Each CLI action is testable in isolation — pure logic in `cmd/cairn/<verb>.go`, urfave/cli wiring is thin glue.

**Tech Stack:** Go 1.25+, Forgejo's existing urfave/cli v3 (verify version), `crypto/ed25519`, `crypto/sha256`, `golang.org/x/crypto/ssh` (for SSH signature format), `golang.org/x/term` (for password prompting on `auth login`), Cairn's casket-go `DeriveAgentKey`.

**Spec ref:** [`docs/cairn/specs/2026-05-09-cairn-foundation-design.md`](../specs/2026-05-09-cairn-foundation-design.md), §4.8 (CLI minimum), §6 (commit signing flow).

**Plan 1 + 2a dependencies (already on `cairn`):**
- `casket.DeriveAgentKey(seed, slug)` — HKDF Ed25519 derivation
- `cairnidentity.Fingerprint(hmacKey, pubkey)` — HMAC fingerprint (CLI doesn't need this directly, but uses for understanding)
- `/api/cairn/v1/agents` family of endpoints — register, list, approve, block, identity
- `setting.Cairn.HMACKeyPath` and other config flags

**Forgejo CLI conventions:** subcommands use urfave/cli `cli.Command` structs registered into a top-level `cli.App` in `cmd/cmd.go` (or similar entry point). Verify the actual conventions before writing — e.g. `cmd/admin.go` shows how nested subcommands work in this Forgejo version.

---

## Task 1: CLI infrastructure — config paths, HTTP client, key storage

**Files:**
- Create: `cmd/cairn/config.go` — config-dir resolution, token + seed file paths
- Create: `cmd/cairn/config_test.go`
- Create: `cmd/cairn/client.go` — HTTP client wrapper around `/api/cairn/v1/`
- Create: `cmd/cairn/client_test.go` — uses httptest

**Why:** Subsequent tasks (login, init, submit, list, approve, block, sign-helper) all need to find config files and (most) need to call the Cairn API. Centralise the plumbing so each subcommand stays small.

- [ ] **Step 1: New feature branch**

```bash
cd ~/Source/cairn && git checkout cairn && git pull
git checkout -b cairn-cli-infrastructure
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write the failing test in `cmd/cairn/config_test.go`**

```go
package cairn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigPaths_FromXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", "/should/not/be/used")

	c, err := ResolvePaths("https://cairn.example.com")
	if err != nil {
		t.Fatal(err)
	}

	wantHostDir := filepath.Join(dir, "cairn", "cairn.example.com")
	if c.HostDir != wantHostDir {
		t.Errorf("HostDir = %q, want %q", c.HostDir, wantHostDir)
	}
	if c.SeedFile != filepath.Join(dir, "cairn", "seed") {
		t.Errorf("SeedFile = %q, want under cairn root", c.SeedFile)
	}
	if c.TokenFile != filepath.Join(c.HostDir, "token") {
		t.Errorf("TokenFile = %q, want HostDir/token", c.TokenFile)
	}
}

func TestConfigPaths_FromHOMEFallback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", dir)

	c, err := ResolvePaths("https://cairn.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.HostDir, filepath.Join(dir, ".config", "cairn")) {
		t.Errorf("HostDir = %q, want under HOME/.config/cairn", c.HostDir)
	}
}

func TestConfigPaths_RejectsEmptyURL(t *testing.T) {
	_, err := ResolvePaths("")
	if err == nil {
		t.Error("expected error for empty URL")
	}
}

func TestConfigPaths_RejectsMalformedURL(t *testing.T) {
	_, err := ResolvePaths("://not-a-url")
	if err == nil {
		t.Error("expected error for malformed URL")
	}
}

func TestConfigPaths_KeyFilePathBySlug(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	c, _ := ResolvePaths("https://cairn.example.com")
	got := c.KeyFile("plumb")
	want := filepath.Join(c.HostDir, "plumb.key")
	if got != want {
		t.Errorf("KeyFile = %q, want %q", got, want)
	}
}

func TestEnsureHostDir_Creates0700(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	c, _ := ResolvePaths("https://cairn.example.com")
	if err := c.EnsureHostDir(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(c.HostDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0700 {
		t.Errorf("perm = %#o, want 0700", perm)
	}
}
```

- [ ] **Step 3: Run, expect failure**

```bash
go test ./cmd/cairn/ -run TestConfigPaths -v
```

Expected: FAIL — `undefined: ResolvePaths`.

- [ ] **Step 4: Implement `cmd/cairn/config.go`**

```go
// Package cairn — Cairn CLI subcommands and shared CLI plumbing.
//
// Cairn-specific code; AGPLv3. See LICENSING.md.
package cairn

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// Paths holds the resolved filesystem locations Cairn's CLI uses.
//
// Layout:
//
//	$XDG_CONFIG_HOME/cairn/             ← config root (or $HOME/.config/cairn/)
//	   seed                             ← owner's HKDF seed (mode 0600)
//	   <host>/                          ← per-instance, mode 0700
//	     token                          ← API auth token (mode 0600)
//	     <slug>.key                     ← cached agent keypair (mode 0600)
//	     <slug>.key.pub                 ← agent public key (mode 0644)
type Paths struct {
	ConfigRoot string // $XDG_CONFIG_HOME/cairn or $HOME/.config/cairn
	HostDir    string // ConfigRoot/<host>
	SeedFile   string // ConfigRoot/seed (shared across hosts)
	TokenFile  string // HostDir/token
}

// ResolvePaths returns CLI paths for the given Cairn instance URL.
// Examples: "https://cairn.darksoft.co.nz", "http://localhost:3000".
func ResolvePaths(instanceURL string) (*Paths, error) {
	if instanceURL == "" {
		return nil, errors.New("cairn cli: instance URL must not be empty")
	}
	u, err := url.Parse(instanceURL)
	if err != nil {
		return nil, fmt.Errorf("cairn cli: parse URL: %w", err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("cairn cli: URL has no host: %q", instanceURL)
	}

	cfgRoot := os.Getenv("XDG_CONFIG_HOME")
	if cfgRoot == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return nil, errors.New("cairn cli: neither XDG_CONFIG_HOME nor HOME set")
		}
		cfgRoot = filepath.Join(home, ".config")
	}
	cfgRoot = filepath.Join(cfgRoot, "cairn")

	hostDir := filepath.Join(cfgRoot, u.Host)
	return &Paths{
		ConfigRoot: cfgRoot,
		HostDir:    hostDir,
		SeedFile:   filepath.Join(cfgRoot, "seed"),
		TokenFile:  filepath.Join(hostDir, "token"),
	}, nil
}

// KeyFile returns the per-agent private-key file path under HostDir.
// (Currently used as a cache; commit-sign-helper derives on demand.)
func (p *Paths) KeyFile(slug string) string {
	return filepath.Join(p.HostDir, slug+".key")
}

// EnsureHostDir creates the per-host config directory with mode 0700.
func (p *Paths) EnsureHostDir() error {
	return os.MkdirAll(p.HostDir, 0700)
}

// ReadSeed reads the owner's seed file. Returns an error if the file
// is missing or has insecure permissions.
func (p *Paths) ReadSeed() ([]byte, error) {
	info, err := os.Stat(p.SeedFile)
	if err != nil {
		return nil, fmt.Errorf("cairn cli: stat seed %q: %w", p.SeedFile, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		return nil, fmt.Errorf("cairn cli: seed %q has insecure mode %#o (want 0600)", p.SeedFile, perm)
	}
	return os.ReadFile(p.SeedFile)
}

// WriteToken stores the API token at TokenFile with mode 0600.
func (p *Paths) WriteToken(token string) error {
	if err := p.EnsureHostDir(); err != nil {
		return err
	}
	return os.WriteFile(p.TokenFile, []byte(token), 0600)
}

// ReadToken reads the API token. Returns an error if missing or
// insecure permissions.
func (p *Paths) ReadToken() (string, error) {
	info, err := os.Stat(p.TokenFile)
	if err != nil {
		return "", fmt.Errorf("cairn cli: stat token %q: %w", p.TokenFile, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		return "", fmt.Errorf("cairn cli: token %q has insecure mode %#o (want 0600)", p.TokenFile, perm)
	}
	b, err := os.ReadFile(p.TokenFile)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
```

- [ ] **Step 5: Run, expect PASS**

```bash
go test ./cmd/cairn/ -run TestConfigPaths -v
```

Expected: 6 tests pass (4 for ResolvePaths, 1 for KeyFile, 1 for EnsureHostDir).

- [ ] **Step 6: Write the failing test in `cmd/cairn/client_test.go`**

```go
package cairn

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClient_PostAgents(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cairn/v1/agents" {
			t.Errorf("path = %q, want /api/cairn/v1/agents", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "token test-tok" {
			t.Errorf("auth header = %q, want 'token test-tok'", r.Header.Get("Authorization"))
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["proposed_owner"] != "alice" {
			t.Errorf("proposed_owner = %v, want alice", body["proposed_owner"])
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"fingerprint": "cairn:test-fp",
			"slug":        "plumb",
			"status":      "active",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-tok")
	got, err := c.PostAgent(context.Background(), PostAgentRequest{
		ProposedOwner: "alice",
		Slug:          "plumb",
		Domain:        "darksoft.co.nz",
		PublicKey:     pub,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Fingerprint != "cairn:test-fp" {
		t.Errorf("Fingerprint = %q", got.Fingerprint)
	}
}

func TestClient_PostAgents_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":   "agent_exists",
			"message": "duplicate",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "test-tok")
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err := c.PostAgent(context.Background(), PostAgentRequest{
		ProposedOwner: "alice", Slug: "plumb", Domain: "darksoft.co.nz", PublicKey: pub,
	})
	if err == nil {
		t.Fatal("expected error on 409")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err type = %T, want *APIError", err)
	}
	if apiErr.StatusCode != http.StatusConflict {
		t.Errorf("StatusCode = %d, want 409", apiErr.StatusCode)
	}
	if apiErr.ErrorCode != "agent_exists" {
		t.Errorf("ErrorCode = %q, want agent_exists", apiErr.ErrorCode)
	}
}

func TestClient_GetAgents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cairn/v1/agents" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("status") != "pending" {
			t.Errorf("status filter = %q, want pending", r.URL.Query().Get("status"))
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"fingerprint": "cairn:a", "slug": "plumb", "status": "pending"},
			{"fingerprint": "cairn:b", "slug": "anvil", "status": "pending"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	got, err := c.ListAgents(context.Background(), "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("len = %d, want 2", len(got))
	}
}

func TestClient_ApproveAndBlock(t *testing.T) {
	calls := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"fingerprint": "cairn:test"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")

	if err := c.Approve(context.Background(), "cairn:test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Block(context.Background(), "cairn:test", "compromised"); err != nil {
		t.Fatal(err)
	}

	if len(calls) != 2 {
		t.Fatalf("calls = %v", calls)
	}
	if calls[0] != "POST /api/cairn/v1/agents/cairn:test/approve" {
		t.Errorf("approve call = %q", calls[0])
	}
	if calls[1] != "POST /api/cairn/v1/agents/cairn:test/block" {
		t.Errorf("block call = %q", calls[1])
	}

	_ = hex.EncodeToString // keep import
}
```

- [ ] **Step 7: Run, expect failure**

```bash
go test ./cmd/cairn/ -run TestClient -v
```

Expected: FAIL — `undefined: NewClient, PostAgentRequest, APIError, ListAgents, Approve, Block`.

- [ ] **Step 8: Implement `cmd/cairn/client.go`**

```go
package cairn

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Client is a thin wrapper around the Cairn /api/cairn/v1/ endpoints.
//
// Constructed with the instance URL (e.g. "https://cairn.darksoft.co.nz")
// and an auth token. The token may be empty for endpoints that don't
// require auth (e.g. POST /agents anonymously, GET /agents/:fp/identity).
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// NewClient constructs a Client. Use *http.DefaultClient by default.
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL: baseURL,
		Token:   token,
		HTTP:    http.DefaultClient,
	}
}

// APIError is returned when the Cairn server responds with a non-2xx
// status. Carries the HTTP status code, the structured error code from
// the response body, and the human message.
type APIError struct {
	StatusCode int
	ErrorCode  string
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("cairn api: %d %s: %s", e.StatusCode, e.ErrorCode, e.Message)
	}
	return fmt.Sprintf("cairn api: %d %s", e.StatusCode, e.ErrorCode)
}

// AgentResponse is the wire shape returned by the API. Mirrors
// AgentJSON in routers/api/cairn/v1/types.go.
type AgentResponse struct {
	Fingerprint  string `json:"fingerprint"`
	OwnerName    string `json:"owner"`
	Slug         string `json:"slug"`
	Domain       string `json:"domain"`
	PublicKeyHex string `json:"public_key"`
	Status       string `json:"status"`
	Blocked      bool   `json:"blocked"`
	CreatedAt    string `json:"created_at"`
	ActivatedAt  string `json:"activated_at,omitempty"`
}

// PostAgentRequest is the input to the registration endpoint.
type PostAgentRequest struct {
	ProposedOwner string
	Slug          string
	Domain        string
	PublicKey     ed25519.PublicKey
}

// PostAgent registers an agent. Returns the server's response on
// success; *APIError on non-2xx; other errors for transport/JSON
// failures.
func (c *Client) PostAgent(ctx context.Context, in PostAgentRequest) (*AgentResponse, error) {
	body, err := json.Marshal(map[string]string{
		"proposed_owner": in.ProposedOwner,
		"slug":           in.Slug,
		"domain":         in.Domain,
		"public_key":     hex.EncodeToString(in.PublicKey),
	})
	if err != nil {
		return nil, err
	}

	var out AgentResponse
	if err := c.do(ctx, http.MethodPost, "/api/cairn/v1/agents", bytes.NewReader(body), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetIdentity fetches an agent's public-facing metadata by fingerprint.
func (c *Client) GetIdentity(ctx context.Context, fingerprint string) (*AgentResponse, error) {
	path := "/api/cairn/v1/agents/" + url.PathEscape(fingerprint) + "/identity"
	var out AgentResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListAgents returns the current user's agents. status is "" for all,
// or "pending"/"active".
func (c *Client) ListAgents(ctx context.Context, status string) ([]AgentResponse, error) {
	path := "/api/cairn/v1/agents"
	if status != "" {
		path += "?status=" + url.QueryEscape(status)
	}
	var out []AgentResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Approve transitions a pending agent to active. Owner-only.
func (c *Client) Approve(ctx context.Context, fingerprint string) error {
	path := "/api/cairn/v1/agents/" + url.PathEscape(fingerprint) + "/approve"
	return c.do(ctx, http.MethodPost, path, nil, nil)
}

// Block adds an agent to the blocklist. Owner-only.
func (c *Client) Block(ctx context.Context, fingerprint, reason string) error {
	path := "/api/cairn/v1/agents/" + url.PathEscape(fingerprint) + "/block"
	body, _ := json.Marshal(map[string]string{"reason": reason})
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(body), nil)
}

// do is the shared request shape: build, set auth + content-type,
// execute, parse status + body. If decodeInto is non-nil, decode the
// response body into it.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, decodeInto any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "token "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errBody struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return &APIError{
			StatusCode: resp.StatusCode,
			ErrorCode:  errBody.Error,
			Message:    errBody.Message,
		}
	}

	if decodeInto != nil {
		if err := json.NewDecoder(resp.Body).Decode(decodeInto); err != nil {
			return fmt.Errorf("cairn api: decode response: %w", err)
		}
	}
	return nil
}
```

- [ ] **Step 9: Run, expect PASS**

```bash
go test ./cmd/cairn/...
```

Expected: 4 client tests + 6 config tests = 10 tests pass.

- [ ] **Step 10: Commit + push feature branch**

```bash
git add cmd/cairn/
git commit -m "$(cat <<'EOF'
feat(cairn): CLI infrastructure — paths, HTTP client

Establishes the shared CLI plumbing every Cairn subcommand will
build on:

cmd/cairn/config.go — Paths struct + ResolvePaths(instanceURL).
Resolves $XDG_CONFIG_HOME/cairn/<host>/ (or ~/.config/cairn/<host>/),
exposes SeedFile, TokenFile, KeyFile(slug), EnsureHostDir (0700),
ReadSeed (rejects insecure mode), WriteToken/ReadToken (0600).

cmd/cairn/client.go — Client wrapping /api/cairn/v1/. Methods:
PostAgent, GetIdentity, ListAgents, Approve, Block. APIError
typed error preserves status code + structured error code. All
auth via "Authorization: token <tok>" header (matching Forgejo's
existing scheme). Content-Type and request-body framing handled
in a single internal do() helper.

10 tests cover path resolution under XDG vs HOME, malformed URL
rejection, mode-0700 enforcement on host dir, and httptest-backed
client round-trips for all five API methods.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §4.8

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push -u origin cairn-cli-infrastructure
```

Don't merge to cairn — controller will do it.

---

## Task 2: `cairn auth login` subcommand

**Files:**
- Create: `cmd/cairn/auth_login.go` — the login action handler
- Create: `cmd/cairn/auth_login_test.go` — tests using httptest

**Why:** Operators need a way to obtain and persist a Forgejo API token for subsequent CLI calls. Forgejo exposes `/api/v1/users/:username/tokens` (basic-auth protected) for token creation; this subcommand POSTs to it, stores the result.

- [ ] **Step 1: Branch**

```bash
cd ~/Source/cairn && git checkout cairn && git pull
git checkout -b cairn-cli-auth-login
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write the failing test in `cmd/cairn/auth_login_test.go`**

```go
package cairn

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthLogin_StoresToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/users/") {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q", r.Method)
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "alice" || pass != "secret" {
			t.Errorf("basic auth = (%q, %q, %v)", user, pass, ok)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sha1": "fake-token-sha1",
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := AuthLogin(srv.URL, "alice", "secret", "cairn-cli"); err != nil {
		t.Fatal(err)
	}

	paths, _ := ResolvePaths(srv.URL)
	stored, err := paths.ReadToken()
	if err != nil {
		t.Fatal(err)
	}
	if stored != "fake-token-sha1" {
		t.Errorf("stored token = %q, want fake-token-sha1", stored)
	}

	info, _ := os.Stat(paths.TokenFile)
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("token mode = %#o, want 0600", perm)
	}
}

func TestAuthLogin_RejectsBadCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	err := AuthLogin(srv.URL, "alice", "wrong", "cairn-cli")
	if err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestAuthLogin_DoesNotWriteTokenOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	_ = AuthLogin(srv.URL, "alice", "wrong", "cairn-cli")

	paths, _ := ResolvePaths(srv.URL)
	if _, err := os.Stat(paths.TokenFile); !os.IsNotExist(err) {
		t.Errorf("token file exists after failed login: stat err=%v", err)
	}
	_ = filepath.Join // keep import
}
```

- [ ] **Step 3: Run, expect failure**

`undefined: AuthLogin`.

- [ ] **Step 4: Implement `cmd/cairn/auth_login.go`**

```go
package cairn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// AuthLogin obtains an API token from the Cairn instance using HTTP
// basic auth (username + password) against Forgejo's standard token
// creation endpoint, then stores the token at the per-host token path.
//
// On success the file at <hostdir>/token contains the token text and
// has mode 0600. On any non-2xx response or network error, no file is
// written.
func AuthLogin(instanceURL, username, password, tokenName string) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}

	body, _ := json.Marshal(map[string]string{
		"name": tokenName,
	})

	endpoint := instanceURL + "/api/v1/users/" + url.PathEscape(username) + "/tokens"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(username, password)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("cairn auth: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("cairn auth: token creation failed: HTTP %d", resp.StatusCode)
	}

	var out struct {
		SHA1 string `json:"sha1"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("cairn auth: decode response: %w", err)
	}
	if out.SHA1 == "" {
		return fmt.Errorf("cairn auth: empty token in response")
	}

	return paths.WriteToken(out.SHA1)
}
```

- [ ] **Step 5: Run, expect PASS**

```bash
go test ./cmd/cairn/ -run TestAuthLogin -v
```

- [ ] **Step 6: Commit**

```bash
git add cmd/cairn/auth_login.go cmd/cairn/auth_login_test.go
git commit -m "feat(cairn): cairn auth login subcommand

Stores an API token at <config>/<host>/token (mode 0600) by POSTing
to Forgejo's /api/v1/users/<name>/tokens endpoint with basic auth.

3 tests cover happy path, 401 rejection, and confirmation that the
token file is never written on failure.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §4.8

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
git push -u origin cairn-cli-auth-login
```

---

## Task 3: `cairn agent init` subcommand

**Files:**
- Create: `cmd/cairn/agent_init.go`
- Create: `cmd/cairn/agent_init_test.go`

**Why:** Generate the agent keypair locally via casket-go's HKDF derivation, persist as an OpenSSH-format private key file (so vanilla `ssh-keygen` could verify), print the metadata the operator needs.

For MVP, we DO persist the private key locally (`<slug>.key`) — the spec mentions "no per-agent key files to provision" via the commit-sign-helper path, but the helper still needs a way to identify which agent to sign as. We persist a public-key file so git can use it as `user.signingkey`; the helper derives the private key on demand from the seed.

- [ ] **Step 1: Branch**

```bash
cd ~/Source/cairn && git checkout cairn && git pull
git checkout -b cairn-cli-agent-init
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write the failing test in `cmd/cairn/agent_init_test.go`**

```go
package cairn

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"strings"
	"testing"
)

// writeTestSeed places a 32-byte seed at the seed path with mode 0600.
func writeTestSeed(t *testing.T, paths *Paths) {
	t.Helper()
	if err := os.MkdirAll(paths.ConfigRoot, 0700); err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SeedFile, seed, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentInit_WritesPubKeyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	paths, _ := ResolvePaths("https://cairn.example.com")
	writeTestSeed(t, paths)

	out := &bytes.Buffer{}
	if err := AgentInit("https://cairn.example.com", "plumb", "darksoft.co.nz", out); err != nil {
		t.Fatal(err)
	}

	pubFile := paths.KeyFile("plumb") + ".pub"
	pubBytes, err := os.ReadFile(pubFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pubBytes, []byte("ssh-ed25519 ")) {
		t.Errorf("pubkey file does not start with ssh-ed25519: %q", pubBytes[:32])
	}

	info, _ := os.Stat(pubFile)
	if perm := info.Mode().Perm(); perm != 0644 {
		t.Errorf("pubkey perm = %#o, want 0644", perm)
	}

	// Output contains slug + email + a fingerprint hint.
	s := out.String()
	for _, want := range []string{"slug:", "plumb", "email:", "nexus-plumb@darksoft.co.nz"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q\noutput=%s", want, s)
		}
	}
}

func TestAgentInit_RejectsMissingSeed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	out := &bytes.Buffer{}
	err := AgentInit("https://cairn.example.com", "plumb", "darksoft.co.nz", out)
	if err == nil {
		t.Error("expected error for missing seed file")
	}
}

func TestAgentInit_DeterministicKeypair(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	paths, _ := ResolvePaths("https://cairn.example.com")
	writeTestSeed(t, paths)

	out1 := &bytes.Buffer{}
	if err := AgentInit("https://cairn.example.com", "plumb", "darksoft.co.nz", out1); err != nil {
		t.Fatal(err)
	}
	pub1, _ := os.ReadFile(paths.KeyFile("plumb") + ".pub")

	// Re-run init with same slug + same seed.
	out2 := &bytes.Buffer{}
	if err := AgentInit("https://cairn.example.com", "plumb", "darksoft.co.nz", out2); err != nil {
		t.Fatal(err)
	}
	pub2, _ := os.ReadFile(paths.KeyFile("plumb") + ".pub")

	if !bytes.Equal(pub1, pub2) {
		t.Error("re-run produced different pubkey; HKDF derivation is non-deterministic?")
	}
	_ = ed25519.PublicKeySize // keep import
}
```

- [ ] **Step 3: Run, expect failure**

`undefined: AgentInit`.

- [ ] **Step 4: Implement `cmd/cairn/agent_init.go`**

```go
package cairn

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	casket "github.com/CarriedWorldUniverse/casket-go"
)

// AgentInit derives the agent keypair from the owner's seed file via
// HKDF + Ed25519, writes the OpenSSH-format public key to
// <hostdir>/<slug>.key.pub, and prints metadata to out.
//
// Private key is NOT persisted — commit-sign-helper re-derives it on
// each signing invocation. The pubkey file exists so git's
// gpg.format=ssh signing flow has something to point at via
// user.signingkey.
func AgentInit(instanceURL, slug, domain string, out io.Writer) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	if err := paths.EnsureHostDir(); err != nil {
		return err
	}

	seed, err := paths.ReadSeed()
	if err != nil {
		return err
	}

	_, pub, err := casket.DeriveAgentKey(seed, slug)
	if err != nil {
		return err
	}

	pubFile := paths.KeyFile(slug) + ".pub"
	pubLine := openSSHPublicKey(pub, slug+"@"+domain)
	if err := os.WriteFile(pubFile, []byte(pubLine), 0644); err != nil {
		return fmt.Errorf("cairn agent init: write pubkey: %w", err)
	}

	email := "nexus-" + slug + "@" + domain
	fmt.Fprintf(out, "slug: %s\n", slug)
	fmt.Fprintf(out, "domain: %s\n", domain)
	fmt.Fprintf(out, "email: %s\n", email)
	fmt.Fprintf(out, "public_key_file: %s\n", pubFile)
	fmt.Fprintf(out, "next: cairn agent submit --instance %s\n", instanceURL)

	return nil
}

// openSSHPublicKey serialises an Ed25519 public key in the OpenSSH
// authorized_keys format: "ssh-ed25519 <base64-blob> <comment>\n".
//
// The blob is the SSH wire format: length-prefixed "ssh-ed25519" string
// followed by the length-prefixed 32-byte raw key.
func openSSHPublicKey(pub ed25519.PublicKey, comment string) string {
	keytype := []byte("ssh-ed25519")

	buf := make([]byte, 0, 4+len(keytype)+4+len(pub))
	buf = appendString(buf, keytype)
	buf = appendString(buf, pub)

	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(buf) + " " + comment + "\n"
}

func appendString(buf, s []byte) []byte {
	var lenBytes [4]byte
	binary.BigEndian.PutUint32(lenBytes[:], uint32(len(s)))
	buf = append(buf, lenBytes[:]...)
	buf = append(buf, s...)
	return buf
}
```

- [ ] **Step 5: Run, expect PASS**

```bash
go test ./cmd/cairn/ -run TestAgentInit -v
```

- [ ] **Step 6: Commit**

```bash
git add cmd/cairn/agent_init.go cmd/cairn/agent_init_test.go
git commit -m "feat(cairn): cairn agent init — derive + persist agent pubkey

Reads the owner's seed file (~/.config/cairn/seed, mode 0600 enforced),
derives the Ed25519 keypair via casket.DeriveAgentKey(seed, slug),
writes the OpenSSH-format public key to <hostdir>/<slug>.key.pub
(mode 0644), and prints the metadata an operator needs.

Private key is not persisted: commit-sign-helper re-derives on each
signing call. The pubkey file is what git's gpg.format=ssh signing
flow points to via user.signingkey.

3 tests: pubkey-file written with ssh-ed25519 prefix and 0644 mode;
missing-seed rejection; deterministic re-derivation across runs.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6, §4.8

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
git push -u origin cairn-cli-agent-init
```

---

## Task 4: `cairn agent submit` subcommand

**Files:**
- Create: `cmd/cairn/agent_submit.go`
- Create: `cmd/cairn/agent_submit_test.go`

**Why:** Sends the registration request to Cairn's `POST /api/cairn/v1/agents` endpoint using the stored token. Acts on whatever slug+domain were used in `cairn agent init`.

- [ ] **Step 1: Branch**

```bash
cd ~/Source/cairn && git checkout cairn && git pull
git checkout -b cairn-cli-agent-submit
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write the failing test**

```go
// cmd/cairn/agent_submit_test.go
package cairn

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestAgentSubmit_PostsRegistration(t *testing.T) {
	got := struct {
		ProposedOwner string `json:"proposed_owner"`
		Slug          string `json:"slug"`
		Domain        string `json:"domain"`
		PublicKeyHex  string `json:"public_key"`
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"fingerprint": "cairn:test-fp",
			"slug":        "plumb",
			"status":      "active",
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	paths, _ := ResolvePaths(srv.URL)
	writeTestSeed(t, paths)

	// Set up a token + simulate a prior "agent init"
	if err := paths.WriteToken("test-tok"); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	if err := AgentSubmit(srv.URL, "alice", "plumb", "darksoft.co.nz", out); err != nil {
		t.Fatal(err)
	}

	if got.ProposedOwner != "alice" || got.Slug != "plumb" || got.Domain != "darksoft.co.nz" {
		t.Errorf("posted body = %+v", got)
	}
	if got.PublicKeyHex == "" || len(got.PublicKeyHex) != 64 {
		t.Errorf("hex pubkey length = %d, want 64", len(got.PublicKeyHex))
	}

	// Output contains the assigned fingerprint.
	if !strings.Contains(out.String(), "cairn:test-fp") {
		t.Errorf("output missing fingerprint:\n%s", out.String())
	}
}

func TestAgentSubmit_FailsWithoutToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	paths, _ := ResolvePaths("https://cairn.example.com")
	writeTestSeed(t, paths)

	out := &bytes.Buffer{}
	err := AgentSubmit("https://cairn.example.com", "alice", "plumb", "darksoft.co.nz", out)
	if err == nil {
		t.Error("expected error when token is missing")
	}
	_ = os.Stat // keep import
}

func TestAgentSubmit_AnonymousIsAllowed(t *testing.T) {
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"fingerprint": "cairn:anon-fp",
			"status":      "pending",
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	paths, _ := ResolvePaths(srv.URL)
	writeTestSeed(t, paths)
	// Note: no WriteToken — anonymous mode

	out := &bytes.Buffer{}
	err := AgentSubmitAnonymous(srv.URL, "alice", "plumb", "darksoft.co.nz", out)
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "" {
		t.Errorf("anonymous submit sent Authorization header: %q", gotAuth)
	}
	if !strings.Contains(out.String(), "pending") {
		t.Errorf("output missing pending status:\n%s", out.String())
	}
}
```

- [ ] **Step 3: Run, expect failure**

- [ ] **Step 4: Implement `cmd/cairn/agent_submit.go`**

```go
package cairn

import (
	"context"
	"fmt"
	"io"

	casket "github.com/CarriedWorldUniverse/casket-go"
)

// AgentSubmit posts a registration request as the authed user
// (token from <hostdir>/token). Auto-approve happens server-side
// when the auth user matches proposed_owner.
func AgentSubmit(instanceURL, proposedOwner, slug, domain string, out io.Writer) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	token, err := paths.ReadToken()
	if err != nil {
		return err
	}
	return submit(instanceURL, token, proposedOwner, slug, domain, paths, out)
}

// AgentSubmitAnonymous posts a registration without a token. The
// resulting agent goes to pending status; the proposed owner must
// approve via web UI, cairn agents approve, or the API directly.
func AgentSubmitAnonymous(instanceURL, proposedOwner, slug, domain string, out io.Writer) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	return submit(instanceURL, "", proposedOwner, slug, domain, paths, out)
}

func submit(instanceURL, token, proposedOwner, slug, domain string, paths *Paths, out io.Writer) error {
	seed, err := paths.ReadSeed()
	if err != nil {
		return err
	}
	_, pub, err := casket.DeriveAgentKey(seed, slug)
	if err != nil {
		return err
	}

	c := NewClient(instanceURL, token)
	resp, err := c.PostAgent(context.Background(), PostAgentRequest{
		ProposedOwner: proposedOwner,
		Slug:          slug,
		Domain:        domain,
		PublicKey:     pub,
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "fingerprint: %s\n", resp.Fingerprint)
	fmt.Fprintf(out, "status: %s\n", resp.Status)
	if resp.Status == "pending" {
		fmt.Fprintf(out, "note: awaiting %s's approval — owner can run `cairn agents approve %s`\n",
			proposedOwner, resp.Fingerprint)
	}
	return nil
}
```

- [ ] **Step 5: Run, expect PASS**

- [ ] **Step 6: Commit**

```bash
git add cmd/cairn/agent_submit.go cmd/cairn/agent_submit_test.go
git commit -m "feat(cairn): cairn agent submit — POST registration to Cairn

Two entry points:
- AgentSubmit: uses stored token (auto-approves when authed-as-owner)
- AgentSubmitAnonymous: no token, agent lands in pending state

Both derive the keypair from the seed via casket.DeriveAgentKey,
hex-encode the pubkey, and POST to /api/cairn/v1/agents via the
shared Client. Output reports the assigned fingerprint and status,
with a hint for the pending approval flow.

3 tests cover authed submit, anonymous submit, and missing-token
rejection.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6, §4.8

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
git push -u origin cairn-cli-agent-submit
```

---

## Task 5: `cairn agents list / approve / block` subcommands

**Files:**
- Create: `cmd/cairn/agents_admin.go` — list, approve, block actions
- Create: `cmd/cairn/agents_admin_test.go`

**Why:** Owner-side admin commands. Each is a thin wrapper over the corresponding Client method.

- [ ] **Step 1: Branch**

```bash
cd ~/Source/cairn && git checkout cairn && git pull
git checkout -b cairn-cli-agents-admin
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write tests in `cmd/cairn/agents_admin_test.go`**

```go
package cairn

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentsList_PrintsTable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"fingerprint": "cairn:fp1", "slug": "plumb", "domain": "darksoft.co.nz", "status": "active", "blocked": false},
			{"fingerprint": "cairn:fp2", "slug": "anvil", "domain": "darksoft.co.nz", "status": "pending", "blocked": false},
		})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	paths, _ := ResolvePaths(srv.URL)
	_ = paths.WriteToken("tok")

	out := &bytes.Buffer{}
	if err := AgentsList(srv.URL, "", out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"plumb", "anvil", "active", "pending"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q\noutput=%s", want, s)
		}
	}
}

func TestAgentsApprove_CallsEndpoint(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/approve") {
			called = true
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"fingerprint": "cairn:fp1", "status": "active"})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	paths, _ := ResolvePaths(srv.URL)
	_ = paths.WriteToken("tok")

	out := &bytes.Buffer{}
	if err := AgentsApprove(srv.URL, "cairn:fp1", out); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Error("approve endpoint not called")
	}
}

func TestAgentsBlock_CallsEndpoint(t *testing.T) {
	body := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"fingerprint": "cairn:fp1", "status": "active"})
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	paths, _ := ResolvePaths(srv.URL)
	_ = paths.WriteToken("tok")

	out := &bytes.Buffer{}
	if err := AgentsBlock(srv.URL, "cairn:fp1", "key compromised", out); err != nil {
		t.Fatal(err)
	}
	if body["reason"] != "key compromised" {
		t.Errorf("reason = %q, want 'key compromised'", body["reason"])
	}
}
```

- [ ] **Step 3: Implement `cmd/cairn/agents_admin.go`**

```go
package cairn

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
)

// AgentsList prints the current user's agents as a table to out.
// status is "" for all, or "pending" / "active".
func AgentsList(instanceURL, status string, out io.Writer) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	token, err := paths.ReadToken()
	if err != nil {
		return err
	}

	c := NewClient(instanceURL, token)
	agents, err := c.ListAgents(context.Background(), status)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SLUG\tDOMAIN\tSTATUS\tBLOCKED\tFINGERPRINT")
	for _, a := range agents {
		blocked := "no"
		if a.Blocked {
			blocked = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", a.Slug, a.Domain, a.Status, blocked, a.Fingerprint)
	}
	return tw.Flush()
}

// AgentsApprove approves a pending agent. Owner-only on the server.
func AgentsApprove(instanceURL, fingerprint string, out io.Writer) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	token, err := paths.ReadToken()
	if err != nil {
		return err
	}
	c := NewClient(instanceURL, token)
	if err := c.Approve(context.Background(), fingerprint); err != nil {
		return err
	}
	fmt.Fprintf(out, "approved: %s\n", fingerprint)
	return nil
}

// AgentsBlock adds an agent to the blocklist with a reason. Owner-only.
func AgentsBlock(instanceURL, fingerprint, reason string, out io.Writer) error {
	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	token, err := paths.ReadToken()
	if err != nil {
		return err
	}
	c := NewClient(instanceURL, token)
	if err := c.Block(context.Background(), fingerprint, reason); err != nil {
		return err
	}
	fmt.Fprintf(out, "blocked: %s (reason: %s)\n", fingerprint, reason)
	return nil
}
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./cmd/cairn/ -run "TestAgents" -v
```

- [ ] **Step 5: Commit**

```bash
git add cmd/cairn/agents_admin.go cmd/cairn/agents_admin_test.go
git commit -m "feat(cairn): cairn agents list/approve/block subcommands

AgentsList prints a tabwriter-formatted table of the caller's agents
(slug, domain, status, blocked, fingerprint). Optional status filter.

AgentsApprove + AgentsBlock are thin wrappers over the corresponding
Client methods, with friendly stdout output on success.

3 tests cover the table output, approve endpoint hit, and block
reason in request body.

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §4.8

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
git push -u origin cairn-cli-agents-admin
```

---

## Task 6: `cairn commit-sign-helper` subcommand

**Files:**
- Create: `cmd/cairn/commit_sign_helper.go`
- Create: `cmd/cairn/commit_sign_helper_test.go`

**Why:** Spec §6 — git invokes a signing program in `gpg.format=ssh` mode with `gpg.ssh.program=cairn commit-sign-helper`. The helper reads stdin (the data to sign), derives the agent's private key from the seed, signs in SSH-signature wire format (PROTOCOL.sshsig), writes to stdout. Private key never persists to disk.

- [ ] **Step 1: Branch**

```bash
cd ~/Source/cairn && git checkout cairn && git pull
git checkout -b cairn-cli-commit-sign-helper
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Write the failing test**

```go
// cmd/cairn/commit_sign_helper_test.go
package cairn

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	casket "github.com/CarriedWorldUniverse/casket-go"
)

func TestCommitSignHelper_ProducesValidSSHSignature(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	paths, _ := ResolvePaths("https://cairn.example.com")
	writeTestSeed(t, paths)

	stdin := bytes.NewReader([]byte("commit blob to sign"))
	stdout := &bytes.Buffer{}

	if err := CommitSignHelper("https://cairn.example.com", "plumb", "git", stdin, stdout); err != nil {
		t.Fatal(err)
	}

	out := stdout.String()
	if !strings.Contains(out, "BEGIN SSH SIGNATURE") {
		t.Errorf("output missing SSH SIGNATURE armor:\n%s", out)
	}

	// Parse the signature and verify against the agent's pubkey.
	seed, _ := paths.ReadSeed()
	_, pub, _ := casket.DeriveAgentKey(seed, "plumb")
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}

	sig, err := parseSSHSignature(stdout.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySSHSignature(sshPub, []byte("commit blob to sign"), sig, "git"); err != nil {
		t.Errorf("signature did not verify: %v", err)
	}
}

func TestCommitSignHelper_RequiresSeed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	stdin := bytes.NewReader([]byte("data"))
	stdout := &bytes.Buffer{}

	err := CommitSignHelper("https://cairn.example.com", "plumb", "git", stdin, stdout)
	if err == nil {
		t.Error("expected error when seed file is missing")
	}
}

// parseSSHSignature and verifySSHSignature are TEST helpers — they
// validate the helper's output against golang.org/x/crypto/ssh.
//
// PROTOCOL.sshsig structure:
//   "SSHSIG"          (6 bytes magic)
//   uint32 version    (1)
//   string publickey  (length-prefixed wire-format pubkey)
//   string namespace
//   string reserved
//   string hash_algorithm
//   string signature
func parseSSHSignature(armored []byte) (*ssh.Signature, error) {
	// Strip PEM-style armor and base64-decode the contents.
	// (The actual implementation lives in commit_sign_helper.go.)
	return parseSSHSignatureBlob(armored)
}

func verifySSHSignature(pub ssh.PublicKey, data []byte, sig *ssh.Signature, namespace string) error {
	// Wrap the data into the same SSHSIG signed-data format the
	// signing helper used, then ssh.PublicKey.Verify against it.
	return verifySSHSignedData(pub, data, sig, namespace)
}
```

- [ ] **Step 3: Implement `cmd/cairn/commit_sign_helper.go`**

This implementation requires PROTOCOL.sshsig serialisation. The skeleton:

```go
package cairn

import (
	"bytes"
	"crypto/sha512"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"

	casket "github.com/CarriedWorldUniverse/casket-go"
)

// sshSigMagic is the leading bytes of an SSHSIG-wrapped blob per
// PROTOCOL.sshsig.
const sshSigMagic = "SSHSIG"

// CommitSignHelper reads data from stdin, derives the agent's keypair
// from the owner's seed file via casket.DeriveAgentKey(seed, slug),
// produces an SSH-format signature in the given namespace (typically
// "git"), and writes the PEM-armored result to stdout.
//
// This is the function git invokes when configured with
//
//	gpg.format       = ssh
//	gpg.ssh.program  = cairn commit-sign-helper --slug <slug>
//
// The private key is never persisted to disk — it is derived on each
// signing call and discarded when the function returns.
func CommitSignHelper(instanceURL, slug, namespace string, in io.Reader, out io.Writer) error {
	if namespace == "" {
		namespace = "git"
	}

	paths, err := ResolvePaths(instanceURL)
	if err != nil {
		return err
	}
	seed, err := paths.ReadSeed()
	if err != nil {
		return err
	}

	priv, _, err := casket.DeriveAgentKey(seed, slug)
	if err != nil {
		return err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return err
	}

	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}

	armored, err := signSSHSig(signer, data, namespace)
	if err != nil {
		return err
	}
	_, err = out.Write(armored)
	return err
}

// signSSHSig produces an SSHSIG-armored signature over data per
// PROTOCOL.sshsig.
func signSSHSig(signer ssh.Signer, data []byte, namespace string) ([]byte, error) {
	hashed := sha512.Sum512(data)
	signedBlob := signedDataBlob(signer.PublicKey(), hashed[:], namespace)

	sig, err := signer.Sign(nil, signedBlob)
	if err != nil {
		return nil, err
	}

	// PROTOCOL.sshsig outer envelope.
	var buf bytes.Buffer
	buf.WriteString(sshSigMagic)
	writeUint32(&buf, 1) // version
	writeString(&buf, signer.PublicKey().Marshal())
	writeString(&buf, []byte(namespace))
	writeString(&buf, nil) // reserved
	writeString(&buf, []byte("sha512"))
	writeString(&buf, ssh.Marshal(sig))

	return pem.EncodeToMemory(&pem.Block{
		Type:  "SSH SIGNATURE",
		Bytes: buf.Bytes(),
	}), nil
}

// signedDataBlob is the data the signer actually signs. Per
// PROTOCOL.sshsig: MAGIC + namespace + reserved + hash_algo + hashed_data.
func signedDataBlob(_ ssh.PublicKey, hashed []byte, namespace string) []byte {
	var buf bytes.Buffer
	buf.WriteString(sshSigMagic)
	writeString(&buf, []byte(namespace))
	writeString(&buf, nil)
	writeString(&buf, []byte("sha512"))
	writeString(&buf, hashed)
	return buf.Bytes()
}

// writeString writes a length-prefixed string (SSH wire format).
func writeString(buf *bytes.Buffer, s []byte) {
	var lenBytes [4]byte
	binary.BigEndian.PutUint32(lenBytes[:], uint32(len(s)))
	buf.Write(lenBytes[:])
	buf.Write(s)
}

func writeUint32(buf *bytes.Buffer, n uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], n)
	buf.Write(b[:])
}

// parseSSHSignatureBlob is used by tests to round-trip-verify our
// own output. Returns the inner ssh.Signature.
func parseSSHSignatureBlob(armored []byte) (*ssh.Signature, error) {
	block, _ := pem.Decode(armored)
	if block == nil || block.Type != "SSH SIGNATURE" {
		return nil, errors.New("not an SSH SIGNATURE PEM block")
	}
	r := bytes.NewReader(block.Bytes)

	magic := make([]byte, len(sshSigMagic))
	if _, err := io.ReadFull(r, magic); err != nil || string(magic) != sshSigMagic {
		return nil, fmt.Errorf("missing SSHSIG magic")
	}
	var version uint32
	if err := binary.Read(r, binary.BigEndian, &version); err != nil {
		return nil, err
	}
	// Skip publickey, namespace, reserved, hash_algorithm strings.
	for i := 0; i < 4; i++ {
		if _, err := readString(r); err != nil {
			return nil, err
		}
	}
	sigBytes, err := readString(r)
	if err != nil {
		return nil, err
	}
	var sig ssh.Signature
	if err := ssh.Unmarshal(sigBytes, &sig); err != nil {
		return nil, err
	}
	return &sig, nil
}

func verifySSHSignedData(pub ssh.PublicKey, data []byte, sig *ssh.Signature, namespace string) error {
	hashed := sha512.Sum512(data)
	signedBlob := signedDataBlob(pub, hashed[:], namespace)
	return pub.Verify(signedBlob, sig)
}

func readString(r *bytes.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}
```

- [ ] **Step 4: Run, expect PASS**

```bash
go test ./cmd/cairn/ -run TestCommitSignHelper -v
```

- [ ] **Step 5: Commit**

```bash
git add cmd/cairn/commit_sign_helper.go cmd/cairn/commit_sign_helper_test.go
git commit -m "feat(cairn): cairn commit-sign-helper — git ssh-signing helper

Implements PROTOCOL.sshsig (the SSH SIGNATURE format git uses with
gpg.format=ssh). Reads stdin, derives the agent's private key from
the seed file via casket.DeriveAgentKey on each invocation (key never
persists), signs in the given namespace (typically 'git'), writes
PEM-armored SSH SIGNATURE to stdout.

Round-trip-verified in tests using golang.org/x/crypto/ssh.

This is what git invokes when the operator configures:
  gpg.format       = ssh
  gpg.ssh.program  = cairn commit-sign-helper --slug <slug>

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §6, §4.8

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
git push -u origin cairn-cli-commit-sign-helper
```

---

## Task 7: Subcommand registration in Forgejo's CLI tree

**Files:**
- Create: `cmd/cairn/cli.go` — urfave/cli command definitions wrapping the action functions
- Modify: Forgejo's main `cmd/` registration (typically `cmd/cmd.go` or similar) — add the `cairn` subcommand group

**Why:** Wire the action functions (AuthLogin, AgentInit, AgentSubmit, AgentsList/Approve/Block, CommitSignHelper) into urfave/cli `cli.Command` definitions, then register them under a top-level `cairn` group in Forgejo's main CLI.

This task touches Forgejo upstream (small additive patch — one line in the main commands slice). Verify the actual urfave/cli version and command registration pattern in this Forgejo checkout before writing.

- [ ] **Step 1: Branch**

```bash
cd ~/Source/cairn && git checkout cairn && git pull
git checkout -b cairn-cli-registration
git config user.name "nexus-cw"
git config user.email "nexus@darksoft.co.nz"
```

- [ ] **Step 2: Inspect Forgejo's CLI structure**

```bash
ls cmd/
grep -ln 'cli.Command{' cmd/ | head -5
grep -n 'cli.Commands\|App.Commands' cmd/main.go cmd/cmd.go 2>/dev/null | head
```

Find where the commands slice is built. Document the actual entry point in your commit message.

- [ ] **Step 3: Implement `cmd/cairn/cli.go`**

The exact urfave/cli API depends on the version. For v2 / v3 (skeleton — adapt to actual):

```go
package cairn

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v2" // or v3 — verify
)

// Commands returns the cairn subcommand group for registration with
// Forgejo's main CLI.
func Commands() *cli.Command {
	return &cli.Command{
		Name:  "cairn",
		Usage: "Cairn agent administration",
		Subcommands: []*cli.Command{
			{
				Name:  "auth",
				Usage: "Authentication",
				Subcommands: []*cli.Command{
					authLoginCmd(),
				},
			},
			{
				Name:  "agent",
				Usage: "Per-agent operations",
				Subcommands: []*cli.Command{
					agentInitCmd(),
					agentSubmitCmd(),
				},
			},
			{
				Name:  "agents",
				Usage: "Owner-side agent admin",
				Subcommands: []*cli.Command{
					agentsListCmd(),
					agentsApproveCmd(),
					agentsBlockCmd(),
				},
			},
			commitSignHelperCmd(),
		},
	}
}

var (
	flagInstance = &cli.StringFlag{
		Name:     "instance",
		Usage:    "Cairn instance URL (e.g. https://cairn.darksoft.co.nz)",
		Required: true,
	}
	flagSlug = &cli.StringFlag{Name: "slug", Required: true}
	flagDomain = &cli.StringFlag{Name: "domain", Required: true}
)

func authLoginCmd() *cli.Command {
	return &cli.Command{
		Name:  "login",
		Usage: "Obtain and store an API token",
		Flags: []cli.Flag{
			flagInstance,
			&cli.StringFlag{Name: "username", Required: true},
			&cli.StringFlag{Name: "password", Required: true},
			&cli.StringFlag{Name: "token-name", Value: "cairn-cli"},
		},
		Action: func(c *cli.Context) error {
			return AuthLogin(
				c.String("instance"),
				c.String("username"),
				c.String("password"),
				c.String("token-name"),
			)
		},
	}
}

// agentInitCmd, agentSubmitCmd, agentsListCmd, agentsApproveCmd,
// agentsBlockCmd, commitSignHelperCmd: similar pattern. Each wraps
// the corresponding action function. commitSignHelperCmd reads stdin
// directly (os.Stdin) and writes to stdout.

// ... rest of command definitions ...
```

Repeat the pattern for each command. Each `Action` func reads flags and calls the appropriate function from Tasks 2-6.

- [ ] **Step 4: Register in Forgejo's main CLI**

Find the file with the commands slice (e.g. `cmd/cmd.go` or `cmd/main.go`). Add:

```go
import cairncmd "github.com/CarriedWorldUniverse/cairn/cmd/cairn"

// In the commands slice / app definition:
app.Commands = append(app.Commands, cairncmd.Commands())
```

Adjust to whatever the actual Forgejo entry-point pattern is.

- [ ] **Step 5: Smoke test against built binary**

```bash
go build -o /tmp/cairn-cli-test .

/tmp/cairn-cli-test cairn --help
# Should show: auth, agent, agents, commit-sign-helper subcommands

/tmp/cairn-cli-test cairn agent --help
# Should show: init, submit
```

- [ ] **Step 6: Commit**

```bash
git add cmd/cairn/cli.go cmd/cmd.go  # or wherever the registration touched
git commit -m "feat(cairn): register cairn CLI subcommands in Forgejo's CLI tree

cmd/cairn/cli.go — urfave/cli Command definitions wrapping the
action functions from Tasks 2-6: auth login, agent init/submit,
agents list/approve/block, commit-sign-helper.

cmd/<entry>.go — small additive patch appending Cairn's subcommand
group to Forgejo's main app.Commands slice.

Smoke-tested: cairn binary now exposes 'cairn cairn ...'
subcommand tree. (Yes the double-cairn looks weird; Forgejo's
binary is named 'cairn' post-rename, so the subcommand groups under
it are 'cairn auth', 'cairn agent', etc. The naming is consistent
with how Forgejo's other admin commands work.)

Refs: docs/cairn/specs/2026-05-09-cairn-foundation-design.md §4.8

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
git push -u origin cairn-cli-registration
```

---

## End-of-plan verification

After all 7 tasks land:

- [ ] **Run the full test suite**

```bash
cd ~/Source/cairn && git checkout cairn && git pull
go test ./cmd/cairn/... ./services/cairn/... ./models/cairn/... ./routers/api/cairn/...
```

Expected: all tests pass.

- [ ] **End-to-end live test**

Build, migrate, run, exercise the CLI:

```bash
go build -o /tmp/cairn-2b-test .

mkdir -p /tmp/cairn-2b-data
cat > /tmp/cairn-2b.ini <<EOF
[database]
DB_TYPE = sqlite3
PATH = /tmp/cairn-2b-data/cairn.db
[repository]
ROOT = /tmp/cairn-2b-data/repos
[server]
APP_DATA_PATH = /tmp/cairn-2b-data
DOMAIN = localhost
HTTP_PORT = 3000
[security]
INSTALL_LOCK = true
[cairn]
hmac_key_path = /tmp/cairn-2b-data/instance-hmac.key
EOF

/tmp/cairn-2b-test migrate --config /tmp/cairn-2b.ini
/tmp/cairn-2b-test admin user create --config /tmp/cairn-2b.ini \
    --username alice --email nexus@darksoft.co.nz --password testpassword --admin

/tmp/cairn-2b-test web --config /tmp/cairn-2b.ini &
SERVER_PID=$!
sleep 3

# Set up a test seed and config
TEST_HOME=$(mktemp -d)
export XDG_CONFIG_HOME=$TEST_HOME
mkdir -p $TEST_HOME/cairn
head -c 32 /dev/urandom > $TEST_HOME/cairn/seed
chmod 0600 $TEST_HOME/cairn/seed

# auth login
/tmp/cairn-2b-test cairn auth login \
    --instance http://localhost:3000 \
    --username alice --password testpassword

# agent init
/tmp/cairn-2b-test cairn agent init \
    --instance http://localhost:3000 \
    --slug plumb --domain darksoft.co.nz

# agent submit
/tmp/cairn-2b-test cairn agent submit \
    --instance http://localhost:3000 \
    --owner alice --slug plumb --domain darksoft.co.nz

# agents list
/tmp/cairn-2b-test cairn agents list \
    --instance http://localhost:3000

# Sign a test commit
echo "test commit data" | /tmp/cairn-2b-test cairn commit-sign-helper \
    --instance http://localhost:3000 \
    --slug plumb \
    -Y sign -n git

kill $SERVER_PID
rm -rf /tmp/cairn-2b-test /tmp/cairn-2b-data /tmp/cairn-2b.ini $TEST_HOME
```

Expected: each command produces sensible output, no errors. The sign-helper output ends with `-----END SSH SIGNATURE-----`.

---

## Notes for the executing agent

- Forgejo upstream touches in Task 7 are version-dependent — verify the actual urfave/cli version and registration pattern before writing.
- Tasks 2-6 are decoupled from Forgejo's runtime; they're pure functions tested with httptest. Task 7 is where they integrate into the real binary.
- Per-agent private keys never persist to disk by design. The `<slug>.key.pub` files exist (for git's user.signingkey) but `.key` files do not — `commit-sign-helper` re-derives on each call.
- The CLI uses Forgejo's existing token-auth scheme (`Authorization: token <sha1>`) rather than session cookies; this is what `/api/v1/users/:username/tokens` produces.
