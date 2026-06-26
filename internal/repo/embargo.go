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
//
// The gate keys on whether the embargo bare still holds ANY gated head ref
// (refs/cairn/embargo/heads/*), not merely on the directory existing: once every
// branch is disclosed (PruneDisclosedEmbargo renamed them out), recipients fall
// back to the full-fidelity public bare even while the now-redundant bare lingers
// awaiting gc — so the gate flips without an rm on the serve hot path.
func (s *Service) BareForServe(ctx context.Context, repoID, agentID, verb string) string {
	pub := s.storagePath(repoID)
	if verb != "git-upload-pack" {
		return pub
	}
	emb := s.EmbargoStoragePath(repoID)
	if _, err := os.Stat(emb); err != nil {
		return pub // no embargo bare → nothing gated
	}
	if !hasEmbargoHeads(emb) {
		return pub // fully disclosed (no gated heads left) → serve the public bare
	}
	if ok, err := s.IsEmbargoRecipient(ctx, repoID, agentID); err == nil && ok {
		return emb
	}
	return pub
}

// PruneDisclosedEmbargo reconciles the embargo bare after a disclose re-push. When
// an embargo is lifted (client `disclose` + the next normal push), the disclosed
// branch's content re-enters the PUBLIC projection — the client uncaps it and
// force-pushes full meta. Its refs/cairn/embargo/heads/<branch> in the embargo
// bare is then redundant and, left in place, would make BareForServe keep serving
// recipients the stale gated copy. For each such ref whose tip is BOTH an ancestor
// of a public head/tag AND physically present in the public bare, it is disclosed:
// rename it to refs/heads/<branch> inside the embargo bare (a recipient still
// cloning the bare for OTHER gated branches keeps full visibility of the disclosed
// one), then drop the gated embargo ref. Reachability is computed from
// refs/heads/* + refs/tags/* ONLY (never refs/cairn/*), so a still-embargoed
// commit — held out of public by PublicTip — can never be selected. The
// object-presence check guards against deleting a gate before public actually
// holds the bytes. Idempotent and self-healing: any error skips that ref, and the
// next push re-evaluates from scratch. Returns the count disclosed. Run AFTER
// RelocateEmbargoRefs (it must not see partially-relocated state).
func (s *Service) PruneDisclosedEmbargo(ctx context.Context, repoID string) (int, error) {
	emb := s.EmbargoStoragePath(repoID)
	if _, err := os.Stat(emb); err != nil {
		return 0, nil // no embargo bare → nothing to reconcile
	}
	pub := s.storagePath(repoID)
	pubTips, err := publicRefTips(pub)
	if err != nil {
		return 0, fmt.Errorf("repo.PruneDisclosedEmbargo: public refs: %w", err)
	}
	if len(pubTips) == 0 {
		return 0, nil
	}

	g, err := git.PlainOpen(emb)
	if err != nil {
		return 0, fmt.Errorf("repo.PruneDisclosedEmbargo: open embargo: %w", err)
	}
	iter, err := g.References()
	if err != nil {
		return 0, fmt.Errorf("repo.PruneDisclosedEmbargo: refs: %w", err)
	}
	headsPrefix := change.EmbargoRefPrefix + "heads/"
	type cand struct{ name, branch, tip string }
	var cands []cand
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		n := ref.Name().String()
		if ref.Type() == plumbing.HashReference && strings.HasPrefix(n, headsPrefix) {
			cands = append(cands, cand{name: n, branch: strings.TrimPrefix(n, headsPrefix), tip: ref.Hash().String()})
		}
		return nil
	})

	disclosed := 0
	for _, c := range cands {
		// Disclosed iff the tip both re-entered the public projection (ancestor of a
		// public head/tag) AND is physically present in the public bare.
		if !objectPresent(ctx, pub, c.tip) || !isAncestorOfAny(ctx, pub, c.tip, pubTips) {
			continue
		}
		// Keep the disclosed branch visible as a normal head in the embargo bare,
		// then drop the gated ref. On any error, skip — the next push retries.
		if err := gitRun(ctx, emb, "update-ref", "refs/heads/"+c.branch, c.tip); err != nil {
			continue
		}
		if err := gitRun(ctx, emb, "update-ref", "-d", c.name); err != nil {
			continue
		}
		disclosed++
	}
	return disclosed, nil
}

// GCRepo reclaims dangling objects and reaps a fully-disclosed embargo bare. It
// runs `git gc` on the public bare (reclaiming the embargoed objects left dangling
// by RelocateEmbargoRefs); pruneNow uses --prune=now for an explicit quiet-window
// run, otherwise git's default grace expiry protects objects an in-flight
// receive-pack wrote but has not yet ref-linked. If the embargo bare exists with
// no gated heads left (fully disclosed), its content is pure duplication of public
// and the whole directory is removed (instant byte reclaim); otherwise it is gc'd
// in place (repack after a partial disclosure). gc on the public bare cannot harm
// the embargo bare — the two share no object storage (no git alternates). Returns
// whether the embargo bare was reaped. Operator-invoked (`cairn-server gc`), never
// on the push hot path: gc is the one object-rewriting op and is kept serialized
// and schedulable, off the race surface of concurrent pushes/clones.
func (s *Service) GCRepo(ctx context.Context, repoID string, pruneNow bool) (bool, error) {
	gcArgs := []string{"gc"}
	if pruneNow {
		gcArgs = append(gcArgs, "--prune=now")
	}
	pub := s.storagePath(repoID)
	if err := gitRun(ctx, pub, gcArgs...); err != nil {
		return false, fmt.Errorf("repo.GCRepo: public gc: %w", err)
	}
	emb := s.EmbargoStoragePath(repoID)
	if _, err := os.Stat(emb); err != nil {
		return false, nil // no embargo bare
	}
	if !hasEmbargoHeads(emb) {
		if err := os.RemoveAll(emb); err != nil {
			return false, fmt.Errorf("repo.GCRepo: reap embargo bare: %w", err)
		}
		return true, nil
	}
	if err := gitRun(ctx, emb, gcArgs...); err != nil {
		return false, fmt.Errorf("repo.GCRepo: embargo gc: %w", err)
	}
	return false, nil
}

// hasEmbargoHeads reports whether embPath holds any gated head ref
// (refs/cairn/embargo/heads/*). On any read error it returns false — the
// non-leak-safe default (serve/keep the public bare), never over-serve.
func hasEmbargoHeads(embPath string) bool {
	g, err := git.PlainOpen(embPath)
	if err != nil {
		return false
	}
	iter, err := g.References()
	if err != nil {
		return false
	}
	prefix := change.EmbargoRefPrefix + "heads/"
	found := false
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.HashReference && strings.HasPrefix(ref.Name().String(), prefix) {
			found = true
		}
		return nil
	})
	return found
}

// publicRefTips returns the tip SHAs of a bare's refs/heads/* and refs/tags/*
// only — the truly-public reachability frontier (never refs/cairn/*, so a
// not-yet-relocated or still-embargoed ref can't widen it).
func publicRefTips(pub string) ([]string, error) {
	g, err := git.PlainOpen(pub)
	if err != nil {
		return nil, err
	}
	iter, err := g.References()
	if err != nil {
		return nil, err
	}
	var out []string
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		n := ref.Name().String()
		if ref.Type() == plumbing.HashReference && (strings.HasPrefix(n, "refs/heads/") || strings.HasPrefix(n, "refs/tags/")) {
			out = append(out, ref.Hash().String())
		}
		return nil
	})
	return out, nil
}

// objectPresent reports whether gitDir physically holds the object (cat-file -e).
func objectPresent(ctx context.Context, gitDir, sha string) bool {
	return gitRun(ctx, gitDir, "cat-file", "-e", sha) == nil
}

// isAncestorOfAny reports whether tip is an ancestor of (or equal to) any of the
// given ref tips in gitDir (git merge-base --is-ancestor exits 0 for ancestor,
// and a commit is its own ancestor — covering tip == public head).
func isAncestorOfAny(ctx context.Context, gitDir, tip string, refTips []string) bool {
	for _, r := range refTips {
		if gitRun(ctx, gitDir, "merge-base", "--is-ancestor", tip, r) == nil {
			return true
		}
	}
	return false
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
