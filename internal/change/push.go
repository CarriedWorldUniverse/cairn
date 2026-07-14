package change

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
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
	url = storeAndStrip(url) // never persist credentials in the repo remote; move them to the user-level credstore
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
	return e.push("change.PushToRemote", remoteName, "", gitRefSpecs(force), force)
}

// PushToRemoteBranch is PushToRemote restricted to a single branch (plus tags):
// it publishes refs/heads/<branch> and refs/tags/* only.
func (e *Engine) PushToRemoteBranch(remoteName, branch string, force bool) error {
	return e.push("change.PushToRemoteBranch", remoteName, branch, branchRefSpecs(branch, force), force)
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

// push is the shared implementation behind PushToRemote/PushToRemoteBranch —
// the unlocked, single-call convenience path used when there is no worktime
// lock to release around the network phase (i.e. engine-level callers/tests
// that talk to the Engine directly). It just runs the three PreparePush /
// NetworkPush / FinishPush phases back to back. The worktree layer, which
// DOES hold a cross-process lock, calls the three phases itself so it can
// release that lock for NetworkPush (issue #98 Phase B).
func (e *Engine) push(label, remoteName, branch string, refSpecs []config.RefSpec, force bool) error {
	pp, err := e.PreparePush(label, remoteName, branch, refSpecs, force)
	if err != nil {
		return err
	}
	netErr := e.NetworkPush(pp)
	finErr := e.FinishPush(pp)
	if netErr != nil {
		return netErr
	}
	if finErr != nil {
		return fmt.Errorf("%s: cleanup pinned refs: %w", label, finErr)
	}
	return nil
}

// pushPin is one outgoing ref pinned into the private refs/cairn/push/<op-id>/*
// namespace during PreparePush's Phase 1: a private, immutable-content copy of
// the exact ref state the network phase will publish, so a concurrent mutator
// of the REAL ref (refs/heads/*, refs/tags/*, refs/cairn/*) can never tear what
// NetworkPush reads.
type pushPin struct {
	pinned      plumbing.ReferenceName // refs/cairn/push/<op-id>/<original path minus "refs/">
	target      plumbing.ReferenceName // the real ref name this publishes to on the remote
	forceUpdate bool
	expected    plumbing.Hash // the SHA pinned == the SHA we expect to land on the remote
}

// PreparedPush is the frozen, network-ready state produced by PreparePush: a
// pinned, private snapshot of every outgoing ref plus the resolved remote/auth
// to publish it with. Every PreparedPush MUST be passed to FinishPush exactly
// once — on both the success and the failure path — to remove its pinned refs;
// see FinishPush.
type PreparedPush struct {
	label      string
	remoteName string
	opID       string
	force      bool
	auth       transport.AuthMethod
	pins       []pushPin
}

// testNetworkDelay, when non-nil, is invoked by NetworkPush just before the
// actual rem.Push call — a test seam for injecting artificial network latency
// (a local bare-repo remote has no real latency to exercise the "network phase
// runs without the lock" behavior) without touching the network phase's
// logic. Never set outside tests.
var testNetworkDelay func()

// testSkipRealPush, when non-nil and returning true, makes NetworkPush skip
// the actual rem.Push call while still running post-push verification — a
// test seam for issue #117's failure mode: go-git's file transport can report
// success without the ref actually landing on the remote. Never set outside
// tests.
var testSkipRealPush func() bool

// testPinError, when non-nil, is consulted by pinOutgoingRefs for the pinned
// ref name about to be written — a test seam for injecting a mid-loop pin
// failure (there is no cheap real-world way to make a specific SetReference
// call fail deterministically), so the partial-pin rollback path can be
// exercised directly. Never set outside tests.
var testPinError func(pinned plumbing.ReferenceName) error

// PreparePush runs the LOCKED half of a push: the conflict gate, Export, all
// redaction/embargo staging, and — the issue #98 Phase B change — pinning the
// exact refs about to be published into the private refs/cairn/push/<op-id>/*
// namespace, so NetworkPush (Phase 2, run WITHOUT the caller's lock) reads
// only content this op owns. Loose git objects are content-addressed and
// immutable, and the pinned refs are private to this op's op-id, so a
// concurrent mutator of the real refs cannot tear what NetworkPush reads.
//
// Callers must hold whatever lock guards the local repo state (the worktree
// layer's wc.lock) for the duration of PreparePush, may then release it for
// NetworkPush, and must call FinishPush — under the lock again — on every
// path (success or error) to remove the pinned refs.
func (e *Engine) PreparePush(label, remoteName, branch string, refSpecs []config.RefSpec, force bool) (*PreparedPush, error) {
	// Remote kind seam: a "cairn" remote receives a full-fidelity push that
	// includes refs/cairn/meta (the serialized change-graph snapshot). A plain
	// "git" remote receives only the standard heads/tags projection (no cairn refs).
	isCairn := e.remoteKind(remoteName) == "cairn"

	if err := e.checkConflictGate(label, branch, isCairn, force); err != nil {
		return nil, err
	}

	if err := e.Export(); err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}

	rem, err := e.git.Remote(remoteName)
	if errors.Is(err, git.ErrRemoteNotFound) {
		return nil, fmt.Errorf("%s: no remote %q", label, remoteName)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}

	if isCairn {
		hasEmb, herr := e.HasEmbargo()
		if herr != nil {
			return nil, fmt.Errorf("%s: %w", label, herr)
		}
		if hasEmb {
			// Embargo + cairn server: the PUBLIC bare gets only the capped heads/tags
			// — no cairn meta and no per-change refs — so a public clone reconstructs
			// the frozen FLAT projection (valid, free of embargoed content; full cairn
			// fidelity is restored when the embargo is disclosed). The REAL (uncapped)
			// tips + full meta go to the private refs/cairn/embargo/* namespace, which
			// the server relocates into its gated private store for authorized clones.
			cleanup, serr := e.stageEmbargoRefs()
			if serr != nil {
				return nil, fmt.Errorf("%s: stage embargo: %w", label, serr)
			}
			// cleanup removes the staged refs/cairn/embargo/* refs; it must run
			// AFTER we've pinned them below, so it is deferred first (LIFO — runs
			// last, after the redaction/embargo-cap restores and the pin capture).
			defer cleanup()
			refSpecs = append(refSpecs, config.RefSpec("+"+EmbargoRefPrefix+"*:"+EmbargoRefPrefix+"*"))
		} else {
			metaCommit, err := e.ExportMeta()
			if err != nil {
				return nil, fmt.Errorf("%s: meta: %w", label, err)
			}
			if err := e.git.Storer.SetReference(
				plumbing.NewHashReference(
					plumbing.ReferenceName("refs/cairn/meta"),
					plumbing.NewHash(metaCommit),
				),
			); err != nil {
				return nil, fmt.Errorf("%s: set meta ref: %w", label, err)
			}
			// Push ALL cairn refs (meta + the per-change refs). refs/heads publishes
			// only SEALED tips, so the working-snapshot commits a cairn->cairn clone
			// needs for full fidelity are reachable only via refs/cairn/change/<id>.
			refSpecs = append(refSpecs, config.RefSpec("+refs/cairn/*:refs/cairn/*"))
		}
	}

	// Embargo: cap each public ref (refs/heads/*, refs/tags/*) at its embargo
	// boundary so embargoed commits (and everything after) are held out of the
	// public projection. The refs/cairn/embargo/* namespace is exempt (it carries
	// the real tips for the gated store). Runs BEFORE redaction so privacy then
	// redacts the capped commit. No-op when nothing is embargoed.
	embRestore, err := e.embargoCapForPush(refSpecs, isCairn)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	defer embRestore()

	// Privacy: if any path is withheld, repoint the refs about to be pushed at a
	// redacted projection (private bytes stripped from every reachable tree), push
	// those, and restore the real refs afterward. No-op (and byte-identical to a
	// plain push) when nothing is flagged private.
	restore, err := e.redactForPush(refSpecs)
	if err != nil {
		return nil, fmt.Errorf("%s: redact: %w", label, err)
	}
	defer restore()

	auth, err := e.authForRemote(rem)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}

	// Pin: capture the FINAL local ref state (post-Export, post-embargo-cap,
	// post-redaction — exactly what a same-process, same-moment push would have
	// published) into refs/cairn/push/<op-id>/*, private to this op. The deferred
	// restores above then put the real refs back to their normal state before
	// this function returns, while NetworkPush publishes from the pinned copies.
	opID, err := randomOpID()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	pins, err := e.pinOutgoingRefs(opID, refSpecs)
	if err != nil {
		return nil, fmt.Errorf("%s: pin refs: %w", label, err)
	}

	return &PreparedPush{
		label:      label,
		remoteName: remoteName,
		opID:       opID,
		force:      force,
		auth:       auth,
		pins:       pins,
	}, nil
}

// randomOpID returns a random 16-hex-char id (8 bytes) for scoping a push's
// pinned refs to refs/cairn/push/<op-id>/*, so concurrent pushes never collide.
func randomOpID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("randomOpID: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// pushPinRefPrefix is the private namespace pinned refs live under
// (refs/cairn/push/<op-id>/...). It is also the namespace pinOutgoingRefs and
// the fetch network phase both refuse to ever touch as a SOURCE (see the
// exclusion below and stripForeignPushPins) — see the "pin leak" fix: go-git's
// refspec glob matching has no "/" boundary awareness, so a wildcard refspec
// like "refs/cairn/*:refs/cairn/*" or "refs/cairn/*" ALSO matches
// refs/cairn/push/<op-id>/*, meaning a naive pin/fetch pass would happily
// re-pin (and then permanently publish) another op's — or a crash orphan's,
// or a previously-fetched foreign — pinned refs. They must never be pinned,
// published, or imported; only PreparePush (which creates them) and
// FinishPush (which removes them) ever touch this namespace.
const pushPinRefPrefix = "refs/cairn/push/"

// pinOutgoingRefs enumerates every local hash ref matching refSpecs' source
// side — EXCLUDING anything already under pushPinRefPrefix (see its doc) —
// copies each into refs/cairn/push/<opID>/<path-without-"refs/">, and
// records its real target ref name (via the matching RefSpec's Dst) and
// force-update flag for NetworkPush to publish from.
//
// If pinning fails partway through, every pin already created in this call is
// removed (best-effort) before returning the error, so a partial failure
// never leaves orphaned pins for FinishPush to never see (PreparePush returns
// nil on error, so its caller has no PreparedPush to pass to FinishPush).
func (e *Engine) pinOutgoingRefs(opID string, refSpecs []config.RefSpec) ([]pushPin, error) {
	iter, err := e.git.References()
	if err != nil {
		return nil, fmt.Errorf("pinOutgoingRefs: refs: %w", err)
	}
	var pins []pushPin
	rollback := func() {
		for _, p := range pins {
			_ = e.git.Storer.RemoveReference(p.pinned)
		}
	}
	ferr := iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil // skip symbolic refs (HEAD)
		}
		if strings.HasPrefix(ref.Name().String(), pushPinRefPrefix) {
			return nil // never re-pin/re-publish another op's (or an orphaned) pin
		}
		spec, ok := matchingRefSpec(refSpecs, ref.Name())
		if !ok {
			return nil
		}
		dst := spec.Dst(ref.Name())
		pinned := plumbing.ReferenceName(pushPinRefPrefix + opID + "/" + strings.TrimPrefix(ref.Name().String(), "refs/"))
		if testPinError != nil {
			if terr := testPinError(pinned); terr != nil {
				return fmt.Errorf("pinOutgoingRefs: pin %s: %w", ref.Name(), terr)
			}
		}
		if err := e.git.Storer.SetReference(plumbing.NewHashReference(pinned, ref.Hash())); err != nil {
			return fmt.Errorf("pinOutgoingRefs: pin %s: %w", ref.Name(), err)
		}
		pins = append(pins, pushPin{
			pinned:      pinned,
			target:      dst,
			forceUpdate: spec.IsForceUpdate(),
			expected:    ref.Hash(),
		})
		return nil
	})
	if ferr != nil {
		rollback()
		return nil, ferr
	}
	return pins, nil
}

// matchingRefSpec returns the first refspec in refSpecs whose source side
// matches name.
func matchingRefSpec(refSpecs []config.RefSpec, name plumbing.ReferenceName) (config.RefSpec, bool) {
	for _, s := range refSpecs {
		if s.Match(name) {
			return s, true
		}
	}
	return config.RefSpec(""), false
}

// NetworkPush is the UNLOCKED half of a push (issue #98 Phase B): it publishes
// pp's pinned refs to their real target names on the remote, then verifies —
// by listing the remote's advertised refs (no extra object fetch) — that every
// pushed ref now equals the pinned SHA. go-git's file transport can report
// success without the ref landing (issue #117's root cause), so verification
// is mandatory, not optional. Safe to run without any lock: the pinned refs it
// reads are private to pp's op-id and immutable (content-addressed objects,
// refs nothing else writes).
func (e *Engine) NetworkPush(pp *PreparedPush) error {
	if len(pp.pins) == 0 {
		return nil // nothing matched the refspecs (e.g. an empty repo) — no-op
	}
	rem, err := e.git.Remote(pp.remoteName)
	if err != nil {
		return fmt.Errorf("%s: %w", pp.label, err)
	}

	specs := make([]config.RefSpec, 0, len(pp.pins))
	for _, p := range pp.pins {
		prefix := ""
		if p.forceUpdate {
			prefix = "+"
		}
		specs = append(specs, config.RefSpec(prefix+p.pinned.String()+":"+p.target.String()))
	}

	if testNetworkDelay != nil {
		testNetworkDelay()
	}

	skip := testSkipRealPush != nil && testSkipRealPush()
	if !skip {
		err = rem.Push(&git.PushOptions{
			RemoteName: pp.remoteName,
			RefSpecs:   specs,
			Force:      pp.force,
			Auth:       pp.auth,
		})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			// go-git v5.13.2 reports a non-fast-forward rejection as a plain error
			// "non-fast-forward update: <ref>" (remote.go), not the typed
			// ErrNonFastForwardUpdate (that one is for worktree pull). Match the
			// message robustly so a diverged remote gives a clear, actionable error.
			if IsNonFastForward(err) {
				return fmt.Errorf(
					"%s: remote %q diverged (non-fast-forward); fetch/sync first or push --force. If you folded/committed into this branch locally and didn't mean to, 'cairn undo' rewinds it: %w",
					pp.label, pp.remoteName, err)
			}
			return fmt.Errorf("%s: %w", pp.label, err)
		}
	}

	return e.verifyPush(pp, rem)
}

// verifyPush lists the remote's advertised refs and checks that every pin's
// target ref now equals the pinned SHA — the fix for issue #117: go-git's
// file transport can swallow a subprocess failure and report a push as
// successful when the ref never actually moved, so success must be verified
// from the remote's own advertised state, not trusted from a nil error.
//
// An exact-SHA mismatch is not automatically a failure: NetworkPush runs
// without the caller's wc.lock, so between our rem.Push and this List another
// process can legitimately have pushed further (e.g. fast-forwarded past our
// commit). If the advertised SHA is a commit we HAVE locally and our pinned
// SHA is its ancestor, that's a benign race — someone built on our push — and
// is treated as success. If the advertised SHA is not a commit we have
// locally at all, we cannot tell a legitimate concurrent push apart from ours
// never landing; residual ambiguity: the error names the possibility rather
// than asserting failure outright, and points at `fetch` to disambiguate.
//
// Threat model boundary (security review): this defends against an HONEST
// remote whose transport swallowed a failure (issue #117 — go-git's file
// transport can report success without the ref actually landing). It is NOT
// a defense against a LYING or compromised remote: rem.List just asks that
// same remote what it currently has, so a malicious remote can advertise any
// SHA it likes — including a fabricated one it also happily serves objects
// for, which would satisfy the ancestor check above. A nil return here means
// "the remote we talked to says our push landed," not "an independently
// verified, tamper-proof landing." It provides no integrity guarantee
// against a hostile remote.
func (e *Engine) verifyPush(pp *PreparedPush, rem *git.Remote) error {
	refs, err := rem.List(&git.ListOptions{Auth: pp.auth})
	// An empty remote (nothing has ever landed) advertises no refs at all —
	// go-git reports this as ErrEmptyRemoteRepository rather than an empty
	// list. Treat it as "no refs advertised" so the per-pin check below
	// reports the expected "did not land" error naming the ref, instead of a
	// generic transport error.
	if err != nil && !errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return fmt.Errorf("%s: verify push: list remote refs: %w", pp.label, err)
	}
	byName := make(map[plumbing.ReferenceName]plumbing.Hash, len(refs))
	for _, r := range refs {
		if r.Type() == plumbing.HashReference {
			byName[r.Name()] = r.Hash()
		}
	}
	for _, p := range pp.pins {
		got, ok := byName[p.target]
		if !ok {
			return fmt.Errorf(
				"%s: push did not land: remote ref %s is missing (want %s — verified via post-push remote listing — issue #117)",
				pp.label, p.target, p.expected)
		}
		if got == p.expected {
			continue
		}
		if e.commitExistsLocally(got) {
			if e.remoteAdvancedPastOurPush(p.expected, got) {
				continue // legitimate concurrent push landed on top of ours
			}
			return fmt.Errorf(
				"%s: push did not land: remote ref %s is %s (known locally, but not a descendant of the pinned %s — verified via post-push remote listing — issue #117)",
				pp.label, p.target, got, p.expected)
		}
		return fmt.Errorf(
			"%s: push did not land: remote %s advanced to %s which is not known locally — if another push raced ours, fetch and inspect; otherwise the push did not land (want %s — verified via post-push remote listing — issue #117)",
			pp.label, p.target, got, p.expected)
	}
	return nil
}

// commitExistsLocally reports whether h is a commit object present in this
// engine's local store.
func (e *Engine) commitExistsLocally(h plumbing.Hash) bool {
	_, err := e.git.CommitObject(h)
	return err == nil
}

// remoteAdvancedPastOurPush reports whether got — a commit the caller has
// already confirmed is known locally (see commitExistsLocally) — has
// expected as an ancestor: the "someone raced our push and fast-forwarded
// past it" case, distinguished from "our push never landed" (see
// verifyPush). false also covers "our own pinned ref isn't a commit" (e.g. a
// lightweight tag we don't ancestor-check) and "got diverged from expected"
// (a real failure).
func (e *Engine) remoteAdvancedPastOurPush(expected, got plumbing.Hash) bool {
	if !e.commitExistsLocally(expected) {
		return false
	}
	base, err := e.mergeBase(expected.String(), got.String())
	return err == nil && base == expected.String()
}

// FinishPush removes pp's pinned refs (refs/cairn/push/<op-id>/*). It is
// best-effort and MUST be called on every PreparedPush exactly once, on both
// the success and the failure path (defer it right after PreparePush
// succeeds), so a crash or an early return never leaks pinned refs. Errors
// removing individual refs are collected and joined, but every ref is still
// attempted.
func (e *Engine) FinishPush(pp *PreparedPush) error {
	var errs []error
	for _, p := range pp.pins {
		if err := e.git.Storer.RemoveReference(p.pinned); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", p.pinned, err))
		}
	}
	return errors.Join(errs...)
}

// PreparePushToRemote is the Phase-1 (locked) half of PushToRemote: see
// PreparePush.
func (e *Engine) PreparePushToRemote(remoteName string, force bool) (*PreparedPush, error) {
	return e.PreparePush("change.PushToRemote", remoteName, "", gitRefSpecs(force), force)
}

// PreparePushToRemoteBranch is the Phase-1 (locked) half of
// PushToRemoteBranch: see PreparePush.
func (e *Engine) PreparePushToRemoteBranch(remoteName, branch string, force bool) (*PreparedPush, error) {
	return e.PreparePush("change.PushToRemoteBranch", remoteName, branch, branchRefSpecs(branch, force), force)
}

// ErrPushHasConflict is returned by checkConflictGate when a push to a plain
// git remote is refused because an in-scope line still has an open conflict.
// It is deliberately distinct from two other, differently-caused sentinels:
//   - change.ErrHasConflict (fold.go) fires on FoldLine and reads "resolve
//     before folding" — wrapping it here would print folding advice on a
//     refused push.
//   - worktree.ErrPushConflict fires when Push's own auto-reconcile (a pull
//     triggered by a non-fast-forward rejection) produces a NEW conflict
//     mid-retry; this sentinel instead covers a conflict that already existed
//     going into the push.
var ErrPushHasConflict = errors.New("change: line has open conflicts; resolve before pushing")

// checkConflictGate refuses a push to a plain "git" remote while any in-scope
// line still has an open conflict.
//
// Design decision (issue #93): a conflicted reconcile commits a 2-parent merge
// whose file content is the literal diff3 conflict markers ("<<<<<<< ours" /
// "=======" / ">>>>>>> theirs") as the line's new tip — that's how cairn
// represents "conflicted" internally (conflicts-as-data). A plain git remote
// has no such representation: it just stores bytes, so pushing that tip
// silently PUBLISHES the raw markers as if they were resolved content. So a
// "git" remote refuses the push (unless force) until the conflict is
// resolved. A "cairn" remote is exempt from the line-tip check: conflicts-as-
// data is part of its full-fidelity design (the line tree + open conflicts
// travel with the push, same as any other change-graph state) — that's the
// #93-settled decision. BUT the shipped cairn server serves plain git on the
// wire (httpd/sshd's git-upload-pack), so a marker-laden refs/heads commit IS
// readable by an ordinary git client with repo:read; this exemption stands
// pending a server-side gate, not because there is genuinely nothing to
// protect against.
//
// branch scopes the check to a single line's name (PushToRemoteBranch, gating
// on just that branch's line); "" checks every line (PushToRemote). A branch
// name with no matching cairn line (e.g. a remote-only ref) is not gated —
// there is no line state to check. force bypasses the gate, matching the
// existing --force semantics elsewhere in push.
//
// A branch-scoped push additionally checks every tag for a conflicted target:
// branchRefSpecs always appends refs/tags/* alongside the single named branch
// (see branchRefSpecs), so a tag pointing at a DIFFERENT, still-conflicted
// line's tip would otherwise leak the marker-laden commit even though the
// pushed branch itself is clean. A whole-repo push (branch == "") already
// covers every line via conflictedLineNames, so it skips the extra tag query.
func (e *Engine) checkConflictGate(label, branch string, isCairn, force bool) error {
	if force || isCairn {
		return nil
	}
	var lineID string
	if branch != "" {
		line, err := e.LineByName(branch)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		lineID = line.ID
	}
	names, err := e.conflictedLineNames(lineID)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if len(names) > 0 {
		return fmt.Errorf(
			"%s: line(s) %s have open conflicts; resolve them ('cairn resolve <branch> <path>') then push again, or 'cairn undo' to rewind the reconcile merge, or pass --force to publish the conflict markers anyway: %w",
			label, strings.Join(names, ", "), ErrPushHasConflict)
	}

	if branch != "" {
		tags, terr := e.conflictedTagNames()
		if terr != nil {
			return fmt.Errorf("%s: %w", label, terr)
		}
		if len(tags) > 0 {
			return fmt.Errorf(
				"%s: tag(s) %s point at commits with open conflicts; delete the tag ('cairn tag -d <name>') or resolve the underlying line ('cairn resolve <branch> <path>') then push again, or pass --force to publish the conflict markers anyway: %w",
				label, formatConflictedTags(tags), ErrPushHasConflict)
		}
	}
	return nil
}

// conflictedTag names a tag found by conflictedTagNames, together with the
// (live, open-conflicted) line whose content it points at.
type conflictedTag struct {
	Tag  string
	Line string
}

// formatConflictedTags renders conflictedTagNames' results for an error
// message: "leak (line exp), other (line feat2)".
func formatConflictedTags(tags []conflictedTag) string {
	parts := make([]string, len(tags))
	for i, ct := range tags {
		parts[i] = fmt.Sprintf("%s (line %s)", ct.Tag, ct.Line)
	}
	return strings.Join(parts, ", ")
}

// conflictedTagNames returns every tag whose commit_sha matches either the
// tip_commit of a live, open-conflicted line, or the head_commit of a live
// change carrying an open conflict row — the same live-open-conflict
// line/change set conflictedLineNames uses (c.status='open' AND
// l.status='open' AND ch.status='open'), just joined against the tag table
// instead of grouped by line name. Sorted by tag name.
func (e *Engine) conflictedTagNames() ([]conflictedTag, error) {
	rows, err := e.db.Query(
		`SELECT DISTINCT t.name, l.name
		 FROM tag t
		 JOIN change ch ON ch.status = 'open'
		 JOIN line l ON l.id = ch.line_id AND l.status = 'open'
		 JOIN conflict c ON c.change_id = ch.id AND c.status = 'open'
		 WHERE t.commit_sha = l.tip_commit OR t.commit_sha = ch.head_commit
		 ORDER BY t.name`)
	if err != nil {
		return nil, fmt.Errorf("change.conflictedTagNames: %w", err)
	}
	defer rows.Close()
	var out []conflictedTag
	for rows.Next() {
		var ct conflictedTag
		if err := rows.Scan(&ct.Tag, &ct.Line); err != nil {
			return nil, fmt.Errorf("change.conflictedTagNames: %w", err)
		}
		out = append(out, ct)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change.conflictedTagNames: %w", err)
	}
	return out, nil
}

// conflictedLineNames returns the sorted, de-duplicated names of every LIVE
// line with at least one open conflict. When onlyLineID is non-empty it
// restricts the check to that single line; empty checks every line in the
// catalogue. Mirrors FoldLine's has_conflict lookup (fold.go) but reports the
// offending line names rather than just a count.
//
// l.status='open' AND ch.status='open' filters to live lines/changes only:
// AbandonLine (fold.go) flips a line's and its changes' status to 'abandoned'
// without touching the conflict table, so an abandoned line can leave an
// orphaned open conflict row behind. Without this filter that orphan would
// permanently block every whole-repo push even though the conflicted line no
// longer exists in any meaningful sense — mirroring FoldLine's own live-lines
// intent (it only ever looks at 'open' changes for the fold it's performing).
func (e *Engine) conflictedLineNames(onlyLineID string) ([]string, error) {
	q := `SELECT DISTINCT l.name FROM line l
		JOIN change ch ON ch.line_id = l.id
		JOIN conflict c ON c.change_id = ch.id
		WHERE c.status = 'open' AND l.status = 'open' AND ch.status = 'open'`
	var args []any
	if onlyLineID != "" {
		q += ` AND l.id = ?`
		args = append(args, onlyLineID)
	}
	q += ` ORDER BY l.name`
	rows, err := e.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("change.conflictedLineNames: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("change.conflictedLineNames: %w", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("change.conflictedLineNames: %w", err)
	}
	return names, nil
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

// embargoCapForPush caps each public ref (refs/heads/*, refs/tags/*) about to be
// pushed at its embargo boundary (PublicTip), so embargoed commits and their
// descendants are held out of the public projection, and returns a restore func
// (run via defer). No-op when nothing is embargoed. A cairn-remote push with
// embargoed content is refused: serving the real embargoed bytes to authorized
// recipients from a gated private store is the server tier (Slice 4b), not yet
// built — refusing here prevents leaking embargoed content through refs/cairn/*.
func (e *Engine) embargoCapForPush(refSpecs []config.RefSpec, isCairn bool) (func(), error) {
	noop := func() {}
	on, err := e.HasEmbargo()
	if err != nil {
		return noop, err
	}
	if !on {
		return noop, nil
	}

	iter, err := e.git.References()
	if err != nil {
		return noop, fmt.Errorf("embargoCapForPush: refs: %w", err)
	}
	type snap struct {
		name plumbing.ReferenceName
		orig plumbing.Hash
	}
	var caps []snap
	ferr := iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference || !refSpecsMatch(refSpecs, ref.Name()) {
			return nil
		}
		// The refs/cairn/embargo/* namespace deliberately carries the REAL (uncapped)
		// tips for the server's gated private store — never cap it.
		if strings.HasPrefix(ref.Name().String(), EmbargoRefPrefix) {
			return nil
		}
		caps = append(caps, snap{ref.Name(), ref.Hash()})
		return nil
	})
	if ferr != nil {
		return noop, fmt.Errorf("embargoCapForPush: %w", ferr)
	}

	var restores []func()
	restore := func() {
		for i := len(restores) - 1; i >= 0; i-- {
			restores[i]()
		}
	}
	for _, s := range caps {
		public, err := e.PublicTip(s.orig.String())
		if err != nil {
			restore()
			return noop, fmt.Errorf("embargoCapForPush: %w", err)
		}
		if public == "" {
			restore()
			return noop, fmt.Errorf("embargo: %s is embargoed to its root; nothing public to push (disclose its base commit first)", s.name)
		}
		if public == s.orig.String() {
			continue // not capped
		}
		name, orig := s.name, s.orig
		if err := e.git.Storer.SetReference(plumbing.NewHashReference(name, plumbing.NewHash(public))); err != nil {
			restore()
			return noop, fmt.Errorf("embargoCapForPush: set %s: %w", name, err)
		}
		restores = append(restores, func() { _ = e.git.Storer.SetReference(plumbing.NewHashReference(name, orig)) })
	}
	return restore, nil
}

// stageEmbargoRefs publishes the REAL (uncapped) projection into the private
// refs/cairn/embargo/* namespace for a cairn push: the full meta and each line's
// real sealed tip. The server relocates these into its gated private store; the
// public refs are capped separately (embargoCapForPush). Returns a cleanup that
// removes the staged refs after the push — they are push-time staging, not part
// of the local repo's refs. Must run AFTER Export (so refs/heads carry the real
// sealed tips) and BEFORE embargoCapForPush caps them.
func (e *Engine) stageEmbargoRefs() (func(), error) {
	noop := func() {}
	metaCommit, err := e.ExportMeta() // full, uncapped meta (real tips + embargo[])
	if err != nil {
		return noop, err
	}
	var staged []plumbing.ReferenceName
	cleanup := func() {
		for _, rn := range staged {
			_ = e.git.Storer.RemoveReference(rn)
		}
	}
	set := func(name, sha string) error {
		rn := plumbing.ReferenceName(name)
		if err := e.git.Storer.SetReference(plumbing.NewHashReference(rn, plumbing.NewHash(sha))); err != nil {
			return err
		}
		staged = append(staged, rn)
		return nil
	}
	if err := set(EmbargoRefPrefix+"meta", metaCommit); err != nil {
		cleanup()
		return noop, err
	}
	// Each line's real sealed tip (refs/heads/* still hold them pre-cap).
	iter, err := e.git.References()
	if err != nil {
		cleanup()
		return noop, err
	}
	type head struct{ branch, sha string }
	var heads []head
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		n := ref.Name().String()
		if ref.Type() == plumbing.HashReference && strings.HasPrefix(n, "refs/heads/") {
			heads = append(heads, head{strings.TrimPrefix(n, "refs/heads/"), ref.Hash().String()})
		}
		return nil
	})
	for _, h := range heads {
		if err := set(EmbargoRefPrefix+"heads/"+h.branch, h.sha); err != nil {
			cleanup()
			return noop, err
		}
	}
	return cleanup, nil
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
