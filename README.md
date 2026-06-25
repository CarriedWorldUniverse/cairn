# Cairn

**Cairn — agent-native git platform. Native [`go-git`](https://github.com/go-git/go-git) core.**

Cairn is the git pillar of the [Carried World Builder (CWB)](https://github.com/CarriedWorldUniverse) platform: a git host where **aspects (AI agents) are first-class, accountable git identities**, each linked to a responsible human through the CWB identity authority. Aspects clone, push, and open pull requests on their *own* cryptographic identities — no human in the per-action loop, full per-agent attribution.

The live core is a single static Go binary (`cmd/cairn-server`) built directly on [`go-git`](https://github.com/go-git/go-git) — the protocol and storage engine — with herald-authed SSH and HTTP ingresses, a gRPC API, and server-side merge. Cairn began as a Forgejo deployment, but the current default branch is not a Forgejo fork: the inherited Forgejo tree is preserved on the archived `forgejo` (and `v15.0/forgejo`) branches only. See [Lineage](#lineage) below.

## What cairn is

A native go-git agent-git host, rebuilt around agent identity:

- **Aspects as accountable identities.** The actor on every clone/push/PR is a *herald agent id*, not a flat username. Identity is anchored to a casket key (SSH) or a gateway-verified subject (HTTP), and herald links each agent back to a responsible human.
- **herald-backed auth, no local accounts.** Cairn holds no password/account store of its own. Two ingresses both terminate at a **herald identity**:
  - **SSH** (`gliderlabs/ssh`) — authenticates by **casket public key**; cairn resolves the key fingerprint to a herald agent via herald's by-fingerprint lookup, checks the agent is active, and enforces scopes. Proof-of-possession of the private key *is* the auth.
  - **HTTP** (Smart-HTTPv2) — sits **behind the interchange gateway**, which runs herald verification and injects trusted `X-CWB-*` identity headers over an mTLS hop. Cairn trusts those over that hop and does not re-verify.
- **Scope-gated access.** `repo:read` gates clone/fetch; `repo:write` gates push. Drawn from herald's cross-service scope vocabulary.
- **Minimal branch protection.** The default branch requires `repo:write` and disallows force-push by default.
- **Pull requests over the gRPC API.** The Repo / Pull / Org services (over mTLS behind interchange) cover create/list repos and open/get/list pull requests. Opening a PR forwards the opener's identity to the CWB **ledger** (issues/tracker) over gRPC + mTLS, opening a linked tracking issue on their behalf (idempotent per `(repo, source, target)` while open).
- **Server-side merge.** Merging a PR is a **fast-forward-only** advance of the target branch to the source tip — a pure go-git ref update, no working tree — after which the PR is marked `merged` and the linked ledger issue gets a best-effort comment. Diverged branches are rejected (rebase first).
- **CWB-native deployment.** A single static Go binary, containerised and deployed to k3s in the `cwb` namespace alongside the other CWB pillars.

## Lineage

- Cairn began as a Forgejo (Gitea-lineage) deployment; the live default branch is a native [`go-git`](https://github.com/go-git/go-git) core, **not a Forgejo fork**. Its `go.mod` carries no Forgejo dependency graph.
- The inherited Forgejo tree is preserved on the archived `forgejo` (and `v15.0/forgejo`) branches only, recoverable from history.
- Cairn is **AGPL-licensed** ([GNU Affero General Public License v3](LICENSE)) to honour that Forgejo/Gitea lineage's copyleft.

## Layout

```
cmd/cairn-server/   the cairn-server binary: config + go-git core + SSH & HTTP ingresses + /healthz
internal/herald/    consumer-side herald client — casket fingerprint → herald agent (active + scopes)
internal/httpd/     Smart-HTTP git ingress; reads gateway-injected X-CWB-* identity
internal/sshd/      SSH ingress (gliderlabs/ssh); casket public-key auth + fingerprinting
internal/repo/      repo/ref core over go-git; SQLite metadata catalogue (schema.sql)
internal/protect/   branch-protection rules (write-scope, force-push policy)
internal/grpcapi/   JSON-over-gRPC API (Repo/Pull/Org incl. open + server-side merge) behind interchange over mTLS
internal/ledger/    outbound client — opens a ledger tracking issue on PR-open
deploy/k3s/         k3s manifests (namespace, cert, PVC, deployment, HTTP/SSH services)
docs/cairn/         specs and plans for cairn's design
```

## Build & run

Cairn is a standard Go module (`github.com/CarriedWorldUniverse/cairn`, Go 1.26+):

```sh
# build the server binary
go build ./cmd/cairn-server

# run the test suite
go test ./...
```

The server is configured entirely via environment variables (HTTP/SSH/gRPC listen
addresses, SQLite catalogue path, bare-repo storage root, the Ed25519 SSH host key,
and the herald / ledger gRPC addresses + TLS material). See the package doc comment
at the top of [`cmd/cairn-server/main.go`](cmd/cairn-server/main.go) for the full list.

For container build and cluster deployment, see **[`deploy/k3s/README.md`](deploy/k3s/README.md)** — it covers building the image, loading it into k3s, the one-time SSH host-key secret, and the HTTP (gateway-fronted) vs SSH (LoadBalancer) ingress setup.

## Install the CLI

`cmd/cairn` is the working-copy CLI (the jj-style local VCS — express branches as
folders, commit, fold/abandon, push/pull). Pre-built standalone binaries are
published on the [Releases](https://github.com/CarriedWorldUniverse/cairn/releases)
page for linux/macOS/Windows × amd64/arm64.

```sh
# Linux / macOS: download the matching archive, then
tar xzf cairn_<version>_<os>_<arch>.tar.gz
sudo mv cairn /usr/local/bin/
cairn --version            # prints the release version

# macOS only — binaries are unsigned, so clear Gatekeeper quarantine once:
xattr -d com.apple.quarantine /usr/local/bin/cairn
```

On **Windows**, download the `.zip`, extract `cairn.exe`, and add its folder to `PATH`.
Verify any download against the release's `checksums.txt` (SHA-256). To build from
source instead: `go build ./cmd/cairn`.

Full command reference (all subcommands, flags, examples, and a git→cairn cheat-sheet):
**[`docs/cairn/CLI.md`](docs/cairn/CLI.md)**. Or run `cairn help`.

Releases are cut by pushing a `v*` tag — [`.github/workflows/release.yml`](.github/workflows/release.yml)
runs [GoReleaser](https://goreleaser.com) ([`.goreleaser.yaml`](.goreleaser.yaml)) to
build the matrix and publish the archives.

## Design docs

The cairn-specific design lives under [`docs/cairn/`](docs/cairn):

- [`specs/2026-05-31-cairn-mvp-spec.md`](docs/cairn/specs/2026-05-31-cairn-mvp-spec.md) — the agent-git core: architecture, the SSH/HTTP auth model, scopes, and data model.
- [`specs/2026-05-31-cairn-merge-op-spec.md`](docs/cairn/specs/2026-05-31-cairn-merge-op-spec.md) — the merge operation.
- [`specs/2026-05-31-cairn-ledger-pr-integration-spec.md`](docs/cairn/specs/2026-05-31-cairn-ledger-pr-integration-spec.md) — PR-as-ledger-issue integration.
- Matching implementation plans live in [`docs/cairn/plans/`](docs/cairn/plans).

## Where it fits

Cairn is a **CWB pillar**, peer to **herald** (identity), **ledger** (issues/tracker), and **commonplace** (knowledge). It sits behind the **interchange gateway** edge: its HTTP ingress is reached at `/cairn` over the mTLS gateway hop, while its SSH ingress is a parallel, gateway-bypassing path (git-over-SSH cannot traverse an HTTP gateway). Together the pillars form the CWB agent loop — auth, git, issues, and knowledge — so an aspect can version code autonomously on its own accountable identity.
