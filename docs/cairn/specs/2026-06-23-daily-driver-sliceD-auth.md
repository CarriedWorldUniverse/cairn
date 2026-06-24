# cairn daily-driver — Slice D: remotes & authentication

**Status:** draft · 2026-06-23 · from the daily-driver audit (dimension 2)
**Goal:** make cairn work against **real** GitHub/GitLab repos. Today NO `AuthMethod` is passed to go-git, so only anonymous public clone/fetch works and **every push fails**. This is the single biggest "can't actually use it" gap. Add credential resolution (token / `git credential` / ssh) threaded into the four transport calls, plus humanized errors.

## Scope (3 tasks)

### D1 — credential resolver (`internal/change/auth.go`)
`func authFor(rawurl string) (transport.AuthMethod, error)` — resolve an auth method by URL scheme; return `nil` (anonymous) when nothing applies (so public HTTPS and `file://` keep working unchanged).
- **SSH** (`git@host:path` or `ssh://…`): prefer the ssh-agent (`$SSH_AUTH_SOCK` set → `ssh.NewSSHAgentAuth(user)`), else the first existing default key (`~/.ssh/id_ed25519`, `~/.ssh/id_rsa`) via `ssh.NewPublicKeysFromFile(user, path, "")`. `user` = the URL's user (default `git`). If none available → return the agent/key error (so the user learns to set up SSH).
- **HTTP(S)**: precedence —
  1. env token: first non-empty of `$CAIRN_TOKEN`, `$GITHUB_TOKEN`, `$GITLAB_TOKEN` → `&http.BasicAuth{Username: "x-access-token", Password: token}` (GitHub/GitLab accept any non-empty username with a PAT as the password; `x-access-token` is the GitHub convention).
  2. **`git credential` bridge**: shell out to `git credential fill` with `protocol`/`host` from the URL; if it returns a `username`/`password`, use `&http.BasicAuth{...}`. This inherits whatever the user already configured for `git` (keychain/helper) — zero new config, the highest-value path.
  3. else `nil` (anonymous — public repos still clone).
- **`file://` / other**: `nil`.
- Helpers: `isSSHURL`, `firstEnv(keys...)`, `gitCredentialFill(scheme, host) (user, pass string, ok bool)` (exec `git credential fill`, parse `key=value` lines; any error/`git` absent → `ok=false`, never fatal).

### D2 — thread auth into the four transport calls
Every call site already has the remote `rem` in scope; resolve `auth, err := authFor(rem.Config().URLs[0])` (bail on a real ssh-key error; a nil auth is fine) and set `Auth: auth`:
- `internal/change/importer.go:127` `rem.Fetch(&git.FetchOptions{… Auth: auth})` (clone/import).
- `internal/change/importer.go:155` `rem.List(&git.ListOptions{Auth: auth})` (detect default branch).
- `internal/change/sync.go:46` `rem.Fetch(&git.FetchOptions{… Auth: auth})` (fetch/pull).
- `internal/change/push.go:154` `rem.Push(&git.PushOptions{… Auth: auth})` (push).
- A small `func (e *Engine) authForRemote(rem *git.Remote) (transport.AuthMethod, error)` wrapper keeps the call sites tidy. `file://` local-fixture tests get `nil` → behavior unchanged (existing clone/push/pull tests stay green).

### D3 — humanize remote/network/auth errors
`cairn clone/push/fetch/pull` surface raw go-git strings today. Add a `mapRemoteErr(err) error` (used by the four remote CLI commands, or fold into `mapErr`) translating common transport failures into actionable messages:
- `transport.ErrAuthenticationRequired` / `ErrAuthorizationFailed` → `authentication failed — set $CAIRN_TOKEN (a PAT) for HTTPS, or check your ssh-agent/key for SSH`.
- `transport.ErrRepositoryNotFound` → `repository not found at <url> (check the URL and your access)`.
- network (`*net.OpError`, timeout, `no such host`, connection refused) → `could not reach <remote> — check the URL and your network`.
- keep `NoErrAlreadyUpToDate`/non-fast-forward handling as-is (already good).

## Out of scope
The `--cairn` remote kind staying a no-op (documented seam), annotated-tag fidelity, multi-account credential selection beyond the env+helper precedence. The privacy-server features remain a later phase.

## Testing / DoD
- D1 unit (`auth_test.go`): `authFor` classification — `file://`→nil; `https://…` with `$CAIRN_TOKEN` set → `*http.BasicAuth` with that password (use `t.Setenv`); `https://` with no token/helper → nil (anonymous); `git@github.com:org/repo.git` classified as SSH (assert it attempts ssh — at minimum `isSSHURL` true; the agent/key resolution may error in CI with no agent, so assert the branch taken, not a live connection). `firstEnv` precedence; `gitCredentialFill` returns `ok=false` cleanly when `git` is absent or declines.
- D2: existing `file://` clone/push/pull e2e stay green (auth=nil). Add a test that a push to a bogus authenticated URL surfaces `mapRemoteErr`'s auth message rather than a raw go-git string (can stub by calling `mapRemoteErr` on a `transport.ErrAuthenticationRequired`).
- D3: unit-test `mapRemoteErr` maps each sentinel to its actionable message.
- Full gate + cross-compile; `skipOnWindows` where local-fixture transport is used; all prior phases unaffected. **No live network in tests** — exercise the resolution + error-mapping logic, not real GitHub.
