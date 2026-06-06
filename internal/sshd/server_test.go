package sshd

import (
	"context"
	"crypto/ed25519"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/CarriedWorldUniverse/cairn/internal/herald"
	"github.com/CarriedWorldUniverse/cairn/internal/repo"
	gossh "golang.org/x/crypto/ssh"
)

// bootServer starts an sshd.Server on a random localhost port with the given
// core + agents, returns its addr, and registers cleanup.
func bootServer(t *testing.T, core *repo.Service, agents herald.HeraldAgents) (addr string, hostKeyPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sshd integration tests need a POSIX ssh client + host-key handling; cairn sshd is a Linux service")
	}
	_, hostPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Config{Core: core, Agents: agents, HostSigner: signer})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })

	// Persist the host's public key so the client can pin it (StrictHostKeyChecking).
	hk := filepath.Join(t.TempDir(), "known_hosts")
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	line := "[" + host + "]:" + port + " " + string(gossh.MarshalAuthorizedKey(signer.PublicKey()))
	if err := os.WriteFile(hk, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return ln.Addr().String(), hk
}

// writeCasketKey writes an Ed25519 private key in OpenSSH format and returns
// its path plus the matching herald.Agent (with cairn's fingerprint).
func writeCasketKey(t *testing.T, dir, agentID, orgID string, scopes []string) (keyPath string, agent herald.Agent) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	keyPath = filepath.Join(dir, agentID+"-casket")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}
	sshPub, _ := gossh.NewPublicKey(pub)
	fp, err := Fingerprint(sshPub)
	if err != nil {
		t.Fatal(err)
	}
	return keyPath, herald.Agent{ID: agentID, OrgID: orgID, Active: true, Scopes: scopes, Fingerprint: fp}
}

// gitEnv returns an env that forces git to use our key + known_hosts and no
// agent/askpass interference.
func gitEnv(keyPath, knownHosts string) []string {
	cmd := "ssh -i " + keyPath + " -o IdentitiesOnly=yes -o UserKnownHostsFile=" + knownHosts + " -o StrictHostKeyChecking=yes"
	return append(os.Environ(),
		"GIT_SSH_COMMAND="+cmd,
		"GIT_TERMINAL_PROMPT=0",
	)
}

func TestSSHCloneAndPush(t *testing.T) {
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
	r, err := core.CreateRepo(ctx, "org-1", "widgets")
	if err != nil {
		t.Fatal(err)
	}

	keyPath, builder := writeCasketKey(t, dir, "agent-builder", "org-1", []string{"repo:read", "repo:write"})
	agents := herald.NewFakeAgents()
	agents.Add(builder)

	addr, knownHosts := bootServer(t, core, agents)
	host, port, _ := net.SplitHostPort(addr)
	cloneURL := "ssh://git@" + host + ":" + port + "/org-1/widgets.git"

	// Clone the (empty) repo.
	work := filepath.Join(dir, "work")
	clone := exec.Command("git", "clone", cloneURL, work)
	clone.Env = gitEnv(keyPath, knownHosts)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}

	// Make a commit and push a feature branch.
	runGit(t, work, gitEnv(keyPath, knownHosts), "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(work, "f.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, gitEnv(keyPath, knownHosts), "add", ".")
	runGit(t, work, gitEnv(keyPath, knownHosts), "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-m", "x")
	push := exec.Command("git", "push", "origin", "feature")
	push.Dir = work
	push.Env = gitEnv(keyPath, knownHosts)
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}

	// The feature ref now exists in the core.
	if _, err := core.GetRef(ctx, r.ID, "refs/heads/feature"); err != nil {
		t.Fatalf("expected refs/heads/feature after push: %v", err)
	}
}

func TestSSHReaderCannotPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()
	dir := t.TempDir()
	core, _ := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
	t.Cleanup(func() { _ = core.Close() })
	_, _ = core.CreateRepo(ctx, "org-1", "widgets")

	keyPath, reader := writeCasketKey(t, dir, "agent-reader", "org-1", []string{"repo:read"})
	agents := herald.NewFakeAgents()
	agents.Add(reader)
	addr, knownHosts := bootServer(t, core, agents)
	host, port, _ := net.SplitHostPort(addr)

	// Clone works (repo:read).
	work := filepath.Join(dir, "work")
	clone := exec.Command("git", "clone", "ssh://git@"+host+":"+port+"/org-1/widgets.git", work)
	clone.Env = gitEnv(keyPath, knownHosts)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("reader clone should succeed: %v\n%s", err, out)
	}
	// Push fails (no repo:write).
	runGit(t, work, gitEnv(keyPath, knownHosts), "-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "x")
	push := exec.Command("git", "push", "origin", "HEAD:refs/heads/nope")
	push.Dir = work
	push.Env = gitEnv(keyPath, knownHosts)
	if out, err := push.CombinedOutput(); err == nil {
		t.Fatalf("reader push should fail, got success:\n%s", out)
	}
}

func runGit(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = env
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestSSHForcePushToDefaultRejected(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()
	dir := t.TempDir()
	// The pre-receive subcommand reads these from the environment; the SSH
	// session's git-receive-pack inherits the server process env.
	t.Setenv("CAIRN_DB", filepath.Join(dir, "cairn.db"))
	t.Setenv("CAIRN_REPO_ROOT", filepath.Join(dir, "repos"))
	core, _ := repo.Open(filepath.Join(dir, "cairn.db"), filepath.Join(dir, "repos"))
	t.Cleanup(func() { _ = core.Close() })

	// Wire the same pre-receive hook the binary installs, pointing at a built
	// cairn-server binary so the hook can shell back in.
	bin := buildCairnBinary(t)
	core.SetHookInstaller(func(repoID, hooksDir string) error {
		if err := os.MkdirAll(hooksDir, 0o755); err != nil {
			return err
		}
		body := "#!/bin/sh\nexec " + bin + " pre-receive " + repoID + "\n"
		return os.WriteFile(filepath.Join(hooksDir, "pre-receive"), []byte(body), 0o755)
	})

	r, _ := core.CreateRepo(ctx, "org-1", "widgets")
	keyPath, builder := writeCasketKey(t, dir, "agent-builder", "org-1", []string{"repo:read", "repo:write"})
	agents := herald.NewFakeAgents()
	agents.Add(builder)
	addr, knownHosts := bootServer(t, core, agents)
	host, port, _ := net.SplitHostPort(addr)
	url := "ssh://git@" + host + ":" + port + "/org-1/widgets.git"

	// Seed main with two commits, push (creates main), then rewrite history and
	// force-push — which must be rejected.
	work := filepath.Join(dir, "work")
	if out, err := exec.Command("git", "init", work).CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	g := func(args ...string) *exec.Cmd {
		c := exec.Command("git", append([]string{"-C", work}, args...)...)
		c.Env = gitEnv(keyPath, knownHosts)
		return c
	}
	mustRun(t, g("checkout", "-b", "main"))
	mustRun(t, g("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "c1"))
	mustRun(t, g("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "c2"))
	mustRun(t, g("remote", "add", "origin", url))
	mustRun(t, g("push", "origin", "main"))
	_ = r
	// Rewrite: drop the last commit and add a different one => non-fast-forward.
	mustRun(t, g("reset", "--hard", "HEAD~1"))
	mustRun(t, g("-c", "user.email=t@t", "-c", "user.name=t", "commit", "--allow-empty", "-m", "c2-prime"))
	out, err := g("push", "--force", "origin", "main").CombinedOutput()
	if err == nil {
		t.Fatalf("force-push to default should be rejected:\n%s", out)
	}
}

func mustRun(t *testing.T, c *exec.Cmd) {
	t.Helper()
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", c.Args, err, out)
	}
}

// buildCairnBinary compiles cmd/cairn-server into the test's temp dir and
// returns the path. The pre-receive hook shells back into it.
func buildCairnBinary(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "cairn-server")
	c := exec.Command("go", "build", "-o", out, "github.com/CarriedWorldUniverse/cairn/cmd/cairn-server")
	if o, err := c.CombinedOutput(); err != nil {
		t.Fatalf("build cairn-server: %v\n%s", err, o)
	}
	return out
}
