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
		refSpecs = append(refSpecs, config.RefSpec("refs/cairn/meta:refs/cairn/meta"))
	}

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

// IsNonFastForward reports whether a go-git push error is a non-fast-forward
// rejection. It checks the typed error (for forward-compatibility) and the
// message text go-git actually emits today.
func IsNonFastForward(err error) bool {
	if errors.Is(err, git.ErrNonFastForwardUpdate) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "non-fast-forward")
}
