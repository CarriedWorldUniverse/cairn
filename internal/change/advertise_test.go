package change

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/storage/memory"
)

// probeTransport is a stand-in git server that never speaks the wire protocol:
// it only records the deadline its caller allowed for the ref advertisement,
// then answers with adv (or advErr). That deadline is the whole subject of
// #142 — go-git's Remote.List quietly imposes a 10s one.
type probeTransport struct {
	mu       sync.Mutex
	deadline time.Time
	hadDL    bool
	adv      *packp.AdvRefs
	advErr   error
}

type probeSession struct{ t *probeTransport }

func (p *probeTransport) NewUploadPackSession(*transport.Endpoint, transport.AuthMethod) (transport.UploadPackSession, error) {
	return &probeSession{t: p}, nil
}

func (p *probeTransport) NewReceivePackSession(*transport.Endpoint, transport.AuthMethod) (transport.ReceivePackSession, error) {
	return nil, errors.New("probeTransport: receive-pack not supported")
}

func (s *probeSession) AdvertisedReferences() (*packp.AdvRefs, error) {
	return s.AdvertisedReferencesContext(context.TODO())
}

func (s *probeSession) AdvertisedReferencesContext(ctx context.Context) (*packp.AdvRefs, error) {
	dl, ok := ctx.Deadline()
	s.t.mu.Lock()
	s.t.deadline, s.t.hadDL = dl, ok
	s.t.mu.Unlock()
	if s.t.advErr != nil {
		return nil, s.t.advErr
	}
	return s.t.adv, nil
}

func (s *probeSession) UploadPack(context.Context, *packp.UploadPackRequest) (*packp.UploadPackResponse, error) {
	return nil, errors.New("probeTransport: upload-pack not supported")
}

func (s *probeSession) Close() error { return nil }

// installProbe registers p under a unique scheme and returns a remote URL for
// it. client.InstallProtocol is process-global, so the scheme is per-test and
// unregistered on cleanup.
func installProbe(t *testing.T, scheme string, p *probeTransport) string {
	t.Helper()
	client.InstallProtocol(scheme, p)
	t.Cleanup(func() { client.InstallProtocol(scheme, nil) })
	return scheme + "://probe.invalid/repo.git"
}

func remoteTo(t *testing.T, url string) *git.Remote {
	t.Helper()
	r, err := git.Init(memory.NewStorage(), nil)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	rem, err := r.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{url}})
	if err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}
	return rem
}

// TestListAdvertisedRefsOutlastsGoGitsHiddenTenSecondCap is the direct
// regression test for #142. go-git's Remote.List substitutes a 10s deadline
// whenever ListOptions.Timeout is zero, while Fetch imposes none — so a repo
// whose ref advertisement takes longer than 10s (a large repo, or a slow link)
// fetched fine and then failed to answer "which branch is default?". Both
// halves are asserted together so the second can never silently become the
// first again.
func TestListAdvertisedRefsOutlastsGoGitsHiddenTenSecondCap(t *testing.T) {
	adv := packp.NewAdvRefs()
	adv.References["refs/heads/develop"] = plumbing.NewHash("1111111111111111111111111111111111111111")

	p := &probeTransport{adv: adv}
	url := installProbe(t, "cairnprobe-cap", p)
	rem := remoteTo(t, url)

	// What go-git does when cairn does NOT pass a deadline of its own.
	if _, err := rem.List(&git.ListOptions{}); err != nil {
		t.Fatalf("bare List: %v", err)
	}
	p.mu.Lock()
	bare, hadBare := p.deadline, p.hadDL
	p.mu.Unlock()
	if !hadBare || time.Until(bare) > 11*time.Second {
		t.Fatalf("go-git's List no longer imposes its ~10s cap (deadline set=%v, in %v) — "+
			"this test's premise is stale, recheck advertiseTimeout", hadBare, time.Until(bare))
	}

	// What cairn does.
	if _, err := listAdvertisedRefs(rem, nil); err != nil {
		t.Fatalf("listAdvertisedRefs: %v", err)
	}
	p.mu.Lock()
	got, hadGot := p.deadline, p.hadDL
	p.mu.Unlock()
	if !hadGot {
		t.Fatal("listAdvertisedRefs passed no deadline at all — a hung remote would hang cairn forever")
	}
	if left := time.Until(got); left <= 10*time.Second {
		t.Fatalf("listAdvertisedRefs allowed the advertisement only %v — a large repo's advertisement "+
			"still gets cut off at go-git's hidden 10s (#142)", left)
	}
}

// makeTwoHeadOrigin builds a repo whose default branch is "develop" and which
// ALSO carries "main" — the exact shape from #142, where guessing "main"
// produced a wrong-looking, apparently incomplete checkout.
func makeTwoHeadOrigin(t *testing.T, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: "refs/heads/develop"},
	})
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("develop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Add("readme.txt"); err != nil {
		t.Fatal(err)
	}
	head, err := wt.Commit("init", &git.CommitOptions{Author: &object.Signature{Name: "o", Email: "o@x"}})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	for _, name := range extra {
		ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(name), head)
		if err := r.Storer.SetReference(ref); err != nil {
			t.Fatalf("set %s: %v", name, err)
		}
	}
	return dir
}

// TestDetectDefaultRefusesToGuessWhenTheRemoteCannotBeAsked pins the second
// half of the #142 fix: when the advertisement is unavailable AND the fetched
// heads are ambiguous, detectDefault must fail loudly rather than fall back to
// "main". The old code returned "main" here, and the clone reported success on
// a checkout of the wrong branch.
func TestDetectDefaultRefusesToGuessWhenTheRemoteCannotBeAsked(t *testing.T) {
	skipOnWindows(t)
	origin := makeTwoHeadOrigin(t, "main", "release")

	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err := e.fetchRemote(origin); err != nil {
		t.Fatalf("fetchRemote: %v", err)
	}

	// Sanity: with a reachable remote the real default is detected exactly.
	def, err := e.detectDefault()
	if err != nil {
		t.Fatalf("detectDefault (reachable): %v", err)
	}
	if def != "develop" {
		t.Fatalf("detectDefault (reachable) = %q, want %q", def, "develop")
	}

	// Now the heads are all local, but the remote can no longer be asked —
	// exactly the state a timed-out/failed advertisement leaves behind.
	p := &probeTransport{advErr: errors.New("context deadline exceeded")}
	url := installProbe(t, "cairnprobe-guess", p)
	if err := e.git.DeleteRemote(originRemote); err != nil {
		t.Fatalf("DeleteRemote: %v", err)
	}
	if _, err := e.git.CreateRemote(&config.RemoteConfig{Name: originRemote, URLs: []string{url}}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}

	got, err := e.detectDefault()
	if err == nil {
		t.Fatalf("detectDefault guessed %q from an unreachable remote; it must refuse (#142)", got)
	}
	for _, want := range []string{"develop", "main", "release", "deadline"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q — the operator cannot act on it", err, want)
		}
	}
}

// TestDetectDefaultTakesASoleHeadWithoutTheRemote is the other side of the
// refusal: one fetched head is not a guess, it is the only possible answer, so
// an unreachable remote must not turn a single-branch clone into a failure.
func TestDetectDefaultTakesASoleHeadWithoutTheRemote(t *testing.T) {
	skipOnWindows(t)
	origin := makeTwoHeadOrigin(t) // develop only

	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err := e.fetchRemote(origin); err != nil {
		t.Fatalf("fetchRemote: %v", err)
	}

	p := &probeTransport{advErr: errors.New("connection reset by peer")}
	url := installProbe(t, "cairnprobe-sole", p)
	if err := e.git.DeleteRemote(originRemote); err != nil {
		t.Fatalf("DeleteRemote: %v", err)
	}
	if _, err := e.git.CreateRemote(&config.RemoteConfig{Name: originRemote, URLs: []string{url}}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}

	def, err := e.detectDefault()
	if err != nil {
		t.Fatalf("detectDefault with a sole head: %v", err)
	}
	if def != "develop" {
		t.Fatalf("detectDefault = %q, want %q", def, "develop")
	}
}

// TestDetectDefaultGuessesOnlyWhenTheRemoteHasNoDefault covers the other side
// of the #142 split. A bare repo used purely as a push target answers the
// advertisement but has no HEAD to give (go-git's PlainInit points it at a
// "master" nobody ever pushed). There is no remote answer left to contradict,
// so cairn may fall back to a conventional trunk name — but it must say out
// loud that it did, which is exactly what the old silent guess never did.
func TestDetectDefaultGuessesOnlyWhenTheRemoteHasNoDefault(t *testing.T) {
	skipOnWindows(t)
	origin := makeTwoHeadOrigin(t, "main", "release")

	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if err := e.fetchRemote(origin); err != nil {
		t.Fatalf("fetchRemote: %v", err)
	}

	// A remote that answers cleanly but advertises heads only — no HEAD.
	adv := packp.NewAdvRefs()
	adv.References["refs/heads/develop"] = plumbing.NewHash("1111111111111111111111111111111111111111")
	p := &probeTransport{adv: adv}
	url := installProbe(t, "cairnprobe-nodefault", p)
	if err := e.git.DeleteRemote(originRemote); err != nil {
		t.Fatalf("DeleteRemote: %v", err)
	}
	if _, err := e.git.CreateRemote(&config.RemoteConfig{Name: originRemote, URLs: []string{url}}); err != nil {
		t.Fatalf("CreateRemote: %v", err)
	}

	var warned []string
	orig := warnf
	warnf = func(format string, args ...any) { warned = append(warned, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { warnf = orig })

	def, err := e.detectDefault()
	if err != nil {
		t.Fatalf("detectDefault: %v", err)
	}
	if def != "main" {
		t.Fatalf("detectDefault = %q, want the conventional %q", def, "main")
	}
	if len(warned) != 1 || !strings.Contains(warned[0], "main") {
		t.Fatalf("the fallback was silent (warnings: %v) — the operator gets no signal that the branch was guessed", warned)
	}
}
