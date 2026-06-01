package httpd

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/repo"
)

// boot starts an httptest server in front of the cairn HTTP handler with the
// given core, returns its base URL. The test injects X-CWB-* itself (standing
// in for the gateway).
func boot(t *testing.T, core *repo.Service) *httptest.Server {
	t.Helper()
	h := New(Config{Core: core})
	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// gitHTTPEnv mimics what the gateway would inject. (In production the client
// never sets these — the gateway does — but in this gateway-less test we stand
// in for it.)
func gitHTTPEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
	)
}

func extraHeaders(org, subject, scopes string) []string {
	return []string{
		"-c", "http.extraHeader=X-CWB-Subject: " + subject,
		"-c", "http.extraHeader=X-CWB-Org: " + org,
		"-c", "http.extraHeader=X-CWB-Kind: agent",
		"-c", "http.extraHeader=X-CWB-Scopes: " + scopes,
	}
}

func TestHTTPCloneAndPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()
	dir := t.TempDir()
	core, err := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	r, _ := core.CreateRepo(ctx, "org-1", "widgets")

	srv := boot(t, core)
	cloneURL := srv.URL + "/org-1/widgets.git"

	work := filepath.Join(dir, "work")
	args := append([]string{"clone"}, extraHeaders("org-1", "agent-builder", "repo:read repo:write")...)
	args = append(args, cloneURL, work)
	clone := exec.Command("git", args...)
	clone.Env = gitHTTPEnv()
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}

	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(extra ...string) {
		c := exec.Command("git", append([]string{"-C", work}, extra...)...)
		c.Env = gitHTTPEnv()
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", extra, err, out)
		}
	}
	run("-c", "user.email=t@t", "-c", "user.name=t", "checkout", "-b", "feature")
	run("add", ".")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "x")
	pushArgs := append([]string{"-C", work}, extraHeaders("org-1", "agent-builder", "repo:read repo:write")...)
	pushArgs = append(pushArgs, "push", "origin", "feature")
	push := exec.Command("git", pushArgs...)
	push.Env = gitHTTPEnv()
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if _, err := core.GetRef(ctx, r.ID, "refs/heads/feature"); err != nil {
		t.Fatalf("expected refs/heads/feature: %v", err)
	}
}

func TestHTTPReaderCannotPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()
	dir := t.TempDir()
	core, _ := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
	t.Cleanup(func() { _ = core.Close() })
	_, _ = core.CreateRepo(ctx, "org-1", "widgets")
	srv := boot(t, core)

	work := filepath.Join(dir, "work")
	args := append([]string{"clone"}, extraHeaders("org-1", "agent-reader", "repo:read")...)
	args = append(args, srv.URL+"/org-1/widgets.git", work)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("reader clone should succeed: %v\n%s", err, out)
	}
	c := exec.Command("git", "-C", work, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "x")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	pushArgs := append([]string{"-C", work}, extraHeaders("org-1", "agent-reader", "repo:read")...)
	pushArgs = append(pushArgs, "push", "origin", "HEAD:refs/heads/nope")
	if out, err := exec.Command("git", pushArgs...).CombinedOutput(); err == nil {
		t.Fatalf("reader push should fail:\n%s", out)
	}
}

// (Repo-admin API tests moved to internal/grpcapi with the handlers; this file
// now covers only the Smart-HTTP git ingress.)
