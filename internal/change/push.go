package change

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

// RemoteInfo describes a configured remote: its name, its first configured URL,
// and its cairn-level kind ("git" or "cairn").
type RemoteInfo struct {
	Name string
	URL  string
	Kind string
}

// AddRemote registers (or re-points) a git remote and records its cairn kind.
//
// kind defaults to "git" when empty. The go-git remote is created if absent; if
// it already exists with a different first URL it is re-pointed (delete +
// recreate) so a changed URL does not silently keep the old one — mirroring
// fetchRemote's behaviour. The kind is upserted into the remote_kind catalogue.
func (e *Engine) AddRemote(name, url, kind string) error {
	if kind == "" {
		kind = "git"
	}
	rem, err := e.git.Remote(name)
	if errors.Is(err, git.ErrRemoteNotFound) {
		_, err = e.git.CreateRemote(&config.RemoteConfig{Name: name, URLs: []string{url}})
	} else if err == nil {
		cfg := rem.Config()
		if len(cfg.URLs) == 0 || cfg.URLs[0] != url {
			if err = e.git.DeleteRemote(name); err != nil {
				return fmt.Errorf("change.AddRemote: %w", err)
			}
			_, err = e.git.CreateRemote(&config.RemoteConfig{Name: name, URLs: []string{url}})
		}
	}
	if err != nil {
		return fmt.Errorf("change.AddRemote: %w", err)
	}

	if _, err := e.db.Exec(
		`INSERT INTO remote_kind(name, kind) VALUES(?,?)
		 ON CONFLICT(name) DO UPDATE SET kind=excluded.kind`,
		name, kind); err != nil {
		return fmt.Errorf("change.AddRemote: %w", err)
	}
	return nil
}

// ListRemotes returns every configured git remote with its first URL and cairn
// kind (default "git" when unrecorded), sorted by name.
func (e *Engine) ListRemotes() ([]RemoteInfo, error) {
	rems, err := e.git.Remotes()
	if err != nil {
		return nil, fmt.Errorf("change.ListRemotes: %w", err)
	}
	out := make([]RemoteInfo, 0, len(rems))
	for _, rem := range rems {
		cfg := rem.Config()
		url := ""
		if len(cfg.URLs) > 0 {
			url = cfg.URLs[0]
		}
		out = append(out, RemoteInfo{Name: cfg.Name, URL: url, Kind: e.remoteKind(cfg.Name)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// remoteKind returns the recorded cairn kind for a remote, defaulting to "git"
// when there is no remote_kind row.
func (e *Engine) remoteKind(name string) string {
	var kind string
	err := e.db.QueryRow(`SELECT kind FROM remote_kind WHERE name=?`, name).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) || err != nil || kind == "" {
		return "git"
	}
	return kind
}

// PushToRemote projects the change-graph onto git refs (Export) and publishes
// refs/heads/* and refs/tags/* to remoteName. cairn-internal refs (refs/cairn/*)
// are NOT pushed: a plain git remote stores only the standard projection.
//
// When force is true each refspec is force-published (leading "+") and PushOptions.Force
// is set, overwriting a diverged remote branch. A non-fast-forward rejection on
// a non-force push is surfaced as a clear "diverged" error advising fetch/sync or
// --force; an already-up-to-date push is treated as success.
func (e *Engine) PushToRemote(remoteName string, force bool) error {
	return e.push("change.PushToRemote", remoteName, gitRefSpecs(force), force)
}

// PushToRemoteBranch is PushToRemote restricted to a single branch (plus tags):
// it publishes refs/heads/<branch> and refs/tags/* only.
func (e *Engine) PushToRemoteBranch(remoteName, branch string, force bool) error {
	return e.push("change.PushToRemoteBranch", remoteName, branchRefSpecs(branch, force), force)
}

// gitRefSpecs returns the all-branches + all-tags refspecs, force-prefixed when
// force is set.
func gitRefSpecs(force bool) []config.RefSpec {
	prefix := ""
	if force {
		prefix = "+"
	}
	return []config.RefSpec{
		config.RefSpec(prefix + "refs/heads/*:refs/heads/*"),
		config.RefSpec(prefix + "refs/tags/*:refs/tags/*"),
	}
}

// branchRefSpecs returns the single-branch + all-tags refspecs, force-prefixed
// when force is set.
func branchRefSpecs(branch string, force bool) []config.RefSpec {
	prefix := ""
	if force {
		prefix = "+"
	}
	return []config.RefSpec{
		config.RefSpec(fmt.Sprintf("%srefs/heads/%s:refs/heads/%s", prefix, branch, branch)),
		config.RefSpec(prefix + "refs/tags/*:refs/tags/*"),
	}
}

// push is the shared implementation behind PushToRemote/PushToRemoteBranch.
// label names the calling function for error wrapping so messages point at the
// real entry point (PushToRemote vs PushToRemoteBranch).
func (e *Engine) push(label, remoteName string, refSpecs []config.RefSpec, force bool) error {
	if err := e.Export(); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}

	rem, err := e.git.Remote(remoteName)
	if errors.Is(err, git.ErrRemoteNotFound) {
		return fmt.Errorf("%s: no remote %q", label, remoteName)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}

	// Remote kind seam: a "cairn" remote receives a full-fidelity push that
	// includes refs/cairn/meta (the serialized change-graph snapshot). A plain
	// "git" remote receives only the standard heads/tags projection (no cairn refs).
	if e.remoteKind(remoteName) == "cairn" {
		metaCommit, err := e.ExportMeta()
		if err != nil {
			return fmt.Errorf("%s: meta: %w", label, err)
		}
		if err := e.git.Storer.SetReference(
			plumbing.NewHashReference(
				plumbing.ReferenceName("refs/cairn/meta"),
				plumbing.NewHash(metaCommit),
			),
		); err != nil {
			return fmt.Errorf("%s: set meta ref: %w", label, err)
		}
		// Push ALL cairn refs (meta + the per-change refs), not just meta. refs/heads
		// now publishes only SEALED tips, so the working-snapshot commits a
		// cairn->cairn clone needs for full fidelity are reachable only via the
		// refs/cairn/change/<id> refs. Force, since an open change's working head is
		// re-amended (a fresh, non-descendant commit) on every snapshot.
		refSpecs = append(refSpecs, config.RefSpec("+refs/cairn/*:refs/cairn/*"))
	}

	// Privacy: if any path is withheld, repoint the refs about to be pushed at a
	// redacted projection (private bytes stripped from every reachable tree), push
	// those, and restore the real refs afterward. No-op (and byte-identical to a
	// plain push) when nothing is flagged private.
	restore, err := e.redactForPush(refSpecs)
	if err != nil {
		return fmt.Errorf("%s: redact: %w", label, err)
	}
	defer restore()

	auth, err := e.authForRemote(rem)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	err = rem.Push(&git.PushOptions{
		RemoteName: remoteName,
		RefSpecs:   refSpecs,
		Force:      force,
		Auth:       auth,
	})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	// go-git v5.13.2 reports a non-fast-forward rejection as a plain error
	// "non-fast-forward update: <ref>" (remote.go), not the typed
	// ErrNonFastForwardUpdate (that one is for worktree pull). Match the message
	// robustly so a diverged remote gives a clear, actionable error.
	if IsNonFastForward(err) {
		return fmt.Errorf(
			"%s: remote %q diverged (non-fast-forward); fetch/sync first or push --force. If you folded/committed into this branch locally and didn't mean to, 'cairn undo' rewinds it: %w",
			label, remoteName, err)
	}
	return fmt.Errorf("%s: %w", label, err)
}

// redactForPush implements the privacy guarantee at the push boundary. When any
// path is flagged private, it builds a redacted projection of every commit
// reachable from the refs about to be pushed (refSpecs), repoints those local
// refs at their redacted SHAs, and returns a restore func that puts the real refs
// back (run via defer, so it fires even if the push errors). The catalogue/DB is
// never touched, and the local object store keeps the real objects — only NEW
// redacted objects are written. With no private flags it is a single query and a
// no-op restore, so the push is byte-identical to a non-private push.
//
// All four pushed surfaces are covered: refs/heads/* (sealed tips), refs/tags/*,
// refs/cairn/change/* (live working snapshots — the worst leak), and
// refs/cairn/meta (rebuilt so its recorded commit SHAs point at redacted commits).
func (e *Engine) redactForPush(refSpecs []config.RefSpec) (func(), error) {
	noop := func() {}
	red, on, err := e.newRedactor()
	if err != nil {
		return noop, err
	}
	if !on {
		return noop, nil // fast path: nothing withheld
	}

	const metaRef = "refs/cairn/meta"
	type refSnap struct {
		name plumbing.ReferenceName
		orig plumbing.Hash
	}
	var commitRefs []refSnap
	hasMeta := false

	iter, err := e.git.References()
	if err != nil {
		return noop, fmt.Errorf("redactForPush: refs: %w", err)
	}
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil // skip symbolic refs (HEAD)
		}
		name := ref.Name()
		if !refSpecsMatch(refSpecs, name) {
			return nil
		}
		if name.String() == metaRef {
			hasMeta = true
			return nil // meta is rebuilt separately (its tree is meta.json, not file content)
		}
		commitRefs = append(commitRefs, refSnap{name, ref.Hash()})
		return nil
	})
	if err != nil {
		return noop, fmt.Errorf("redactForPush: %w", err)
	}

	// Redact every commit reachable from the in-scope commit refs.
	anchors := make([]string, 0, len(commitRefs))
	for _, rs := range commitRefs {
		anchors = append(anchors, rs.orig.String())
	}
	mapping, err := red.project(anchors)
	if err != nil {
		return noop, fmt.Errorf("redactForPush: %w", err)
	}

	// Repoint each in-scope ref at its redacted target, recording how to restore.
	var restores []func()
	restore := func() {
		for i := len(restores) - 1; i >= 0; i-- {
			restores[i]()
		}
	}
	setRef := func(name plumbing.ReferenceName, sha string) error {
		return e.git.Storer.SetReference(plumbing.NewHashReference(name, plumbing.NewHash(sha)))
	}
	for _, rs := range commitRefs {
		name, orig := rs.name, rs.orig
		// Peel to the underlying commit so an annotated tag (whose ref points at a
		// tag object, not a commit) is repointed at its REDACTED target — otherwise
		// the tag would back-door the real commit. Converting an annotated tag to a
		// lightweight redacted tag loses the annotation but never a private byte.
		target, ok := e.peelToCommit(orig.String())
		if !ok {
			continue // ref does not resolve to a commit (e.g. tag->tree); nothing to redact
		}
		redSHA := mapping[target]
		if redSHA == "" || redSHA == target {
			continue // target commit unchanged by redaction
		}
		if err := setRef(name, redSHA); err != nil {
			restore()
			return noop, fmt.Errorf("redactForPush: set %s: %w", name, err)
		}
		restores = append(restores, func() { _ = e.git.Storer.SetReference(plumbing.NewHashReference(name, orig)) })
	}

	// Rebuild refs/cairn/meta so its recorded commit SHAs point at redacted commits.
	if hasMeta {
		origMeta, rerr := e.git.Reference(plumbing.ReferenceName(metaRef), false)
		if rerr == nil {
			redMeta, merr := red.redactedMeta(mapping)
			if merr != nil {
				restore()
				return noop, fmt.Errorf("redactForPush: meta: %w", merr)
			}
			if redMeta != origMeta.Hash().String() {
				if err := setRef(plumbing.ReferenceName(metaRef), redMeta); err != nil {
					restore()
					return noop, fmt.Errorf("redactForPush: set meta: %w", err)
				}
				oh := origMeta.Hash()
				restores = append(restores, func() {
					_ = e.git.Storer.SetReference(plumbing.NewHashReference(plumbing.ReferenceName(metaRef), oh))
				})
			}
		}
	}
	return restore, nil
}

// refSpecsMatch reports whether any refspec's source side matches the ref name.
func refSpecsMatch(refSpecs []config.RefSpec, name plumbing.ReferenceName) bool {
	for _, s := range refSpecs {
		if s.Match(name) {
			return true
		}
	}
	return false
}

// IsNonFastForward reports whether a go-git push error is a non-fast-forward
// rejection. It checks the typed error (for forward-compatibility) and the
// message text go-git actually emits today.
func IsNonFastForward(err error) bool {
	if errors.Is(err, git.ErrNonFastForwardUpdate) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "non-fast-forward")
}
