# cairn — k3s manifests

Single-node k3s deploy in the `cwb` namespace. cairn has **two ingresses**:

- **HTTP** (`svc/cairn`, ClusterIP `:8100`) — internal only; reach it through
  **interchange-gateway** at `/cairn`. The gateway runs herald verification and
  injects the trusted `X-CWB-*` identity over the mTLS hop; cairn trusts those.
- **SSH** (`svc/cairn-ssh`, LoadBalancer `:22` → container `:2222`) — the
  parallel, gateway-bypassing git-over-ssh ingress. Authenticated by casket
  public key → herald agent (NEX-412 by-fingerprint lookup).

## Build + load the image

```sh
podman build -f cmd/cairn-server/Containerfile -t cairn:dev .
podman save cairn:dev | sudo k3s ctr images import -
```

The runtime base is `alpine` + `git` (NOT scratch): cairn shells out to the
`git` binary for pack transfer and the pre-receive hook.

## One-time secret (SSH host key)

cairn needs a stable Ed25519 SSH host key so its host identity survives
restarts. Generate one and store it base64-std-encoded (64-byte private key):

```sh
# generate an ed25519 private key, base64-std encode the 64-byte seed||pub form
go run - <<'EOF'
package main
import ("crypto/ed25519"; "crypto/rand"; "encoding/base64"; "fmt")
func main(){ _, priv, _ := ed25519.GenerateKey(rand.Reader); fmt.Println(base64.StdEncoding.EncodeToString(priv)) }
EOF
```

```sh
HOSTKEY=$(...)   # the base64 from above
kubectl -n cwb create secret generic cairn-secrets \
  --from-literal=ssh_host_key="$HOSTKEY"
```

## Apply

```sh
kubectl apply -f deploy/k3s/
kubectl -n cwb rollout status deploy/cairn
kubectl -n cwb get pods,svc,pvc
```

## Smoke

```sh
kubectl -n cwb port-forward svc/cairn 8100:8100 &
curl -sS http://localhost:8100/healthz       # {"status":"ok","service":"cairn"}
```

## Gateway route

Add cairn to interchange-gateway's route table so `/cairn` proxies to
`http://cairn.cwb.svc:8100` (prefix stripped). The gateway injects `X-CWB-*`;
cairn trusts them. SSH does **not** go through the gateway. Concretely, add to
the gateway's `Routes` map (see `interchange/internal/gateway/gateway.go`):

```
"/cairn" -> "http://cairn.cwb.svc:8100"
```

This is an interchange-gateway change in a separate repo/PR; the cairn deploy
is not end-to-end complete until it lands.

## Notes

- `imagePullPolicy: Never` — load via `podman save cairn:dev | sudo k3s ctr images import -`.
- Storage is a `local-path` PVC (k3s default), single-node. Repos live under
  `/var/lib/nexus/repos`, the catalogue at `/var/lib/nexus/cairn.db`.
- The SSH path is **live only once NEX-412 (herald by-fingerprint) is deployed**
  and `HERALD_BASE_URL` resolves it; until then SSH auth fails closed.
- Intra-cluster traffic is plain HTTP for this dev deploy; the gateway↔cairn
  mTLS hop (mesh) is layered on later (`project_cwb_tls_everywhere`).
