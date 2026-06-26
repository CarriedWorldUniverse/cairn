// Package sshd is cairn's SSH ingress: it authenticates a connecting aspect by
// its casket public key (fingerprint -> herald agent), enforces repo scope,
// and runs git-upload-pack / git-receive-pack against the on-disk bare repo.
// It is a parallel, inherently-encrypted ingress (not gateway-fronted): SSH's
// own transport encryption plus casket public-key auth secure it.
package sshd

import (
	"context"
	"fmt"
	"net"
	"os/exec"

	"github.com/CarriedWorldUniverse/cairn/internal/herald"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
	glssh "github.com/gliderlabs/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// contextKey for the resolved agent stashed by the auth callback.
type ctxKey string

const agentKey ctxKey = "cairn-agent"

// Config configures the SSH ingress.
type Config struct {
	Core       *repo.Service       // the repo + ref core (Task 1)
	Agents     herald.HeraldAgents // fingerprint -> herald agent (Task 2)
	HostSigner gossh.Signer        // cairn's own Ed25519 host key
}

// Server is cairn's SSH git host.
type Server struct {
	cfg Config
}

// New builds a Server.
func New(cfg Config) *Server { return &Server{cfg: cfg} }

// Serve accepts connections on ln until it is closed.
func (s *Server) Serve(ln net.Listener) error {
	srv := &glssh.Server{
		Handler:          s.handleSession,
		PublicKeyHandler: s.authPublicKey,
	}
	srv.AddHostKey(s.cfg.HostSigner)
	return srv.Serve(ln)
}

// authPublicKey resolves the casket key fingerprint to a herald agent and, on
// success, stashes it in the session context. A non-resolving or inactive key
// is rejected (auth fails).
func (s *Server) authPublicKey(ctx glssh.Context, key glssh.PublicKey) bool {
	fp, err := Fingerprint(key)
	if err != nil {
		return false
	}
	agent, err := s.cfg.Agents.LookupByFingerprint(ctx, fp)
	if err != nil || !agent.Active {
		return false
	}
	ctx.SetValue(agentKey, agent)
	return true
}

// handleSession parses the git command, enforces scope, and pumps the pack
// protocol via the system git binary against the on-disk bare repo.
func (s *Server) handleSession(sess glssh.Session) {
	agent, _ := sess.Context().Value(agentKey).(herald.Agent)

	raw := sess.RawCommand()
	verb, org, slug, err := ParseGitCommand(raw)
	if err != nil {
		fmt.Fprintln(sess.Stderr(), "cairn: "+err.Error())
		_ = sess.Exit(1)
		return
	}

	// Org binding: the agent may only touch its own org (single-org MVP, but
	// enforced so a cross-org key can't reach another org's repo).
	if org != agent.OrgID {
		fmt.Fprintln(sess.Stderr(), "cairn: org mismatch")
		_ = sess.Exit(1)
		return
	}

	// Scope gate: clone/fetch needs repo:read; push needs repo:write.
	need := "repo:read"
	if verb == "git-receive-pack" {
		need = "repo:write"
	}
	if !agent.HasScope(need) {
		fmt.Fprintln(sess.Stderr(), "cairn: missing scope "+need)
		_ = sess.Exit(1)
		return
	}

	ctx := context.Background()
	r, err := s.cfg.Core.GetRepo(ctx, org, slug)
	if err != nil {
		fmt.Fprintln(sess.Stderr(), "cairn: repo not found")
		_ = sess.Exit(1)
		return
	}

	// Pump the pack protocol via the system git binary against the bare repo.
	// Embargo gate: an authorized recipient fetching gets the embargo bare (real
	// content); everyone else gets the public bare (frozen). Push always targets
	// the public bare. No-op when the repo has no embargo bare.
	bareDir := s.cfg.Core.BareForServe(ctx, r.ID, agent.ID, verb)
	cmd := exec.CommandContext(ctx, verb, bareDir)
	cmd.Stdin = sess
	cmd.Stdout = sess
	cmd.Stderr = sess.Stderr()
	if err := cmd.Run(); err != nil {
		_ = sess.Exit(1)
		return
	}
	_ = sess.Exit(0)
}
