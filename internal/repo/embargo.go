package repo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CarriedWorldUniverse/cairn/internal/change"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// The embargo ref namespace (change.EmbargoRefPrefix = "refs/cairn/embargo/") is
// what a cairn client pushes real embargoed content to. The server RELOCATES
// these refs (and their objects) out of the public bare into a per-repo embargo
// bare, so the public bare — which git-upload-pack serves to everyone — never
// advertises them or holds their objects.

// EmbargoStoragePath is the per-repo embargo (private) bare, a sibling of the
// public bare. It holds the real embargoed commits and is self-sufficient — the
// relocation fetch copies the full reachable set into it (it does NOT borrow the
// public bare via git alternates), so the public bare can gc the dangling bytes
// freely. The cost is base duplication, a later space optimization.
func (s *Service) EmbargoStoragePath(id string) string {
	return filepath.Join(s.repoRoot, id+".embargo.git")
}

// ensureEmbargoBare lazily creates the embargo (private) bare (only on the first
// embargo push, so plain repos carry no second bare). It is self-sufficient: the
// relocation fetch copies the full reachable object set into it, so it does NOT
// use git alternates back to the public bare — which means the public bare is
// free to gc the now-dangling embargoed objects (removing the lingering bytes)
// without affecting the embargo bare. The cost is that the frozen base is
// duplicated per embargo bare; a later slice can optimize with a delta-only copy.
func (s *Service) ensureEmbargoBare(id string) error {
	emb := s.EmbargoStoragePath(id)
	if _, err := os.Stat(emb); err == nil {
		return nil // already created
	}
	if _, err := git.PlainInit(emb, true); err != nil {
		return fmt.Errorf("repo.ensureEmbargoBare: init: %w", err)
	}
	return nil
}

// RelocateEmbargoRefs moves every refs/cairn/embargo/* ref (and its delta
// objects) into the embargo bare, then deletes that ref from the public bare.
// After this no public ref reaches the embargoed content, so git-upload-pack
// never advertises or serves it to a public clone — the public projection is
// frozen — while the self-sufficient embargo bare holds the real content. The
// now-dangling embargoed object may physically linger
// in the public bare until gc; that is not a serve leak (reachability, not
// physical presence, is what a clone receives) — paired-gc is a later hardening.
// Returns the number of refs relocated. The post-receive hook calls this.
func (s *Service) RelocateEmbargoRefs(ctx context.Context, repoID string) (int, error) {
	pub := s.storagePath(repoID)
	g, err := git.PlainOpen(pub)
	if err != nil {
		return 0, fmt.Errorf("repo.RelocateEmbargoRefs: open public: %w", err)
	}
	iter, err := g.References()
	if err != nil {
		return 0, fmt.Errorf("repo.RelocateEmbargoRefs: refs: %w", err)
	}
	var names []string
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference && strings.HasPrefix(ref.Name().String(), change.EmbargoRefPrefix) {
			names = append(names, ref.Name().String())
		}
		return nil
	})
	if len(names) == 0 {
		return 0, nil
	}
	if err := s.ensureEmbargoBare(repoID); err != nil {
		return 0, err
	}
	emb := s.EmbargoStoragePath(repoID)
	for _, name := range names {
		// Pull the ref + its full reachable object set into the self-sufficient
		// embargo bare, then drop the ref from the public bare.
		if err := gitRun(ctx, emb, "fetch", "--no-tags", pub, name+":"+name); err != nil {
			return 0, fmt.Errorf("repo.RelocateEmbargoRefs: fetch %s: %w", name, err)
		}
		if err := gitRun(ctx, pub, "update-ref", "-d", name); err != nil {
			return 0, fmt.Errorf("repo.RelocateEmbargoRefs: delete %s: %w", name, err)
		}
	}
	return len(names), nil
}

// GrantEmbargoRecipient authorizes agentID to fetch repoID's embargoed content.
// Idempotent. (cairn owns this ACL; herald supplies the identity.)
func (s *Service) GrantEmbargoRecipient(ctx context.Context, repoID, agentID, grantedBy string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO embargo_recipient(repo_id, agent_id, granted_by, created_at) VALUES(?,?,?,?)
		 ON CONFLICT(repo_id, agent_id) DO NOTHING`,
		repoID, agentID, grantedBy, now)
	if err != nil {
		return fmt.Errorf("repo.GrantEmbargoRecipient: %w", err)
	}
	return nil
}

// RevokeEmbargoRecipient removes agentID's grant. Idempotent. (Note: a recipient
// who already cloned keeps that copy — revocation only stops FUTURE fetches.)
func (s *Service) RevokeEmbargoRecipient(ctx context.Context, repoID, agentID string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM embargo_recipient WHERE repo_id=? AND agent_id=?`, repoID, agentID)
	if err != nil {
		return fmt.Errorf("repo.RevokeEmbargoRecipient: %w", err)
	}
	return nil
}

// IsEmbargoRecipient reports whether agentID may fetch repoID's embargoed content.
func (s *Service) IsEmbargoRecipient(ctx context.Context, repoID, agentID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM embargo_recipient WHERE repo_id=? AND agent_id=?`, repoID, agentID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("repo.IsEmbargoRecipient: %w", err)
	}
	return n > 0, nil
}

// ListEmbargoRecipients returns the agent-ids authorized to fetch repoID's
// embargoed content (sorted), for the ops `embargo-recipients` subcommand.
func (s *Service) ListEmbargoRecipients(ctx context.Context, repoID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_id FROM embargo_recipient WHERE repo_id=? ORDER BY agent_id`, repoID)
	if err != nil {
		return nil, fmt.Errorf("repo.ListEmbargoRecipients: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, fmt.Errorf("repo.ListEmbargoRecipients: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// BareForServe picks which bare git-upload-pack serves to a caller: the per-repo
// embargo bare (real embargoed content) for an authorized recipient fetching, or
// the public bare (frozen projection) for everyone else. The gate is per-identity
// BARE SELECTION — the shell-out git-upload-pack can't filter one bare's
// advertisement, but it can be pointed at a different bare. Only clone/fetch
// (git-upload-pack) is gated; a push (git-receive-pack) always targets the public
// bare (the client pushes embargo refs there; post-receive relocates them).
func (s *Service) BareForServe(ctx context.Context, repoID, agentID, verb string) string {
	pub := s.storagePath(repoID)
	if verb != "git-upload-pack" {
		return pub
	}
	emb := s.EmbargoStoragePath(repoID)
	if _, err := os.Stat(emb); err != nil {
		return pub // no embargo bare → nothing gated
	}
	if ok, err := s.IsEmbargoRecipient(ctx, repoID, agentID); err == nil && ok {
		return emb
	}
	return pub
}

// gitRun runs a system-git command against the given --git-dir. The cairn server
// already depends on system git for upload-pack/receive-pack, so this is no new
// dependency.
func gitRun(ctx context.Context, gitDir string, args ...string) error {
	full := append([]string{"--git-dir", gitDir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
